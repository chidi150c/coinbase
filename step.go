// ---------------------------------------------------------------------------------------------
// FILE: step.go — Synchronized trading tick (EXIT → OPEN), extracted from trader.go
//
// Overview
//
//	step(ctx, candles) is the single-threaded decision loop that reads the latest market
//	snapshot, evaluates exits (profit-gate, trailing, fixed TP), then evaluates a new entry
//	(market or maker-first limit) — in that strict order. It returns a short, human-readable
//	status for logs/metrics and an error if any broker call fails.
//
// Inputs / Outputs
//
//	Input:  []Candle history (last element is the most recent mark/close).
//	        Context is used for broker/network timeouts and cancellation.
//	Output: (msg string, err error) where msg ∈ {"EXIT …","OPEN …","HOLD","FLAT …","OPEN-PENDING …"}.
//
// Concurrency & Locks
//   - Takes t.mu at the top to read/update in-memory state, and RELEASES it around ANY I/O
//     (broker calls, price fetches, Slack). Every unlock is paired with a re-lock before
//     mutating state again.
//   - Close at most ONE lot per tick to keep behavior predictable.
//
// Deterministic Flow
//  1. Daily roll/metrics refresh
//  2. EXIT scan per side (BUY then SELL):
//     - Compute fee-aware net PnL and check profit gate
//     - If gate passes:
//     • Runner/Scalp trailing: USD-based trailing; close on stop trigger
//     • Fixed-TP scalp: maintain maker-friendly TP preview (emulated post-only)
//  3. OPEN evaluation (if no exit fired):
//     - Pull balances/steps with lock released
//     - Enforce MinNotional/OrderMinUSD and step/tick snapping symmetrically
//     - Equity triggers may override pyramiding/ramping gates
//     - If ORDER_TYPE=limit with offset+timeout → maker-first (async pending)
//     else place market immediately
//
// Maker-First Async Opens (Post-Only)
//   - Per-side PendingOpen is persisted and polled until filled/timeout; channels deliver the result.
//   - On fill: append lot using actual fill price/size/fee and record EntryOrderID.
//   - On timeout/error: set a per-side “recheck” flag permitting one market fallback later.
//   - RehydratePending() can restore polling after restart using saved OrderID+Deadline.
//
// Repricing Guardrails (async maker path)
//   - Optional repricing loop honors cfg: RepriceEnable, RepriceIntervalMs, RepriceMaxCount,
//     RepriceMaxDriftBps, RepriceMinImprovTicks, RepriceMinEdgeUSD, PriceTick, BaseStep, MinNotional.
//
// Pyramiding & Equity Triggers
//   - Pyramiding adds are side-aware and gated by spacing (seconds) and adverse-move thresholds,
//     with optional exponential decay & latching. Equity triggers can stage sizes (25/50/75/100%)
//     and may auto-designate the new lot as the side’s runner.
//
// Fees, Notional & Sizing
//   - Entry/exit PnL is fee-aware. Prefer broker-reported commission; fallback to FeeRatePct.
//   - All orders satisfy exchange min-notional and step/tick rules before submission.
//
// Persistence & IDs
//   - State mutations (equity, books, exits, pending) are persisted opportunistically.
//   - Lots carry LotID and EntryOrderID; NextLotSeq is incremented on each append.
//
// Dry-Run Behavior
//   - Simulates fees and adjusts equity locally; no broker calls.
//
// Logging & Metrics
//   - TRACE/DEBUG breadcrumbs at key gates (spacing, latching, trailing, post-only lifecycle).
//     Prometheus-style counters/gauges are updated for opens/exits.
//
// ---------------------------------------------------------------------------------------------
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"
)

// ---- Runner helpers (minimal addition to support multiple runners) ----
func isRunner(book *SideBook, idx int) bool {
	if book == nil || len(book.RunnerIDs) == 0 {
		return false
	}
	for _, rid := range book.RunnerIDs {
		if rid == idx {
			return true
		}
	}
	return false
}

func addRunner(book *SideBook, idx int) {
	if book == nil || idx < 0 || idx >= len(book.Lots) {
		return
	}
	for _, rid := range book.RunnerIDs {
		if rid == idx {
			return
		}
	}
	book.RunnerIDs = append(book.RunnerIDs, idx)
}

func runnerCount(book *SideBook) int {
	if book == nil {
		return 0
	}
	return len(book.RunnerIDs)
}

// rampCount returns the number of non-dust lots on a side book for ramp / k
// purposes. Lots whose current notional (SizeBase * px) is < minNotional do
// NOT count toward k; this is a belt-and-braces guard in case any dust
// survives consolidation.
func rampCount(book *SideBook, px, minNotional float64) int {
	if book == nil {
		return 0
	}
	if px <= 0 || minNotional <= 0 {
		// No meaningful notion of "dust" → fall back to raw count.
		return len(book.Lots)
	}
	n := 0
	for _, lot := range book.Lots {
		if lot.SizeBase*px >= minNotional {
			n++
		}
	}
	return n
}

// ---- Core tick ----
// safeSend ensures we deliver a result even if the buffer is momentarily full.
// It will drop one stale item from the channel buffer and resend the latest.
func safeSend(ch chan OpenResult, res OpenResult) {
	select {
	case ch <- res:
	default:
		log.Printf("TRACE fallback.buffer.full: empty the buffer (drop stale) and resending")
		select {
		case <-ch:
		default:
		}
		log.Printf("TRACE fallback.buffer.emptied: emptied buffer and resending")
		ch <- res
	}
}

// creditRefundService records the opposite-side spare created by the refund-service
// portion of an entry order. The refund-service portion is removed from the open
// lot exposure, but its net proceeds/inventory must remain available for the
// side that was previously short/blocked.
//
// BUY entry + refund portion  => restores base inventory for future SELLs, tracked as SpareSellUSD.
// SELL entry + refund portion => restores quote inventory for future BUYs, tracked as SpareBuyUSD.
func (t *Trader) creditRefundService(side OrderSide, refundQuote, refundFee float64) {
	if refundQuote <= 0 {
		return
	}
	if refundFee < 0 {
		refundFee = 0
	}

	refundNet := refundQuote - refundFee
	if refundNet < 0 {
		refundNet = 0
	}

	if side == SideBuy {
		t.SpareSellUSD += refundNet
		log.Printf("TRACE refund.sell.service_credited side=%s gross=%.8f fee=%.8f net=%.8f spareSell_after=%.8f",
			side, refundQuote, refundFee, refundNet, t.SpareSellUSD)
		return
	}

	t.SpareBuyUSD += refundNet
	log.Printf("TRACE refund.buy.service_credited side=%s gross=%.8f fee=%.8f net=%.8f spareBuy_after=%.8f",
		side, refundQuote, refundFee, refundNet, t.SpareBuyUSD)
}

// helper (recommended): persist pending changes safely under lock
func (t *Trader) repriceUpdatePending(side OrderSide, newID string, newLimitPx, newBase float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var p *PendingOpen
	if side == SideBuy {
		p = t.pendingBuy
	} else {
		p = t.pendingSell
	}
	if p == nil {
		return
	}

	// append previous id to history (if any), cap to last 5
	if p.OrderID != "" {
		p.History = append(p.History, p.OrderID)
		if len(p.History) > 5 {
			p.History = p.History[len(p.History)-5:]
		}
	}

	// transfer focus to the new live order
	p.OrderID = newID
	p.LimitPx = newLimitPx
	p.BaseAtLimit = newBase

	// persist immediately so state reflects the current live order
	_ = t.saveStateFrom(t.snapshotStateLocked())
}

// maybeRepriceOnce tries a single reprice pass for the current pending order.
// It returns:
//
//	newOrderID        → possibly updated (if we re-priced successfully), else the input orderID
//	newLastLimitPx    → updated lastLimitPx if we re-priced
//	newRepriceCount   → incremented if we re-priced
//	didReprice        → true if we re-priced (placed a new order)
//
// NOTE: caller should throttle by interval (e.g., time.Since(lastReprice) >= cfg.RepriceIntervalMs).
func (t *Trader) maybeRepriceOnce(
	pctx context.Context,
	side OrderSide,
	orderID string,
	initLimitPx float64,
	initBaseAtLimit float64,
	lastLimitPx float64,
	offsetBps float64,
	pend *PendingOpen,
	repriceCount int,
) (string, float64, int, bool) {

	// Global guards
	if !t.cfg.RepriceEnable {
		return orderID, lastLimitPx, repriceCount, false
	}
	if t.cfg.RepriceMaxCount > 0 && repriceCount >= t.cfg.RepriceMaxCount {
		return orderID, lastLimitPx, repriceCount, false
	}

	bid, ask, bErr := t.broker.GetBBO(pctx, t.cfg.ProductID)
	useBBO := (bErr == nil && bid > 0 && ask > bid)
	var newLimitPx float64
	if useBBO {
		if side == SideBuy {
			newLimitPx = bid
		} else {
			newLimitPx = ask
		}
	} else {
		// existing mark±offset path
		// Fresh snap with small timeout
		ctxPx, cancelPx := context.WithTimeout(pctx, 1*time.Second)
		px, gErr := t.broker.GetNowPrice(ctxPx, t.cfg.ProductID)
		cancelPx()
		if gErr != nil || px <= 0 {
			return orderID, lastLimitPx, repriceCount, false
		}
		if side == SideBuy {
			newLimitPx = px * (1.0 - offsetBps/10000.0)
		} else {
			newLimitPx = px * (1.0 + offsetBps/10000.0)
		}
	}

	// snap to tick
	tick := t.cfg.PriceTick
	if tick > 0 {
		if side == SideBuy {
			newLimitPx = math.Floor(newLimitPx/tick) * tick // round down for buys
		} else {
			newLimitPx = math.Ceil(newLimitPx/tick) * tick // round up for sells  ✅
		}
	}
	// anti-cross nudge when using BBO
	if useBBO && tick > 0 {
		if side == SideBuy {
			if newLimitPx >= ask {
				cand := ask - tick
				if cand <= 0 {
					return orderID, lastLimitPx, repriceCount, false
				}
				newLimitPx = cand
			}
		} else {
			if newLimitPx <= bid {
				newLimitPx = bid + tick
			}
		}
	} else if useBBO && tick <= 0 {
		// If no tick, still ensure we don't cross the book when using BBO
		if side == SideBuy && newLimitPx >= ask {
			newLimitPx = math.Nextafter(ask, 0)
		} // nudge below ask
		if side == SideSell && newLimitPx <= bid {
			newLimitPx = math.Nextafter(bid, +1)
		} // nudge above bid
	}

	// Baseline: only reprice if snapped price changed
	shouldReprice := (tick > 0 && math.Abs(newLimitPx-lastLimitPx) >= tick) || (tick <= 0 && newLimitPx != lastLimitPx)

	// Guard: max drift from initial (bps)
	if shouldReprice && t.cfg.RepriceMaxDriftBps > 0 {
		driftBps := math.Abs((newLimitPx-initLimitPx)/initLimitPx) * 10000.0
		if driftBps > t.cfg.RepriceMaxDriftBps {
			shouldReprice = false
		}
	}

	// Recompute base from original quote for edge calc & placement
	newBase := initBaseAtLimit
	if pend != nil && pend.Quote > 0 {
		newBase = pend.Quote / newLimitPx
	}

	// ✱ Snap base to step BEFORE any economic/min-notional checks
	if t.cfg.BaseStep > 0 {
		newBase = math.Floor(newBase/t.cfg.BaseStep) * t.cfg.BaseStep
	}

	// Ensure min-notional for the reprice candidate
	if shouldReprice && !(newBase > 0 && newBase*newLimitPx >= t.cfg.MinNotional) {
		shouldReprice = false
	}

	if !shouldReprice {
		return orderID, lastLimitPx, repriceCount, false
	}

	if useBBO {
		log.Printf("TRACE postonly.reprice.touch side=%s bid=%.8f ask=%.8f new=%.8f last=%.8f", side, bid, ask, newLimitPx, lastLimitPx)
	} else {
		log.Printf("TRACE postonly.reprice.mark side=%s new=%.8f last=%.8f", side, newLimitPx, lastLimitPx)
	}

	// Cancel current and re-place at the new price
	_ = t.broker.CancelOrder(pctx, t.cfg.ProductID, orderID)
	newID, perr := t.broker.PlaceLimitPostOnly(pctx, t.cfg.ProductID, side, newLimitPx, newBase)
	if perr != nil || strings.TrimSpace(newID) == "" {
		return orderID, lastLimitPx, repriceCount, false
	}

	log.Printf("TRACE postonly.reprice side=%s old_id=%s new_id=%s limit=%.8f baseReq=%.8f",
		side, orderID, newID, newLimitPx, newBase)

	// Update focus to new order + persist (also appends old ID to History)
	t.repriceUpdatePending(side, newID, newLimitPx, newBase)

	// Return updated state
	return newID, newLimitPx, repriceCount + 1, true
}

// step consumes the current candle history and may place/close a position.
// It returns a human-readable status string for logging.
func (t *Trader) step(ctx context.Context, execHistory []Candle, signalHistory []Candle, livePrice float64) (string, error) {
	c := execHistory

	if len(c) == 0 {
		return "NO_DATA", nil
	}

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	// Use wall clock as authoritative "now" for pyramiding timings; fall back for zero candle time.
	wallNow := time.Now().UTC()

	now := c[len(c)-1].Time
	if now.IsZero() {
		now = wallNow
	}
	t.updateDaily(now)

	// -------------------------------------------------------------------------------------------------
	// Drain completed BUY maker-first order result, if any.
	//
	// This block handles the asynchronous post-only BUY lifecycle. The maker order is submitted earlier
	// and polled in the background. On each step(), we non-blockingly check whether that goroutine sent
	// a result.
	//
	// Important:
	// - This block must not block the trading loop.
	// - A result is accepted only if it matches the current pending order or one of its repriced history IDs.
	// - If a fill arrives but pending state is missing, we still accept it to avoid orphaning a real broker fill.
	// On timeout/error/non-fill, set pendingRecheckBuy=true only if the order was not
	// canceled because the signal changed. Signal-change cancels must not allow market fallback.
	// - The pending object is cleared at the end regardless of fill/non-fill.
	// -------------------------------------------------------------------------------------------------
	if t.pendingBuyCh != nil {
		select {
		case res := <-t.pendingBuyCh:
			log.Printf("TRACE postonly.drain.recv side=%s order_id=%s filled=%v placed_nil=%v",
				SideBuy, res.OrderID, res.Filled, res.Placed == nil)

			// Decide whether this async result is safe to apply to state.
			// Repricing can create multiple order IDs, so we accept:
			// 1) the current pending order ID, or
			// 2) any historical replaced order ID recorded in PendingOpen.History.
			accept := false
			if res.Filled && res.Placed != nil {
				log.Printf("TRACE postonly.drain.placed side=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f",
					SideBuy, res.OrderID, res.Placed.Price, res.Placed.BaseSize, res.Placed.QuoteSpent, res.Placed.CommissionUSD)

				if t.pendingBuy != nil {
					if res.OrderID == t.pendingBuy.OrderID {
						accept = true
					} else {
						for _, hid := range t.pendingBuy.History {
							if res.OrderID == hid {
								accept = true
								break
							}
						}
					}
				} else {
					// A real fill without in-memory pending state is safer to accept than ignore.
					// This can happen after restart/state mismatch/channel timing.
					accept = true
					log.Printf("[WARN] postonly.fill.without_pending side=%s order_id=%s", SideBuy, res.OrderID)
				}
			}

			if accept {
				// Convert the broker fill into a BUY lot using actual execution data.
				// Prefer broker-reported commission; if unavailable, estimate from FeeRatePct.
				side := SideBuy
				book := t.book(side)
				priceToUse := res.Placed.Price
				baseToUse := res.Placed.BaseSize
				quoteSpent := res.Placed.QuoteSpent
				entryFee := res.Placed.CommissionUSD
				if entryFee <= 0 {
					entryFee = quoteSpent * (t.cfg.FeeRatePct / 100.0)
				}

				// Diagnostic: was the fill accepted by current ID or repriced-history ID?
				matchHistory := false
				if t.pendingBuy != nil {
					for _, id := range t.pendingBuy.History {
						if id == res.OrderID {
							matchHistory = true
							break
						}
					}
				}

				log.Printf("TRACE postonly.drain.accept side=%s match_current=%v match_history=%v pending_nil=%v",
					SideBuy,
					t.pendingBuy != nil && res.OrderID == t.pendingBuy.OrderID,
					matchHistory,
					t.pendingBuy == nil,
				)

				// Refund-service adjustment:
				// If part of the fill was intended to restore opposite-side inventory/spare,
				// remove that portion from the open lot and credit it to the appropriate spare bucket.
				if t.pendingBuy != nil && t.pendingBuy.RefundPortionUSD > 0 {
					origBase := baseToUse
					origQuote := quoteSpent
					origFee := entryFee

					refundBase := t.pendingBuy.RefundPortionUSD / priceToUse
					if refundBase > baseToUse {
						refundBase = baseToUse
					}

					keptBase := baseToUse - refundBase
					if keptBase < 0 {
						keptBase = 0
					}

					keptQuote := quoteSpent
					keptFee := entryFee
					refundQuote := t.pendingBuy.RefundPortionUSD
					refundFee := refundQuote * (t.cfg.FeeRatePct / 100.0)

					if origBase > 0 {
						keptQuote = origQuote * (keptBase / origBase)
						keptFee = origFee * (keptBase / origBase)
						refundQuote = origQuote * (refundBase / origBase)
						refundFee = origFee * (refundBase / origBase)
					}

					t.creditRefundService(SideBuy, refundQuote, refundFee)

					baseToUse = keptBase
					quoteSpent = keptQuote
					entryFee = keptFee
				}

				// Build the open BUY lot.
				//
				// ConfidenceMult / EntryAIMode / ProfitGateUSD should come from PendingOpen,
				// because this fill may occur many ticks after the decision that created it.
				// Do not rely on current d/confMult here unless this block is guaranteed to run
				// in the same tick as placement.
				newLot := &Position{
					OpenPrice:       priceToUse,
					Side:            side,
					SizeBase:        baseToUse,
					OpenTime:        now,
					EntryFee:        entryFee,
					OpenNotionalUSD: quoteSpent,
					Reason:          "async postonly filled",
					Take:            0,
					Version:         1,
					LotID:           len(book.Lots),
					EntryOrderID:    res.OrderID,
				}

				// Restore decision-time metadata from pending state.
				if t.pendingBuy != nil {
					newLot.Reason = t.pendingBuy.Reason
					newLot.RefundPortionUSD = t.pendingBuy.RefundPortionUSD
					newLot.Take = t.pendingBuy.Take
					newLot.ConfidenceMult = t.pendingBuy.ConfidenceMult
					newLot.EntryAIMode = t.pendingBuy.EntryAIMode
					newLot.ProfitGateUSD = t.pendingBuy.ProfitGateUSD
				}
				if newLot.ConfidenceMult <= 0 {
					newLot.ConfidenceMult = 0.0
				}
				if newLot.EntryAIMode == "" {
					newLot.EntryAIMode = "UNKNOWN"
				}
				if newLot.ProfitGateUSD <= 0 {
					newLot.ProfitGateUSD = t.cfg.ProfitGateUSD
				}

				log.Printf(
					"[KPI] lot.created side=%s mode=%s conf=%.2f gate=%.2f",
					newLot.Side,
					newLot.EntryAIMode,
					newLot.ConfidenceMult,
					newLot.ProfitGateUSD,
				)

				book.Lots = append(book.Lots, newLot)

				// Clean up any dust created by partial fills/refund service.
				t.consolidateDust(book, priceToUse, t.cfg.MinNotional)
				t.didConsolidateStartup = false

				// Deduct quote actually kept as open BUY exposure.
				t.SpareBuyUSD -= quoteSpent
				if t.SpareBuyUSD < 0 {
					t.SpareBuyUSD = 0
				}

				// Equity-triggered BUY fills are promoted to runner status.
				if t.pendingBuy != nil && t.pendingBuy.EquityBuy {
					newIdx := len(book.Lots) - 1
					addRunner(book, newIdx)

					r := book.Lots[newIdx]
					r.TrailActive = false
					r.TrailPeak = r.OpenPrice
					r.TrailStop = 0
					t.applyRunnerTargets(r)

					log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f",
						newIdx, side, r.OpenPrice, r.Take)
				}

				// Reset BUY-side add anchors after accepted fill.
				t.lastAddBuy = wallNow
				t.winLowBuy = priceToUse
				t.latchedGateBuy = 0

				old := t.lastAddEquityBuy
				t.lastAddEquityBuy = t.equityUSD
				log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f",
					side, old, t.lastAddEquityBuy)

				msg := fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
					side, quoteSpent, newLot.Take, entryFee, newLot.Reason, "async postonly filled")

				if t.cfg.UseDirectSlack {
					postSlack(msg)
				}

				if err := t.saveStateNoLock(); err != nil {
					log.Printf("[WARN] saveState (filled BUY): %v", err)
				}
			} else {
				// Non-fill result: allow the normal entry path to re-check and possibly fallback.
				cancelRequested := t.pendingBuy != nil && t.pendingBuy.CancelRequested

				if cancelRequested {
					t.pendingRecheckBuy = false
					log.Printf(
						"TRACE postonly.cancel.ack side=%s order_id=%s fallback=false reason=signal_changed",
						SideBuy,
						res.OrderID,
					)
				} else {
					t.pendingRecheckBuy = true
					log.Printf(
						"TRACE postonly.recheck side=%s set=true reason=timeout_or_error order_id=%s",
						SideBuy,
						res.OrderID,
					)
				}
			}

			// Pending lifecycle is finished for this result, whether filled or not.
			// Cancel context, clear pointers, and persist so restart state is clean.
			if t.pendingBuyCancel != nil {
				t.pendingBuyCancel()
			}
			t.pendingBuy = nil
			t.pendingBuyCtx = nil
			t.pendingBuyCancel = nil

			if err := t.saveStateNoLock(); err != nil {
				log.Printf("[WARN] saveState (drain BUY): %v", err)
			}

		default:
			// No async BUY result this tick.
		}
	}

	// -------------------------------------------------------------------------------------------------
	// Drain completed SELL maker-first order result, if any.
	//
	// Same lifecycle as BUY drain, but for SELL-side post-only orders.
	// Accept fills that match the current pending order ID or any repriced historical ID.
	// If accepted, convert the broker fill into a SELL lot using actual fill price/base/fee.
	// -------------------------------------------------------------------------------------------------
	if t.pendingSellCh != nil {
		select {
		case res := <-t.pendingSellCh:
			log.Printf("TRACE postonly.drain.recv side=%s order_id=%s filled=%v placed_nil=%v",
				SideSell, res.OrderID, res.Filled, res.Placed == nil)

			accept := false
			if res.Filled && res.Placed != nil {
				log.Printf("TRACE postonly.drain.placed side=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f",
					SideSell, res.OrderID, res.Placed.Price, res.Placed.BaseSize, res.Placed.QuoteSpent, res.Placed.CommissionUSD)

				if t.pendingSell != nil {
					if res.OrderID == t.pendingSell.OrderID {
						accept = true
					} else {
						for _, hid := range t.pendingSell.History {
							if res.OrderID == hid {
								accept = true
								break
							}
						}
					}
				} else {
					accept = true
					log.Printf("[WARN] postonly.fill.without_pending side=%s order_id=%s", SideSell, res.OrderID)
				}
			}

			if accept {
				side := SideSell
				book := t.book(side)
				priceToUse := res.Placed.Price
				baseToUse := res.Placed.BaseSize
				quoteSpent := res.Placed.QuoteSpent
				entryFee := res.Placed.CommissionUSD
				if entryFee <= 0 {
					entryFee = quoteSpent * (t.cfg.FeeRatePct / 100.0)
				}

				matchHistory := false
				if t.pendingSell != nil {
					for _, id := range t.pendingSell.History {
						if id == res.OrderID {
							matchHistory = true
							break
						}
					}
				}

				log.Printf("TRACE postonly.drain.accept side=%s match_current=%v match_history=%v pending_nil=%v",
					SideSell,
					t.pendingSell != nil && res.OrderID == t.pendingSell.OrderID,
					matchHistory,
					t.pendingSell == nil,
				)

				// Refund-service adjustment.
				if t.pendingSell != nil && t.pendingSell.RefundPortionUSD > 0 {
					origBase := baseToUse
					origQuote := quoteSpent
					origFee := entryFee

					refundBase := t.pendingSell.RefundPortionUSD / priceToUse
					if refundBase > baseToUse {
						refundBase = baseToUse
					}

					keptBase := baseToUse - refundBase
					if keptBase < 0 {
						keptBase = 0
					}

					keptQuote := quoteSpent
					keptFee := entryFee
					refundQuote := t.pendingSell.RefundPortionUSD
					refundFee := refundQuote * (t.cfg.FeeRatePct / 100.0)

					if origBase > 0 {
						keptQuote = origQuote * (keptBase / origBase)
						keptFee = origFee * (keptBase / origBase)
						refundQuote = origQuote * (refundBase / origBase)
						refundFee = origFee * (refundBase / origBase)
					}

					t.creditRefundService(SideSell, refundQuote, refundFee)

					baseToUse = keptBase
					quoteSpent = keptQuote
					entryFee = keptFee
				}

				newLot := &Position{
					OpenPrice:       priceToUse,
					Side:            side,
					SizeBase:        baseToUse,
					OpenTime:        now,
					EntryFee:        entryFee,
					OpenNotionalUSD: quoteSpent,
					Reason:          "async postonly filled",
					Take:            0,
					Version:         1,
					LotID:           len(book.Lots),
					EntryOrderID:    res.OrderID,
				}

				if t.pendingSell != nil {
					newLot.Reason = t.pendingSell.Reason
					newLot.Take = t.pendingSell.Take
					newLot.RefundPortionUSD = t.pendingSell.RefundPortionUSD
					newLot.ConfidenceMult = t.pendingSell.ConfidenceMult
					newLot.EntryAIMode = t.pendingSell.EntryAIMode
					newLot.ProfitGateUSD = t.pendingSell.ProfitGateUSD
				}

				if newLot.ConfidenceMult <= 0 {
					newLot.ConfidenceMult = 0.0
				}
				if newLot.EntryAIMode == "" {
					newLot.EntryAIMode = "UNKNOWN"
				}
				if newLot.ProfitGateUSD <= 0 {
					newLot.ProfitGateUSD = t.cfg.ProfitGateUSD
				}

				log.Printf(
					"[KPI] lot.created side=%s mode=%s conf=%.2f gate=%.2f",
					newLot.Side,
					newLot.EntryAIMode,
					newLot.ConfidenceMult,
					newLot.ProfitGateUSD,
				)

				book.Lots = append(book.Lots, newLot)

				t.consolidateDust(book, priceToUse, t.cfg.MinNotional)
				t.didConsolidateStartup = false

				t.SpareSellUSD -= quoteSpent
				if t.SpareSellUSD < 0 {
					t.SpareSellUSD = 0
				}

				if t.pendingSell != nil && t.pendingSell.EquitySell {
					newIdx := len(book.Lots) - 1
					addRunner(book, newIdx)

					r := book.Lots[newIdx]
					r.TrailActive = false
					r.TrailPeak = r.OpenPrice
					r.TrailStop = 0
					t.applyRunnerTargets(r)

					log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f",
						newIdx, side, r.OpenPrice, r.Take)
				}

				t.lastAddSell = wallNow
				t.winHighSell = priceToUse
				t.latchedGateSell = 0

				old := t.lastAddEquitySell
				t.lastAddEquitySell = t.equityUSD
				log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f",
					side, old, t.lastAddEquitySell)

				msg := fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
					side, quoteSpent, newLot.Take, entryFee, newLot.Reason, "async postonly filled")

				if t.cfg.UseDirectSlack {
					postSlack(msg)
				}

				if err := t.saveStateNoLock(); err != nil {
					log.Printf("[WARN] saveState (filled SELL): %v", err)
				}
			} else {
				cancelRequested := t.pendingSell != nil && t.pendingSell.CancelRequested

				if cancelRequested {
					t.pendingRecheckSell = false
					log.Printf(
						"TRACE postonly.cancel.ack side=%s order_id=%s fallback=false reason=signal_changed",
						SideSell,
						res.OrderID,
					)
				} else {
					t.pendingRecheckSell = true
					log.Printf(
						"TRACE postonly.recheck side=%s set=true reason=timeout_or_error order_id=%s",
						SideSell,
						res.OrderID,
					)
				}

			}

			if t.pendingSellCancel != nil {
				t.pendingSellCancel()
			}
			t.pendingSell = nil
			t.pendingSellCtx = nil
			t.pendingSellCancel = nil

			if err := t.saveStateNoLock(); err != nil {
				log.Printf("[WARN] saveState (drain SELL): %v", err)
			}

		default:
			// No async SELL result this tick.
		}
	}

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	_ = t.shouldRefit(len(c)) // intentionally unused here (guard only)

	// TODO: remove TRACE
	lsb := len(t.book(SideBuy).Lots)
	lss := len(t.book(SideSell).Lots)
	log.Printf("TRACE step.start ts=%s livePrice=%.8f candleClose=%.8f lotsBuy=%d lotsSell=%d lastAddBuy=%s lastAddSell=%s winLowBuy=%.8f winHighSell=%.8f latchedGateBuy=%.8f latchedGateSell=%.8f recentLow=%.8f recentHigh=%.8f elapsed_Hours_Buy=%.1f elapsed_Hours_Sell=%.1f",
		now.Format(time.RFC3339), livePrice, c[len(c)-1].Close, lsb, lss,
		t.lastAddBuy.Format(time.RFC3339), t.lastAddSell.Format(time.RFC3339), t.winLowBuy, t.winHighSell, t.latchedGateBuy, t.latchedGateSell, t.RecentLow, t.RecentHigh, time.Since(t.lastAddBuy).Hours(), time.Since(t.lastAddSell).Hours())

	price := livePrice

	// --- Effective min-notional for this tick: prefer cfg.MinNotional, fallback to cfg.OrderMinUSD ---
	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}

	// // One-time dust consolidation right after startup (uses current price snapshot)
	// if !t.didConsolidateStartup {
	// 	// We already hold t.mu here
	// 	t.consolidateDust(t.book(SideBuy),  price, minNotional)
	// 	t.consolidateDust(t.book(SideSell), price, minNotional)

	// 	if err := t.saveStateNoLock(); err != nil {
	// 		log.Printf("[WARN] saveState (startup consolidate): %v", err)
	// 	}
	// 	t.didConsolidateStartup = true
	// 	log.Printf("TRACE consolidate.startup done px=%.8f minNotional=%.2f", price, minNotional)
	// }

	//AI-LOGIC
	d := t.decide(signalHistory)
	d = t.applyLogicGate(d, c)

	updatePreviousAIRaw := func() {
		t.previousAIRaw = d.Raw
	}

	// Cancel stale pending opens if the current decision no longer supports them.
	// Do NOT clear pending here.
	// Do NOT cancel pending context here.
	// Let the async poller observe CANCELED/PARTIALLY_FILLED/FILLED and emit OpenResult.
	if t.pendingBuy != nil && d.Signal != Buy {
		orderID := t.pendingBuy.OrderID
		t.pendingBuy.CancelRequested = true
		t.pendingRecheckBuy = false

		t.mu.Unlock()
		_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, orderID)
		t.mu.Lock()

		_ = t.saveStateNoLock()
		updatePreviousAIRaw()
		t.mu.Unlock()
		return "HOLD pending BUY cancel requested: signal changed", nil
	}

	if t.pendingSell != nil && d.Signal != Sell {
		orderID := t.pendingSell.OrderID
		t.pendingSell.CancelRequested = true
		t.pendingRecheckSell = false

		t.mu.Unlock()
		_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, orderID)
		t.mu.Lock()

		_ = t.saveStateNoLock()
		updatePreviousAIRaw()
		t.mu.Unlock()
		return "HOLD pending SELL cancel requested: signal changed", nil
	}
	// --------------------------------------------------------------------------------------------------------
	// EXIT path: fee-aware per-lot exit management.
	//
	// This block scans existing BUY and SELL lots before any new entry is considered.
	// It computes each lot's current net PnL, applies its correct per-lot profit gate,
	// manages runner/scalp exit behavior, and closes at most ONE lot per step.
	//
	// Important:
	// - Profit gate is per-lot. AI-FLAT entries may have a reduced ProfitGateUSD.
	// - ScalpFixedTP exits require profit gate + AI/logic exit approval.
	// - RunnerTrailing uses runner activation/trailing rules.
	// - nearestTakeBuy/Sell are diagnostic/Gate2 snapshots, not separate exit orders.
	// --------------------------------------------------------------------------------------------------------
	if (lsb > 0) || (lss > 0) {

		nearestTakeBuy := 0.0
		nearestTakeSell := 0.0
		buyNearestIdx, sellNearestIdx := -1, -1
		buyModeLabel, sellModeLabel := "n/a", "n/a"
		buyNet, sellNet := 0.0, 0.0
		feeRatePct := t.cfg.FeeRatePct

		// Human-readable label for the lot's current exit mode.
		// Used only for logs/Gate2 snapshots.
		modeLabel := func(m ExitMode) string {
			switch m {
			case ExitModeRunnerTrailing:
				return "RunnerTrailing"
			case ExitModeScalpFixedTP:
				return "ScalpFixedTP"
			default:
				return "Unknown"
			}
		}

		// Track nearest fee-aware exit/activation price per side.
		//
		// BUY side:
		//   lowest Take is nearest (price rises to reach it).
		//
		// SELL side:
		//   highest Take is nearest (price falls to reach it).
		//
		// Used for diagnostics/Gate2 context only.
		updateNearest := func(book *SideBook, side OrderSide, idx int, lot *Position, net float64, price float64) {
			cand := lot.Take
			if cand <= 0 {
				// lightweight preview if not armed: use lot’s gate (already set by setExitMode)
				gate := lot.TrailActivateGateUSD
				if gate > 0 {
					cand = activationPrice(lot, gate, feeRatePct)
				}
			}
			if cand <= 0 {
				return
			}

			if side == SideBuy {
				if nearestTakeBuy == 0 || cand < nearestTakeBuy {
					nearestTakeBuy = cand
					buyNearestIdx = idx
					buyModeLabel = modeLabel(lot.ExitMode)
					buyNet = net
				}
			} else {
				if nearestTakeSell == 0 || cand > nearestTakeSell {
					nearestTakeSell = cand
					sellNearestIdx = idx
					sellModeLabel = modeLabel(lot.ExitMode)
					sellNet = net
				}
			}
		}

		// Return this lot's effective profit gate.
		//
		// Normal AI-confirmed lots use cfg.ProfitGateUSD.
		// Reduced-confidence AI-FLAT lots may carry a smaller ProfitGateUSD.
		// Older lots fall back to cfg.ProfitGateUSD.
		lotProfitGateUSD := func(lot *Position) float64 {
			gate := lot.ProfitGateUSD
			if gate <= 0 {
				gate = t.cfg.ProfitGateUSD
			}
			return gate
		}

		// Compute fee-aware unrealized net PnL and gate pass/fail.
		//
		// Net PnL includes:
		// - gross move
		// - entry fee
		// - estimated exit fee
		//
		// Exit gate uses the lot's effective ProfitGateUSD
		computeGate := func(lot *Position) (netPnL float64, gate bool) {
			feeRate := t.cfg.FeeRatePct
			estExit := (lot.SizeBase * price) * (feeRate / 100.0)
			lot.EstExitFeeUSD = estExit

			gross := (price - lot.OpenPrice) * lot.SizeBase
			if lot.Side == SideSell {
				gross = (lot.OpenPrice - price) * lot.SizeBase
			}
			net := gross - lot.EntryFee - estExit
			lot.UnrealizedPnLUSD = net
			gateUSD := lotProfitGateUSD(lot)
			return net, net >= gateUSD
		}

		// Classify exit mode and refresh fee-aware Take preview.
		//
		// Runner:
		//   trailing activation + runner trail distance.
		//
		// ScalpFixedTP:
		//   fee-aware Take derived from this lot's ProfitGateUSD.
		//   Supports confidence-scaled exits for AI-FLAT entries.
		setExitMode := func(book *SideBook, idx int, lot *Position) {
			feeRatePct := t.cfg.FeeRatePct

			if isRunner(book, idx) {
				// Runner: trailing; Take = fee-aware activation price for runner USD gate (preview only)
				lot.ExitMode = ExitModeRunnerTrailing
				lot.TrailDistancePct = t.cfg.TrailDistancePctRunner
				lot.TrailActivateGateUSD = t.cfg.TrailActivateUSDRunner
				lot.Take = activationPrice(lot, lot.TrailActivateGateUSD, feeRatePct)
				if lot.TrailPeak == 0 {
					lot.TrailPeak = lot.OpenPrice
				}
				return
			}
			// ScalpFixedTP: Take = fee-aware profit-gate price (preview);
			// when gate passes you’ll arm FixedTPWorking and use this for post-only exits.
			gateUSD := lotProfitGateUSD(lot)
			lot.ExitMode = ExitModeScalpFixedTP
			lot.TrailDistancePct = 0
			lot.TrailActivateGateUSD = gateUSD
			lot.Take = activationPrice(lot, gateUSD, feeRatePct)
		}

		// Scan one side book and close at most one lot.
		//
		// Flow:
		// 1. Refresh exit mode and Take
		// 2. Compute fee-aware net PnL
		// 3. Update nearest snapshot
		// 4. Skip non-profitable lots
		// 5. Require AI/logic exit approval
		// 6. Trigger side-aware exit
		scanSide := func(side OrderSide) (string, bool, error) {

			book := t.book(side)
			lossLimit := -math.Abs(t.cfg.StopLossPnLUSD)
			enableStopLoss := t.cfg.EnableThresholdStopLoss
			buyTh := t.model.BuyThreshold
			sellTh := t.model.SellThreshold
			minBuyDist := t.cfg.MinBuyDistance
			minSellDist := t.cfg.MinSellDistance
			previousAIRaw := t.previousAIRaw

			for i := 0; i < len(book.Lots); {
				lot := book.Lots[i]
				// classify per spec
				setExitMode(book, i, lot)

				// compute gate

				net, pass := computeGate(lot)

				// gather nearest TAKE/mode/net while we're already here (no extra loops later)
				updateNearest(book, side, i, lot, net, price)

				if lot.ExitMode == ExitModeScalpFixedTP {
					gateUSD := lotProfitGateUSD(lot)
					pass = net >= gateUSD
					lot.TrailActivateGateUSD = gateUSD
				}

				// Must be profitable first
				// Profit gate must pass before any exit action.
				// If profit disappears, clear transient trailing/TP state.
				if !pass {
					stopLossExit := false

					stopReason := fmt.Sprintf(
						"threshold_stoploss_check side=%s pUp=%.5f buyTh=%.5f sellTh=%.5f previousAIRaw=%s raw=%s signal=%s pnl=%.2f lossLimit=%.2f",
						lot.Side,
						d.PUp,
						buyTh,
						sellTh,
						previousAIRaw,
						d.Raw,
						d.Signal,
						net,
						lossLimit,
					)

					if enableStopLoss && net <= lossLimit {
						log.Printf("TRACE %s", stopReason)

						switch lot.Side {
						case SideBuy:
							stopLossExit =
								(previousAIRaw == Flat &&
									d.Raw == Buy &&
									d.PUp > buyTh-minBuyDist &&
									d.PUp <= buyTh &&
									d.Signal != Buy) || (d.Signal == Sell && d.Confidence >= 0.60)

						case SideSell:
							stopLossExit =
								(previousAIRaw == Flat &&
									d.Raw == Sell &&
									d.PUp >= sellTh &&
									d.PUp < sellTh+minSellDist &&
									d.Signal != Sell) || (d.Signal == Buy && d.Confidence >= 0.60)
						}
					}

					if stopLossExit {
						exitDecision := appendReason(d.Reason, stopReason)

						msg, err := t.closeLot(
							ctx,
							c,
							livePrice,
							side,
							i,
							"threshold_stop_loss",
							exitDecision,
						)
						if err != nil {
							return "", true, err
						}
						return msg, true, nil
					}

					lot.TrailActive = false
					lot.TrailPeak = 0
					lot.TrailStop = 0
					lot.FixedTPWorking = false
					i++
					continue
				}

				// AI/logic exit approval.
				// Winning trades are held while AI/logic still supports them.
				aiExit := shouldExitByAILogic(lot, d)

				// Profit gate passed.
				// Apply exit-mode-specific behavior.
				switch lot.ExitMode {
				case ExitModeRunnerTrailing:
					// Runner path.
					// Managed by trailing activation/stop behavior.
					if trigger, _ := t.updateRunnerTrail(lot, price); trigger {
						// --- MINIMAL CHANGE: skip trailing-stop close if notional < ORDER_MIN_USD and CONTINUE ---
						closeSide := SideSell
						if lot.Side == SideSell {
							closeSide = SideBuy
						}
						notional := lot.SizeBase * price
						if notional < minNotional {
							log.Printf("[CLOSE-SKIP] lotSide=%s closeSide=%s base=%.8f price=%.2f notional=%.2f < ORDER_MIN_USD %.2f; deferring",
								lot.Side, closeSide, lot.SizeBase, price, notional, minNotional)
							i++
							continue
						}
						msg, err := t.closeLot(ctx, c, livePrice, side, i, "trailing_stop", d.Reason)
						if err != nil {
							return "", true, err
						}
						return msg, true, nil
					}

				case ExitModeScalpFixedTP:
					// Scalp path.
					// ProfitGate passed
					// Does AI/logic still supports this profitable trade.

					// Hold winner; do not exit yet.
					if !aiExit {
						log.Printf(
							"TRACE ai.exit.hold side=%s idx=%d signal=%s net=%.4f",
							lot.Side,
							i,
							d.Signal,
							net,
						)
						i++
						continue
					}

					//-------flow reminder-----------------------------
					// ProfitGate passed
					// AI exit approved
					// arm Take as maker-friendly limit
					// call closeLot()
					// closeLot tries post-only at Take
					// if not filled by timeout, fallback market
					//-------------------------------------------------------

					// Opposite AI/logic appeared while profitable.
					// Allow maker-friendly exit.
					log.Printf(
						"TRACE ai.exit.allow side=%s idx=%d signal=%s net=%.4f",
						lot.Side,
						i,
						d.Signal,
						net,
					)

					// Arm/update maker-friendly exit limit price to be near current mark price.
					offBps := t.cfg.TPMakerOffsetBps
					makerExitPx := price
					if lot.Side == SideBuy && offBps > 0 {
						makerExitPx = price * (1.0 + offBps/10000.0)
					}
					if lot.Side == SideSell && offBps > 0 {
						makerExitPx = price * (1.0 - offBps/10000.0)
					}
					// place/re-post every tick while gate holds (minimal emulation)
					if !lot.FixedTPWorking || (lot.Side == SideBuy && makerExitPx < lot.Take) || (lot.Side == SideSell && makerExitPx > lot.Take) {
						lot.Take = makerExitPx
						lot.FixedTPWorking = true
						log.Printf("TRACE tp.post side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
					} else {
						log.Printf("TRACE tp.repost side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
					}
				}

				// Final trigger check.
				// Scalp exits require:
				// - ProfitGate already passed
				// - AI/logic exit approval
				// - maker exit armed with lot.Take
				// - valid notional
				//
				// We do NOT wait for price to cross Take here.
				// closeLot() uses lot.Take as the post-only limit price.
				trigger := false
				exitReason := ""
				if lot.ExitMode == ExitModeScalpFixedTP {
					exitReason = "take_profit"
				} else if lot.ExitMode == ExitModeRunnerTrailing {
					exitReason = "trailing_stop"
				}

				//--------------------information----------------------------
				// Take = post-only exit price
				// FixedTPWorking = maker exit armed
				// trigger = send maker exit attempt now
				//----------------------------------------------------------

				// FixedTPWorking means the lot has passed ProfitGate + AI/logic exit approval,
				// and lot.Take has been set as the intended post-only exit limit.
				// We call closeLot immediately so the maker order is placed before price crosses it.
				if lot.ExitMode == ExitModeScalpFixedTP && lot.FixedTPWorking && aiExit {
					trigger = true
				}

				// ⬇️ ADD THIS dust guard for Fixed-TP before calling closeLot:
				if trigger && lot.ExitMode == ExitModeScalpFixedTP {
					notional := lot.SizeBase * price
					if notional < minNotional {
						// skip attempting a broker close; leave it armed or quiet it if you prefer
						// (optional) quiet the spam:
						lot.FixedTPWorking = false
						lot.Take = 0
						i++
						continue
					}
				}
				if trigger {
					msg, err := t.closeLot(ctx, c, livePrice, side, i, exitReason, d.Reason)
					if err != nil {
						return "", true, err
					}
					if lot.ExitMode == ExitModeScalpFixedTP {
						log.Printf("TRACE tp.filled side=%s idx=%d mark=%.8f take=%.8f", lot.Side, i, price, lot.Take)
					}
					return msg, true, nil
				}

				// nearest summary (unchanged)
				if lot.Side == SideBuy {
					if lot.Take > 0 && (nearestTakeBuy == 0 || lot.Take < nearestTakeBuy) {
						nearestTakeBuy = lot.Take
						buyNearestIdx, buyModeLabel, buyNet = i, modeLabel(lot.ExitMode), net
					}
				} else {
					if lot.Take > 0 && (nearestTakeSell == 0 || lot.Take > nearestTakeSell) {
						nearestTakeSell = lot.Take
						sellNearestIdx, sellModeLabel, sellNet = i, modeLabel(lot.ExitMode), net
					}
				}
				i++
			}
			return "", false, nil
		}

		// BUY side first, then SELL
		if msg, done, err := scanSide(SideBuy); done || err != nil {
			updatePreviousAIRaw()
			t.mu.Unlock()
			return msg, err
		}
		if msg, done, err := scanSide(SideSell); done || err != nil {
			updatePreviousAIRaw()
			t.mu.Unlock()
			return msg, err
		}
		// single-pass enriched summary (collected in-loop; no extra scans)
		log.Printf("[DEBUG] Nearest Takes | CLOSE-BUY=%.2f (%s, net=%.2f, idx=%d) | CLOSE-SELL=%.2f (%s, net=%.2f, idx=%d) | Buy-Lots=%d Sell-Lots=%d",
			nearestTakeBuy, buyModeLabel, buyNet, buyNearestIdx,
			nearestTakeSell, sellModeLabel, sellNet, sellNearestIdx, lsb, lss)

		// Persist snapshots for Gate2 use (under lock; we are holding t.mu in step())
		t.nearestTakeBuy = nearestTakeBuy
		t.nearestNetBuy = buyNet
		t.nearestIdxBuy = buyNearestIdx

		t.nearestTakeSell = nearestTakeSell
		t.nearestNetSell = sellNet
		t.nearestIdxSell = sellNearestIdx

	}

	feeMult := 1.0 + (t.cfg.FeeRatePct / 100.0)
	// Sum reserved base for long lots (BUY side)
	var reservedLongBase float64
	if bb := t.book(SideBuy); bb != nil {
		for _, lot := range bb.Lots {
			reservedLongBase += lot.SizeBase
		}
	}
	// --- NEW (Phase 4): include pending SELL base reserve if required ---
	if t.pendingSell != nil && t.cfg.RequireBaseForShort {
		reservedLongBase += t.pendingSell.BaseAtLimit
	}
	// Compute reserved quote for shorts in Lots exit
	var reservedShortQuoteWithFee float64
	if sb := t.book(SideSell); sb != nil {
		// Sum reserved quote for short lots (SELL side)
		for _, lot := range sb.Lots {
			q := lot.SizeBase * price
			reservedShortQuoteWithFee += q * feeMult
		}
	}
	// --- NEW (Phase 4): include pending BUY quote reserve ---
	if t.pendingBuy != nil {
		reservedShortQuoteWithFee += t.pendingBuy.Quote * feeMult
	}

	// --------------------------------------------------------------------------------------------------------
	//---ADD path continues-----
	// --------------------------------------------------------------------------------------------------------

	totalLots := lsb + lss

	log.Printf(
		"[DEBUG] Total Lots=%d Raw=%s Decision=%s price=%.8f Reason=%s buyThresh=%.3f sellThresh=%.3f modelBuyThresh=%.3f modelSellThresh=%.3f LongOnly=%v ver-86",
		totalLots,
		d.Raw,
		d.Signal,
		price,
		d.Reason,
		t.cfg.BuyThreshold,
		t.cfg.SellThreshold,
		t.model.BuyThreshold,
		t.model.SellThreshold,
		t.cfg.LongOnly,
	)

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	// Determine the side and its book
	side := d.SignalToSide()
	book := t.book(side)

	// Prevent duplicate opens while pending on this side (exits already ran) ---
	// Extra belt-and-suspenders: if a pending exists and we haven't hit its Deadline, keep waiting.
	if side == SideBuy && t.pendingBuy != nil && time.Now().Before(t.pendingBuy.Deadline) {
		updatePreviousAIRaw()
		t.mu.Unlock()
		return "OPEN-PENDING side=BUY", nil
	}
	if side == SideSell && t.pendingSell != nil && time.Now().Before(t.pendingSell.Deadline) {
		updatePreviousAIRaw()
		t.mu.Unlock()
		return "OPEN-PENDING side=SELL", nil
	}

	//Required spare inventory
	var spare float64
	var symB string
	var availBase, baseStep float64
	var errB error
	var symQ string
	var availQuote, quoteStep float64
	var errQ error

	// If BUY, required spare quote inventory
	if side == SideBuy {
		// Reserve quote needed to close shorts (includes fees).
		// Release lock for broker I/O to avoid stalls.
		t.mu.Unlock()
		symQ, availQuote, quoteStep, errQ = t.broker.GetAvailableQuote(ctx, t.cfg.ProductID)
		t.mu.Lock()
		if errQ != nil || strings.TrimSpace(symQ) == "" {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: GetAvailableQuote failed: error %v, symQ %s", errQ, symQ)
		}
		if quoteStep <= 0 {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: missing/invalid QUOTE step for %s (step=%.8f)", t.cfg.ProductID, quoteStep)
		}
		spare = availQuote - reservedShortQuoteWithFee
	}

	// If SELL, required spare base inventory (spot safe)
	if side == SideSell {
		// Ask broker for base balance/step (release lock for I/O).
		t.mu.Unlock()
		symB, availBase, baseStep, errB = t.broker.GetAvailableBase(ctx, t.cfg.ProductID)
		t.mu.Lock()
		if errB != nil || strings.TrimSpace(symB) == "" {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: GetAvailableBase failed: error %v, symB %s", errB, symB)
		}
		if baseStep <= 0 {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: missing/invalid BASE step for %s (step=%.8f)", t.cfg.ProductID, baseStep)
		}
		// Only enforce spare-base gating if we actually require base to short.
		if t.cfg.RequireBaseForShort {
			spare = availBase - reservedLongBase
		} else {
			// When shorting without spot requirement, "spare" for equity-trigger SELL
			// can be computed as available base (for step snapping) but we don't gate on it.
			spare = availBase - reservedLongBase
			if spare < 0 {
				spare = 0
			}
		}
	}

	// --- NEW (minimal): equity strategy trigger detection (SELL runner add of entire spare base)
	equityTriggerSell := false
	var equitySpareBase float64
	if t.lastAddEquitySell > 0 && t.equityUSD >= t.lastAddEquitySell*t.cfg.SellEquityTriggerMult && d.Signal == Sell {
		// Only proceed if not long-only; respect existing guard
		if t.cfg.LongOnly {
			updatePreviousAIRaw()
			t.mu.Unlock()
			return "FLAT (long-only) [equity-scalp]", nil
		}
		if spare < 0 {
			spare = 0
		}
		// Floor to step
		if spare > 0 {
			equitySpareBase = math.Floor(spare/baseStep) * baseStep
			if equitySpareBase > 0 {
				equityTriggerSell = true
			}
		}
	}

	// --- NEW (minimal): BUY equity-trigger flag ---(Buy runner of entire spare quote on dip)
	equityTriggerBuy := false
	var equitySpareQuote float64
	if t.lastAddEquityBuy > 0 && t.equityUSD <= t.lastAddEquityBuy*t.cfg.BuyEquityTriggerMult && d.Signal == Buy {
		if spare < 0 {
			spare = 0
		}
		if spare > 0 {
			equitySpareQuote = math.Floor(spare/quoteStep) * quoteStep
			if equitySpareQuote > 0 {
				equityTriggerBuy = true
			}
		}
	}

	log.Printf("[DEBUG] EQUITY Trading: equityUSD=%.2f lastAddEquitySell=%.2f sellEquityMultiplier=%.6f(triggerAt:>=%.2f) lastAddEquityBuy=%.2f buyEquityMultiplier=%.6f(triggerAt:>=%.2f) ", t.equityUSD, t.lastAddEquitySell, t.equityUSD/t.lastAddEquitySell, t.cfg.SellEquityTriggerMult, t.lastAddEquityBuy, t.equityUSD/t.lastAddEquityBuy, t.cfg.BuyEquityTriggerMult)

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		updatePreviousAIRaw()
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}

	//this block might be going out =========
	if d.Signal == Flat && !equityTriggerSell && !equityTriggerBuy {
		log.Printf(
			"[TRADE_GATE] lastAddBuy=%s lastAddSell=%s "+
				"winLowBuy=%.2f winHighSell=%.2f "+
				"latchedBuy=%.2f latchedSell=%.2f "+
				"nearestBuy{take=%.2f net=%.2f idx=%d} "+
				"nearestSell{take=%.2f net=%.2f idx=%d} ",
			t.lastAddBuy.Format(time.RFC3339),
			t.lastAddSell.Format(time.RFC3339),
			t.winLowBuy,
			t.winHighSell,
			t.latchedGateBuy,
			t.latchedGateSell,
			t.nearestTakeBuy,
			t.nearestNetBuy,
			t.nearestIdxBuy,
			t.nearestTakeSell,
			t.nearestNetSell,
			t.nearestIdxSell,
		)
		updatePreviousAIRaw()
		t.mu.Unlock()
		return "FLAT", nil
	}

	// GATE1 Respect lot cap (both sides)
	if (lsb+lss) >= t.cfg.MaxConcurrentLots && !((equityTriggerBuy && d.Signal == Buy) || (equityTriggerSell && d.Signal == Sell)) {
		if !t.didConsolidateStartup {
			// run runner-specific consolidation first (both sides)
			t.consolidateRunners(t.book(SideBuy), price)
			t.consolidateRunners(t.book(SideSell), price)

			// then the generic dust consolidation (unchanged)
			t.consolidateDust(t.book(SideBuy), price, minNotional)
			t.consolidateDust(t.book(SideSell), price, minNotional)

			if err := t.saveStateNoLock(); err != nil {
				log.Printf("[WARN] saveState (startup consolidate): %v", err)
			}
			t.didConsolidateStartup = true
			log.Printf("TRACE consolidate.startup done px=%.8f minNotional=%.2f", price, minNotional)
		}
		updatePreviousAIRaw()
		t.mu.Unlock()
		log.Printf("[DEBUG] GATE1 lot cap reached (%d); HOLD", t.cfg.MaxConcurrentLots)
		return "HOLD", nil
	}

	// Determine if we are opening equity triggered trade or attempting a pyramid add (side-aware).
	isAdd := len(book.Lots) > 0 && t.cfg.AllowPyramiding && (d.Signal == Buy || d.Signal == Sell)
	// --- NEW: skip pyramiding gates for equity-triggered paths (minimal) ---
	skipPyramidGates := equityTriggerSell || equityTriggerBuy

	latchResetHours := t.cfg.PyramidLatchResetHours

	//----------------------------------------------------------------------------------------
	// Rebase stale opposite-side latch before current-side pyramid gating.
	// This must live outside isAdd, because a stale SELL latch may need rebasing
	// on a BUY signal even when BUY is not yet a pyramid add, and vice versa.
	//----------------------------------------------------------------------------------------

	if latchResetHours > 0 && d.Signal == Buy && t.latchedGateSell > 0 {
		sellLatchAgeHr := time.Since(t.lastAddSell).Hours()
		if sellLatchAgeHr >= latchResetHours && t.RecentHigh > 0 && t.RecentHigh < t.latchedGateSell {
			oldLatch := t.latchedGateSell
			oldWin := t.winHighSell
			t.latchedGateSell = t.RecentHigh
			t.winHighSell = t.RecentHigh
			log.Printf("[DEBUG] LATCH REBASE SELL: ageHr=%.2f logic=%s old_latched=%.2f old_winHigh=%.2f new_latched=%.2f new_winHigh=%.2f price=%.2f",
				sellLatchAgeHr, d.Signal, oldLatch, oldWin, t.latchedGateSell, t.winHighSell, price)
		}
	}

	if latchResetHours > 0 && d.Signal == Sell && t.latchedGateBuy > 0 {
		buyLatchAgeHr := time.Since(t.lastAddBuy).Hours()
		if buyLatchAgeHr >= latchResetHours && t.RecentLow > 0 && t.RecentLow > t.latchedGateBuy {
			oldLatch := t.latchedGateBuy
			oldWin := t.winLowBuy
			t.latchedGateBuy = t.RecentLow
			t.winLowBuy = t.RecentLow
			log.Printf("[DEBUG] LATCH REBASE BUY: ageHr=%.2f logic=%s old_latched=%.2f old_winLow=%.2f new_latched=%.2f new_winLow=%.2f price=%.2f",
				buyLatchAgeHr, d.Signal, oldLatch, oldWin, t.latchedGateBuy, t.winLowBuy, price)
		}
	}

	// --- NEW: variables to capture gate audit fields for the reason string (side-biased; no winLow) ---
	var (
		reasonGatePrice float64
		reasonLatched   float64
		reasonEffPct    float64
		reasonBasePct   float64
		reasonElapsedHr float64
		// reasonElapsedHr = elapsedMin / 60.0
		reasonTFloorHr float64
	)
	// GATE2 Gating for pyramiding adds — spacing + adverse move (with optional time-decay), side-aware.
	if isAdd && !skipPyramidGates {

		// Choose side-aware anchor set
		var lastAddSide time.Time
		if side == SideBuy {
			lastAddSide = t.lastAddBuy
		} else {
			lastAddSide = t.lastAddSell
		}

		// 1) Spacing
		psb := t.cfg.PyramidMinSecondsBetween
		// TODO: remove TRACE
		log.Printf("TRACE pyramid.spacing since_last=%.1fs need>=%ds", time.Since(lastAddSide).Seconds(), psb)
		if time.Since(lastAddSide) < time.Duration(psb)*time.Second {
			updatePreviousAIRaw()
			t.mu.Unlock()
			hrs := time.Since(lastAddSide).Hours()
			log.Printf("[DEBUG] GATE2 pyramid: blocked by spacing; since_last=%vHours need>=%ds", fmt.Sprintf("%.1f", hrs), psb)
			return "HOLD", nil
		}

		// 2) Adverse move gate with optional time-based exponential decay.
		basePct := t.cfg.PyramidMinAdversePct
		effPct := basePct
		lambda := t.cfg.PyramidDecayLambda
		floor := t.cfg.PyramidDecayMinPct
		elapsedMin := 0.0

		// Confidence here represents reversal tenderness, not trade certainty.
		// Higher confidence means the setup is more tender/sensitive, so the bot
		// requires a smaller adverse move before allowing a pyramid add.
		//
		// gateMult scales both:
		//   1) effPct: the adverse % gate
		//   2) tFloorMin: the time spent in baseline-effPct regime before recording
		//      winLow/winHigh for future latch.
		//
		// Low confidence  -> larger gateMult -> deeper adverse gate, longer wait.
		// High confidence -> smaller gateMult -> shallower adverse gate, shorter wait.
		//
		// Design:
		// Phase 1: 0 → tFloorMin
		//   Use only the baseline/decayed effPct gate.
		//   If price crosses this gate, the add is valid immediately.
		//
		// Phase 2: tFloorMin → 2*tFloorMin
		//   If baseline gate was not crossed earlier, start observing winLow/winHigh
		//   while still using the baseline/decayed effPct gate.
		//
		// Phase 3: >= 2*tFloorMin
		//   Latch the observed extreme and use the latched gate going forward.
		gateMult := confidenceEffPctMultiplier(d.Confidence)
		if lambda > 0 {
			if !lastAddSide.IsZero() {
				elapsedMin = time.Since(lastAddSide).Minutes()
			} else {
				elapsedMin = 0.0
			}
			decayed := basePct * math.Exp(-lambda*elapsedMin)
			if decayed < floor {
				decayed = floor
			}
			effPct = decayed * gateMult

			log.Printf(
				"TRACE pyramid.conf_gate side=%s confidence=%.4f gateMult=%.4f decayedPct=%.4f effPct=%.4f",
				side, d.Confidence, gateMult, decayed, effPct,
			)
		}

		// Capture for reason string
		reasonBasePct = basePct
		reasonEffPct = effPct
		reasonElapsedHr = elapsedMin / 60.0

		// Time (in minutes) to hit the floor once (t_floor_min)
		baseTFloorMin := 0.0
		if lambda > 0 && basePct > floor {
			baseTFloorMin = math.Log(basePct/floor) / lambda
		}
		tFloorMin := baseTFloorMin * gateMult
		reasonTFloorHr = tFloorMin / 60.0
		// TODO: remove TRACE
		log.Printf("TRACE pyramid.adverse side=%s lastAddAgoMin=%.2f basePct=%.4f effPct=%.4f lambda=%.5f floor=%.4f tFloorMin=%.2f",
			side, elapsedMin, basePct, effPct, lambda, floor, tFloorMin)

		// Use side-aware latest entry for adverse gate anchoring
		last := t.latestEntryBySide(side)

		// Convert stop-loss USD risk into a price distance and use 20% as a
		// latch buffer. Keeps BUY latches below recent entries and SELL latches
		// above recent entries, preventing immediate re-adds near the last fill.
		latchBufferPrice := 0.0
		if t.cfg.RiskPerTradeUSD > 0 && price > 0 {
			fullDistance := math.Abs(t.cfg.StopLossPnLUSD) * price / t.cfg.RiskPerTradeUSD
			latchBufferPrice = fullDistance / 8.0
		}

		if last > 0 {
			if side == SideBuy {
				// baseline gate: last * (1.0 - effPct)
				gatePrice := last * (1.0 - effPct/100.0)

				// BUY adverse tracker
				if elapsedMin >= tFloorMin && t.latchedGateBuy == 0 {
					if t.winLowBuy == 0 || price < t.winLowBuy {
						t.winLowBuy = price
					}

					// Soft regime before hard latch.
					// BUY triggers when price <= gatePrice, so max(recentLow, baseline)
					// softens the gate.
					if elapsedMin < 2.0*tFloorMin && t.RecentLow > 0 {
						oldGate := gatePrice
						gatePrice = math.Max(gatePrice, t.RecentLow)

						if gatePrice != oldGate {
							log.Printf("[DEBUG] SOFT GATE BUY: elapsedMin=%.1f tFloorMin=%.2f old_gate=%.2f recentLow=%.2f soft_gate=%.2f winLow=%.2f price=%.2f",
								elapsedMin, tFloorMin, oldGate, t.RecentLow, gatePrice, t.winLowBuy, price)
						}
					}

				} else if elapsedMin < tFloorMin {
					t.winLowBuy = 0
				}

				// hard latch at 2*tFloorMin
				if t.latchedGateBuy == 0 && elapsedMin >= 2.0*tFloorMin && t.winLowBuy > 0 {
					t.latchedGateBuy = t.winLowBuy
					log.Printf("[DEBUG] LATCH SET BUY: latchedGate=%.2f winLow=%.2f elapsedMin=%.1f tFloorMin=%.2f",
						t.latchedGateBuy, t.winLowBuy, elapsedMin, tFloorMin)
				}

				// latched replaces baseline after hard latch
				if t.latchedGateBuy > 0 {
					oldLatch := t.latchedGateBuy
					t.latchedGateBuy = math.Min(last-latchBufferPrice, t.latchedGateBuy)
					if t.latchedGateBuy != oldLatch {
						log.Printf("TRACE pyramid.latch_clamp.buy old=%.8f last=%.8f new=%.8f", oldLatch, last, t.latchedGateBuy)
					}
					gatePrice = t.latchedGateBuy
				}

				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateBuy

				// --- breadcrumb when baseline condition met ---
				if price <= gatePrice {
					log.Printf("[DEBUG] pyramid: BUY baseline met price=%.2f gatePrice=%.2f last=%.2f eff_pct=%.3f elapsedMin=%.1f",
						price, gatePrice, last, effPct, elapsedMin)
				}

				if !(price <= gatePrice) {
					updatePreviousAIRaw()
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (BUY); price=%.2f last_gate<=%.2f win_low=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f latch_target_Hr>=%.2fHr confidence=%.2f",
						price, gatePrice, t.winLowBuy, effPct, basePct, reasonElapsedHr, 2.0*reasonTFloorHr, d.Confidence)
					log.Printf("TRACE pyramid.block.buy price=%.8f last=%.8f gate=%.8f latched=%.8f", price, last, gatePrice, t.latchedGateBuy)
					return "HOLD", nil
				}
			} else { // SELL
				// baseline gate: last * (1.0 + effPct/100.0)
				gatePrice := last * (1.0 + effPct/100.0)

				// SELL adverse tracker
				if elapsedMin >= tFloorMin && t.latchedGateSell == 0 {
					if t.winHighSell == 0 || price > t.winHighSell {
						t.winHighSell = price
					}

					// Soft regime before hard latch.
					// SELL triggers when price >= gatePrice, so min(recentHigh, baseline)
					// softens the gate.
					if elapsedMin < 2.0*tFloorMin && t.RecentHigh > 0 {
						oldGate := gatePrice
						gatePrice = math.Min(gatePrice, t.RecentHigh)

						if gatePrice != oldGate {
							log.Printf("[DEBUG] SOFT GATE SELL: elapsedMin=%.1f tFloorMin=%.2f old_gate=%.2f recentHigh=%.2f soft_gate=%.2f winHigh=%.2f price=%.2f",
								elapsedMin, tFloorMin, oldGate, t.RecentHigh, gatePrice, t.winHighSell, price)
						}
					}

				} else if elapsedMin < tFloorMin {
					t.winHighSell = 0
				}

				// hard latch at 2*tFloorMin
				if t.latchedGateSell == 0 && elapsedMin >= 2.0*tFloorMin && t.winHighSell > 0 {
					t.latchedGateSell = t.winHighSell
					log.Printf("[DEBUG] LATCH SET SELL: latchedGate=%.2f winHigh=%.2f elapsedMin=%.1f tFloorMin=%.2f",
						t.latchedGateSell, t.winHighSell, elapsedMin, tFloorMin)
				}

				// latched replaces baseline
				if t.latchedGateSell > 0 {
					oldLatch := t.latchedGateSell
					t.latchedGateSell = math.Max(last+latchBufferPrice, t.latchedGateSell)
					if t.latchedGateSell != oldLatch {
						log.Printf("TRACE pyramid.latch_clamp.sell old=%.8f last=%.8f new=%.8f", oldLatch, last, t.latchedGateSell)
					}
					gatePrice = t.latchedGateSell
				}

				// copy for legacy reason/log fields
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateSell

				// --- breadcrumb when baseline condition met ---
				if price >= gatePrice {
					log.Printf("[DEBUG] pyramid: SELL baseline met price=%.2f gatePrice=%.2f last=%.2f eff_pct=%.3f elapsedMin=%.1f",
						price, gatePrice, last, effPct, elapsedMin)
				}

				if !(price >= gatePrice) {
					updatePreviousAIRaw()
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (SELL); price=%.2f last_gate>=%.2f win_high=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f, latch_target_Hr>=%.2fHr confidence=%.2f",
						price, gatePrice, t.winHighSell, effPct, basePct, reasonElapsedHr, 2.0*reasonTFloorHr, d.Confidence)
					log.Printf("TRACE pyramid.block.sell price=%.8f last=%.8f gate=%.8f latched=%.8f", price, last, gatePrice, t.latchedGateSell)
					return "HOLD", nil
				}
			}
		}
	}

	// --- Fixed-USD risk sizing & ramping (no equity dependency) ---
	// Base dollar size for the first lot
	baseUSD := t.cfg.RiskPerTradeUSD
	if baseUSD <= 0 {
		// safety fallback: at least minNotional
		baseUSD = minNotional
	}

	// Start with baseUSD as our target notional
	quote := baseUSD

	// Optional: volatility adjust as a multiplier on USD (not on equity)
	if t.cfg.VolRiskAdjust {
		f := volRiskFactor(c)
		if f <= 0 {
			f = 1.0
		}
		quote = quote * f
		SetVolRiskFactorMetric(f)
	}

	// --- Fixed-USD ramping: scale around baseUSD, independent of equityUSD ---
	if t.cfg.RampEnable && !(equityTriggerSell || equityTriggerBuy) {
		// number of existing non-dust lots on THIS SIDE
		k := rampCount(book, price, minNotional)
		// exclude all runner(s) on this side from k
		if rc := runnerCount(book); rc > 0 && k >= rc {
			k = k - rc
		}

		switch strings.ToLower(strings.TrimSpace(t.cfg.RampMode)) {
		case "exp":
			// Interpret RampStartPct / RampMaxPct as percent multipliers of baseUSD.
			// Example:
			//   RAMP_START_PCT = 100  => 1.0x baseUSD
			//   RAMP_GROWTH    = 1.25 => grow by 25% per add
			//   RAMP_MAX_PCT   = 200  => cap at 2.0x baseUSD
			start := t.cfg.RampStartPct
			g := t.cfg.RampGrowth
			if start <= 0 {
				start = 100.0 // 1.0x
			}
			if g <= 0 {
				g = 1.0
			}
			f := start
			for i := 0; i < k; i++ {
				f *= g
			}
			if max := t.cfg.RampMaxPct; max > 0 && f > max {
				f = max
			}
			if f <= 0 {
				f = 100.0
			}
			quote = baseUSD * (f / 100.0)

		default: // linear
			// Interpret RampStartPct / RampStepPct / RampMaxPct as percent multipliers of baseUSD.
			// Example:
			//   RAMP_START_PCT = 100  => 1.0x baseUSD (first lot)
			//   RAMP_STEP_PCT  = 25   => +0.25x per existing lot
			//   RAMP_MAX_PCT   = 200  => cap at 2.0x baseUSD
			start := t.cfg.RampStartPct
			step := t.cfg.RampStepPct
			if start <= 0 {
				start = 100.0 // 1.0x
			}
			f := start + float64(k)*step
			if max := t.cfg.RampMaxPct; max > 0 && f > max {
				f = max
			}
			if f <= 0 {
				f = 100.0
			}
			quote = baseUSD * (f / 100.0)
		}
	}

	confMult := d.Confidence
	if confMult <= 0 {
		log.Printf(
			"[TRADE_GATE] confidence=%.2f lastAddBuy=%s lastAddSell=%s "+
				"winLowBuy=%.2f winHighSell=%.2f "+
				"latchedBuy=%.2f latchedSell=%.2f "+
				"nearestBuy{take=%.2f net=%.2f idx=%d} "+
				"nearestSell{take=%.2f net=%.2f idx=%d} ",
			confMult,
			t.lastAddBuy.Format(time.RFC3339),
			t.lastAddSell.Format(time.RFC3339),
			t.winLowBuy,
			t.winHighSell,
			t.latchedGateBuy,
			t.latchedGateSell,
			t.nearestTakeBuy,
			t.nearestNetBuy,
			t.nearestIdxBuy,
			t.nearestTakeSell,
			t.nearestNetSell,
			t.nearestIdxSell,
		)
		updatePreviousAIRaw()
		t.mu.Unlock()
		return "HOLD", nil // or continue / hold
	}

	entryAIMode := "AI_MATCH"
	if d.Raw == Flat {
		entryAIMode = "AI_FLAT"
	}

	entryProfitGateUSD := t.cfg.ProfitGateUSD * confMult
	if entryProfitGateUSD < 0.30 {
		entryProfitGateUSD = 0.30
	}

	//Applying confidence multiplier to scalp, that of equity comes later
	if !(equityTriggerSell || equityTriggerBuy) {
		oldQuote := quote
		quote *= confMult
		log.Printf(
			"TRACE sizing.confidence side=%s pUp=%.5f mult=%.2f quote_before=%.2f quote_after=%.2f",
			side, d.PUp, confMult, oldQuote, quote,
		)
	}

	// Ensure we respect the exchange minimum notional
	if quote < minNotional {
		quote = minNotional
	}
	base := quote / price

	// Staged sizing for EQUITY triggers (SELL in BASE, BUY in QUOTE) ---
	// Override sizing for normal Sell using stage function of spare base as the order size (SELL only) ---
	if equityTriggerSell && side == SideSell && equitySpareBase > 0 {
		stagesSell := equityStagesSell()
		startStage := clampStage(t.equityStageSell, len(stagesSell))
		chosen := -1
		var targetBase float64
		for s := startStage; s < len(stagesSell); s++ {
			tb := equitySpareBase * stagesSell[s]
			oldBase := tb
			tb *= confMult
			log.Printf(
				"TRACE sizing.equity.confidence side=%s pUp=%.5f mult=%.2f quote_before=%.2f quote_after=%.2f",
				side, d.PUp, confMult, oldBase, tb,
			)
			tb = snapToStep(tb, baseStep)
			if tb <= 0 || tb > equitySpareBase {
				continue
			}
			if tb*price >= minNotional {
				targetBase = tb
				chosen = s
				break
			}
		}
		if chosen >= 0 {
			base = targetBase
			quote = base * price
			t.equityStageSell = clampStage(chosen+1, len(stagesSell))
		} else {
			equityTriggerSell = false
		}
	}
	// --- NEW: override sizing for BUY equity dip to use entire spare quote ---
	if equityTriggerBuy && side == SideBuy && equitySpareQuote > 0 {
		stagesBuy := equityStagesBuy()
		startStage := clampStage(t.equityStageBuy, len(stagesBuy))
		chosen := -1
		var targetQuote float64
		for s := startStage; s < len(stagesBuy); s++ {
			tq := equitySpareQuote * stagesBuy[s]
			oldQuote := tq
			tq *= confMult
			log.Printf(
				"TRACE sizing.equity.confidence side=%s pUp=%.5f mult=%.2f quote_before=%.2f quote_after=%.2f",
				side, d.PUp, confMult, oldQuote, tq,
			)
			tq = snapToStep(tq, quoteStep)
			if tq <= 0 || tq > equitySpareQuote {
				continue
			}
			if tq >= minNotional {
				targetQuote = tq
				chosen = s
				break
			}
		}
		if chosen >= 0 {
			quote = targetQuote
			base = quote / price
			t.equityStageBuy = clampStage(chosen+1, len(stagesBuy))
		} else {
			equityTriggerBuy = false
		}
	}

	// TODO: remove TRACE
	log.Printf("TRACE sizing.pre side=%s eq=%.2f quote=%.2f price=%.8f base=%.8f", side, t.equityUSD, quote, price, base)

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// -----------------------------------------------------------------------------------------------
	// --- Spare and Reservation Inventory ---
	// -----------------------------------------------------------------------------------------------
	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// TODO: remove TRACE
		log.Printf("TRACE buy.gate.pre availQuote=%.2f reservedShort=%.2f needQuoteRaw=%.2f quoteStep=%.8f",
			availQuote, reservedShortQuoteWithFee, quote, quoteStep)

		// Floor the needed quote to step.
		neededQuote := quote
		if quoteStep > 0 {
			n := math.Floor(neededQuote/quoteStep) * quoteStep
			if n > 0 {
				neededQuote = n
			}
		}

		if spare < 0 {
			spare = 0
		}

		// Fast path: we have enough spare to fund the snapped neededQuote
		if spare+spareEps >= neededQuote {
			// Enforce exchange minimum notional after snapping, then snap UP to step to keep >= min; re-check spare.
			if neededQuote < minNotional {
				neededQuote = minNotional
				if quoteStep > 0 {
					steps := math.Ceil(neededQuote / quoteStep)
					neededQuote = steps * quoteStep
				}
				// after bump to minNotional we must still have spare
				if spare+spareEps < neededQuote {
					log.Printf("[WARN] FUNDS_EXHAUSTED BUY need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
						neededQuote, spare, availQuote, reservedShortQuoteWithFee, quoteStep)
					log.Printf("[DEBUG] GATE BUY: need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
						neededQuote, spare, availQuote, reservedShortQuoteWithFee, quoteStep)
					log.Printf("TRACE buy.gate.block minNotional need=%.2f spare=%.2f", neededQuote, spare)

					short := neededQuote - spare
					if short > 0 {
						// remember that a BUY was blocked by this amount
						t.refundBuyUSD = short
					}
					updatePreviousAIRaw()
					t.mu.Unlock()
					return "HOLD", nil
				}
			}

			// Use the final neededQuote; recompute base.
			quote = neededQuote
			base = quote / price

			log.Printf("TRACE buy.gate.post needQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
		} else {
			// Slow path: we don't have enough to fund neededQuote → try to degrade to available spare
			log.Printf("[WARN] FUNDS_SHORT BUY need=%.2f quote, spare=%.2f → attempting degrade-to-spare",
				neededQuote, spare)

			useQuote := spare

			// snap spare DOWN to quote step
			if quoteStep > 0 {
				u := math.Floor(useQuote/quoteStep) * quoteStep
				if u > 0 {
					useQuote = u
				}
			}

			// must still satisfy minNotional after snapping
			if useQuote < minNotional {
				log.Printf("[WARN] FUNDS_EXHAUSTED BUY even after degrade: useQuote=%.2f < minNotional=%.2f (avail=%.2f, reserved_shorts=%.6f)",
					useQuote, minNotional, availQuote, reservedShortQuoteWithFee)
				log.Printf("[DEBUG] GATE BUY: degrade failed; HOLD")

				short := neededQuote - spare
				if short > 0 {
					// only now (true failure) remember that a BUY was blocked
					t.refundBuyUSD = short
				}
				updatePreviousAIRaw()
				t.mu.Unlock()
				return "HOLD", nil
			}

			// ok, we can place a smaller order using the spare
			quote = useQuote
			base = quote / price

			log.Printf("TRACE buy.gate.post.degraded useQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
		}
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && t.cfg.RequireBaseForShort {
		// TODO: remove TRACE
		log.Printf("TRACE sell.gate.pre availBase=%.8f reservedLong=%.8f needBaseRaw=%.8f baseStep=%.8f",
			availBase, reservedLongBase, base, baseStep)

		// Floor the *needed* base to baseStep (if known)
		neededBase := base
		if baseStep > 0 {
			n := math.Floor(neededBase/baseStep) * baseStep
			if n > 0 {
				neededBase = n
			}
		}

		if spare < 0 {
			spare = 0
		}

		// Fast path: we have enough spare base to fund neededBase
		if spare+spareEps >= neededBase {
			// Use the floored base for the order by updating quote
			quote = neededBase * price
			base = neededBase

			// Ensure SELL meets exchange min funds and step rules (and re-check spare symmetry)
			if quote < minNotional {
				quote = minNotional
				base = quote / price
				if baseStep > 0 {
					b := math.Floor(base/baseStep) * baseStep
					if b > 0 {
						base = b
						quote = base * price
					}
				}
				// >>> Symmetry: re-check spare after min-notional snap <<<
				if spare+spareEps < base {
					// --- breadcrumb ---
					log.Printf("[WARN] FUNDS_EXHAUSTED SELL need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, baseStep=%.8f)",
						base, spare, availBase, reservedLongBase, baseStep)
					log.Printf("[DEBUG] GATE SELL: need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, baseStep=%.8f)",
						base, spare, availBase, reservedLongBase, baseStep)
					log.Printf("TRACE sell.gate.block minNotional need=%.8f spare=%.8f", base, spare)

					// convert the short to USD at current price so we can reuse later on BUY
					shortBase := base - spare
					shortUSD := shortBase * price
					if shortUSD > 0 {
						t.refundSellUSD = shortUSD
					}
					updatePreviousAIRaw()
					t.mu.Unlock()
					return "HOLD", nil
				}
			}

			log.Printf("TRACE sell.gate.post needBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
		} else {
			// Slow path: not enough spare for neededBase → try degrade-to-spare
			log.Printf("[WARN] FUNDS_SHORT SELL need=%.8f base, spare=%.8f → attempting degrade-to-spare",
				neededBase, spare)

			useBase := spare

			// snap spare DOWN to baseStep
			if baseStep > 0 {
				b := math.Floor(useBase/baseStep) * baseStep
				if b > 0 {
					useBase = b
				} else {
					useBase = 0
				}
			}

			// must still satisfy minNotional after snapping
			if useBase <= 0 || useBase*price < minNotional {
				log.Printf("[WARN] FUNDS_EXHAUSTED SELL even after degrade: useBase=%.8f (quote=%.2f) < minNotional=%.2f (avail=%.8f, reserved_longs=%.8f)",
					useBase, useBase*price, minNotional, availBase, reservedLongBase)

				// convert the shortfall to USD only on true failure
				shortBase := neededBase - spare
				shortUSD := shortBase * price
				if shortUSD > 0 {
					t.refundSellUSD = shortUSD
				}
				updatePreviousAIRaw()
				t.mu.Unlock()
				return "HOLD", nil
			}

			// ok, we can place a smaller order using the spare
			base = useBase
			quote = base * price

			log.Printf("TRACE sell.gate.post.degraded useBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
		}
	}

	var take float64
	if t.cfg.ScalpTPDecayEnable && !((equityTriggerBuy && side == SideBuy) || (equityTriggerSell && side == SideSell)) {
		// number of existing non-dust lots on THIS SIDE
		k := rampCount(book, price, minNotional)

		if rc := runnerCount(book); rc > 0 && k >= rc {
			k = len(book.Lots) - rc
		}
		baseTP := t.cfg.TakeProfitPct
		tpPct := baseTP

		switch strings.ToLower(strings.TrimSpace(t.cfg.ScalpTPDecMode)) {
		case "exp", "exponential":
			// geometric decay: baseTP * factor^k, floored
			f := t.cfg.ScalpTPDecayFactor
			if f <= 0 {
				f = 1.0
			}
			factorPow := 1.0
			for i := 0; i < k; i++ {
				factorPow *= f
			}
			tpPct = baseTP * factorPow
		default:
			// linear: baseTP - k * decPct, floored
			dec := t.cfg.ScalpTPDecPct
			tpPct = baseTP - float64(k)*dec
		}

		minTP := t.cfg.ScalpTPMinPct
		if tpPct < minTP {
			tpPct = minTP
		}
		// apply the (possibly reduced) TP for the scalp only
		if side == SideBuy {
			take = price * (1.0 + tpPct/100.0)
		} else {
			take = price * (1.0 - tpPct/100.0)
		}

		// >>> DEBUG LOG <<<
		log.Printf("[DEBUG] scalp tp decay: k=%d mode=%s baseTP=%.3f%% tpPct=%.3f%% minTP=%.3f%% take=%.2f",
			k, t.cfg.ScalpTPDecMode, t.cfg.TakeProfitPct, tpPct, minTP, take)
	}

	// --- apply entry fee (preliminary; may be replaced by broker-provided commission below) ---
	feeRate := t.cfg.FeeRatePct
	entryFee := quote * (feeRate / 100.0)
	if t.cfg.DryRun {
		t.equityUSD -= entryFee
	}

	// --- NEW: side-biased Lot reason (without winLow) ---
	var gatesReason string
	if equityTriggerSell && side == SideSell && equitySpareBase > 0 {
		gatesReason = fmt.Sprintf("EQUITY Trading: equityUSD=%.2f lastAddEquitySell=%.2f sellEquityMultiplier=%.6f(triggerAt:>=%.2f) equitySpareBase=%.8f confidenceMult=%.2f", t.equityUSD, t.lastAddEquitySell, t.equityUSD/t.lastAddEquitySell, t.cfg.SellEquityTriggerMult, equitySpareBase, confMult)
	} else if equityTriggerBuy && side == SideBuy && equitySpareQuote > 0 {
		gatesReason = fmt.Sprintf("EQUITY Trading: equityUSD=%.2f lastAddEquityBuy=%.2f buyEquityMultiplier=%.6f(triggerAt:>=%.2f) equitySpareQuote=%.2f confidenceMult=%.2f", t.equityUSD, t.lastAddEquityBuy, t.equityUSD/t.lastAddEquityBuy, t.cfg.BuyEquityTriggerMult, equitySpareQuote, confMult)
	} else if side == SideBuy {
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|latchTargetHr>=%.2fHr|confidence=%.2f",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr, 2.0*reasonTFloorHr, d.Confidence,
		)
	} else { // SideSell
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|latchTargetHr>=%.2fHr|confidence=%.2f",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr, 2.0*reasonTFloorHr, d.Confidence,
		)
	}
	if d.Reason != "" {
		gatesReason = appendReason(gatesReason, "decision{"+d.Reason+"}")
	}

	refundFromOpposite := 0.0
	refundMinConf := 0.60
	if t.refundBuyUSD > 0 && side == SideSell && confMult >= refundMinConf {
		// turn refund USD into extra base at current price
		extraBase := t.refundBuyUSD / price

		// how much room do we actually have (in base)?
		room := spare - base
		if room < 0 {
			room = 0
		}
		if extraBase > room {
			extraBase = room
		}

		// snap to step if we know it
		if baseStep > 0 {
			extraBase = math.Floor(extraBase/baseStep) * baseStep
		}

		if extraBase > 0 {
			base += extraBase
			quote = base * price

			consumedUSD := extraBase * price
			refundFromOpposite = consumedUSD

			// reduce stored refund
			t.refundBuyUSD -= consumedUSD
			if t.refundBuyUSD < 0 {
				t.refundBuyUSD = 0
			}

			if t.refundBuyUSD == 0 {
				gatesReason = strings.TrimSpace(gatesReason + "|refund=buy-full")
			} else {
				gatesReason = strings.TrimSpace(gatesReason + "|refund=buy-partial")
			}
		}
	} else if t.refundBuyUSD > 0 && side == SideSell && confMult < refundMinConf {
		log.Printf("TRACE refund.block side=%s conf=%.2f need>=%.2f refundBuyUSD=%.2f",
			side, confMult, refundMinConf, t.refundBuyUSD)
	}

	if t.refundSellUSD > 0 && side == SideBuy && confMult >= refundMinConf {
		extraQuote := t.refundSellUSD

		// how much room do we actually have (in quote)?
		room := spare - quote
		if room < 0 {
			room = 0
		}
		if extraQuote > room {
			extraQuote = room
		}

		// snap to quoteStep
		if quoteStep > 0 {
			extraQuote = math.Floor(extraQuote/quoteStep) * quoteStep
		}
		if extraQuote > 0 {
			quote += extraQuote
			base = quote / price

			consumedUSD := extraQuote
			refundFromOpposite = consumedUSD

			// reduce stored refund
			t.refundSellUSD -= consumedUSD
			if t.refundSellUSD < 0 {
				t.refundSellUSD = 0
			}

			if t.refundSellUSD == 0 {
				gatesReason = strings.TrimSpace(gatesReason + "|refund=sell-full")
			} else {
				gatesReason = strings.TrimSpace(gatesReason + "|refund=sell-partial")
			}
		}
	} else if t.refundSellUSD > 0 && side == SideBuy && confMult < refundMinConf {
		log.Printf("TRACE refund.block side=%s conf=%.2f need>=%.2f refundSellUSD=%.2f",
			side, confMult, refundMinConf, t.refundSellUSD)
	}

	if side == SideBuy {
		buySpareUSD := spare
		if buySpareUSD < 0 {
			buySpareUSD = 0
		}
		t.SpareBuyUSD = buySpareUSD
	}
	if side == SideSell {
		sellSpareUSD := spare * price
		if sellSpareUSD < 0 {
			sellSpareUSD = 0
		}
		t.SpareSellUSD = sellSpareUSD
	}
	//-----------------------------------------------------------------------------------------------------------
	//------------------ Place live order without holding the lock.=====================
	//-------------------------------------------------------------------------------------------------------------------
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
		offsetBps := t.cfg.LimitPriceOffsetBps
		limitWait := t.cfg.LimitTimeoutSec
		wantLimit := strings.ToLower(strings.TrimSpace(t.cfg.OrderType)) == "limit" && offsetBps > 0 && limitWait > 0

		// ---- ONE-SHOT MARKET PREFERENCE (after a maker timeout) ----
		// If a previous maker attempt timed out, consume a one-tick "market preference".
		// This tick will skip maker (if an open actually happens after gates).
		// Regardless of whether we open or HOLD, the preference is consumed now.
		recheckNow := false
		if side == SideBuy && t.pendingRecheckBuy {
			recheckNow = true
		}
		if side == SideSell && t.pendingRecheckSell {
			recheckNow = true
		}

		if wantLimit && recheckNow {
			wantLimit = false
			log.Printf("TRACE postonly.skip reason=recheck_market_next_tick side=%s", side)
		}
		// consume the one-shot preference immediately so it never lingers
		t.mu.Lock()
		if recheckNow {
			if side == SideBuy {
				t.pendingRecheckBuy = false
			} else {
				t.pendingRecheckSell = false
			}
		}
		t.mu.Unlock()

		// --- NEW (Phase 4): maker-first routing via Broker when ORDER_TYPE=limit (async, per-side) ---
		if wantLimit {
			// Compute limit price away from last snapshot; compute base from limit price (keeps notional under control).
			var limitPx float64
			if side == SideBuy {
				limitPx = price * (1.0 - offsetBps/10000.0)
			} else {
				limitPx = price * (1.0 + offsetBps/10000.0)
			}
			// Snap to tick if provided
			tick := t.cfg.PriceTick
			if tick > 0 {
				if side == SideBuy {
					limitPx = math.Floor(limitPx/tick) * tick
				} else {
					limitPx = math.Ceil(limitPx/tick) * tick
				}
			}
			baseAtLimit := quote / limitPx
			// Snap base to step if provided
			if t.cfg.BaseStep > 0 {
				baseAtLimit = math.Floor(baseAtLimit/t.cfg.BaseStep) * t.cfg.BaseStep
			}

			if baseAtLimit > 0 && baseAtLimit*limitPx >= minNotional {
				log.Printf("TRACE postonly.place side=%s limit=%.8f baseReq=%.8f timeout_sec=%d", side, limitPx, baseAtLimit, limitWait)
				orderID, err := t.broker.PlaceLimitPostOnly(ctx, t.cfg.ProductID, side, limitPx, baseAtLimit)
				if err == nil && strings.TrimSpace(orderID) != "" {
					// Initialize per-side channel
					if side == SideBuy && t.pendingBuyCh == nil {
						t.pendingBuyCh = make(chan OpenResult, 1)
					}
					if side == SideSell && t.pendingSellCh == nil {
						t.pendingSellCh = make(chan OpenResult, 1)
					}

					// -------------------------------------------------------------------------------------------------
					// Create per-side PendingOpen ("lot-in-waiting") and cancellation context.
					//
					// Mental model:
					// PendingOpen = proposed future lot
					// Position    = confirmed live lot
					//
					// Lifecycle:
					// Decision → maker order submitted → PendingOpen → repricing/polling
					// → fill confirmed → Position (real lot) → PendingOpen cleared
					//
					// Why:
					// Maker/post-only fills are asynchronous and may complete several ticks later.
					// PendingOpen preserves decision-time state until execution is finalized.
					//
					// Stores:
					// - Order lifecycle (order id, repricing history, deadline)
					// - Intended trade (quote, base-at-limit, take)
					// - Decision metadata (confidence multiplier, AI mode, profit gate)
					// - Refund-service bookkeeping
					// - Equity-trigger runner flags
					//
					// Important:
					// Final execution price/base/fee are unknown here.
					// At fill time, PendingOpen matures into a real Position (lot),
					// carrying forward the original decision metadata.
					// -------------------------------------------------------------------------------------------------
					pctx, cancel := context.WithCancel(ctx)
					t.mu.Lock()
					if side == SideBuy {
						t.pendingBuyCtx = pctx
						t.pendingBuyCancel = cancel
						t.pendingBuy = &PendingOpen{
							Side:             side,
							LimitPx:          limitPx,
							BaseAtLimit:      baseAtLimit,
							Quote:            quote,
							Take:             take,
							Reason:           gatesReason, // set later below
							RefundPortionUSD: refundFromOpposite,
							ProductID:        t.cfg.ProductID,
							CreatedAt:        time.Now().UTC(),
							Deadline:         time.Now().Add(time.Duration(limitWait) * time.Second),
							EquityBuy:        equityTriggerBuy,
							EquitySell:       equityTriggerSell,
							OrderID:          orderID,
							History:          make([]string, 0, 5), // NEW
							ConfidenceMult:   confMult,
							EntryAIMode:      entryAIMode,
							ProfitGateUSD:    entryProfitGateUSD,
						}
						if side == SideBuy && t.pendingBuy != nil {
							log.Printf("TRACE postonly.pending.set side=%s order_id=%s limit=%.8f base=%.8f quote=%.2f dl=%s eqFlags[buy=%v sell=%v]",
								side, t.pendingBuy.OrderID, t.pendingBuy.LimitPx, t.pendingBuy.BaseAtLimit, t.pendingBuy.Quote,
								t.pendingBuy.Deadline.Format(time.RFC3339), t.pendingBuy.EquityBuy, t.pendingBuy.EquitySell)
						}
					} else {
						t.pendingSellCtx = pctx
						t.pendingSellCancel = cancel
						t.pendingSell = &PendingOpen{
							Side:             side,
							LimitPx:          limitPx,
							BaseAtLimit:      baseAtLimit,
							Quote:            quote,
							Take:             take,
							Reason:           gatesReason, // set later below
							RefundPortionUSD: refundFromOpposite,
							ProductID:        t.cfg.ProductID,
							CreatedAt:        time.Now().UTC(),
							Deadline:         time.Now().Add(time.Duration(limitWait) * time.Second),
							EquityBuy:        equityTriggerBuy,
							EquitySell:       equityTriggerSell,
							OrderID:          orderID,
							History:          make([]string, 0, 5), // NEW
							ConfidenceMult:   confMult,
							EntryAIMode:      entryAIMode,
							ProfitGateUSD:    entryProfitGateUSD,
						}
						if side == SideSell && t.pendingSell != nil {
							log.Printf("TRACE postonly.pending.set side=%s order_id=%s limit=%.8f base=%.8f quote=%.2f dl=%s eqFlags[buy=%v sell=%v]",
								side, t.pendingSell.OrderID, t.pendingSell.LimitPx, t.pendingSell.BaseAtLimit, t.pendingSell.Quote,
								t.pendingSell.Deadline.Format(time.RFC3339), t.pendingSell.EquityBuy, t.pendingSell.EquitySell)
						}
					}

					// Capture immutable references for the async poller.
					// The poller manages repricing, partial fills, timeout handling,
					// and eventually emits a finalized OpenResult back to step().
					var pend *PendingOpen
					var ch chan OpenResult
					var pctxUse context.Context
					if side == SideBuy {
						pend = t.pendingBuy
						ch = t.pendingBuyCh
						pctxUse = t.pendingBuyCtx
					} else {
						pend = t.pendingSell
						ch = t.pendingSellCh
						pctxUse = t.pendingSellCtx
					}
					t.mu.Unlock()

					// Spawn async maker-first poller.
					//
					// Responsibilities:
					// - Poll broker order state
					// - Track partial fills across reprices
					// - Reprice resting maker orders when allowed
					// - Aggregate session VWAP/base/fees
					// - Emit a finalized OpenResult (filled or non-filled)
					//
					// Important:
					// The poller NEVER mutates lots directly.
					// step() drain path remains the single synchronous authority that
					// converts PendingOpen → Position and persists state.
					go func(initOrderID string, side OrderSide, deadline time.Time, initLimitPx, initBaseAtLimit float64, pend *PendingOpen, ch chan OpenResult, pctx context.Context) {
						log.Printf("TRACE postonly.poll.start side=%s init_id=%s init_limit=%.8f init_base=%.8f deadline=%s offset_bps=%.3f",
							side, initOrderID, initLimitPx, initBaseAtLimit, deadline.Format(time.RFC3339), offsetBps)
						defer func() { log.Printf("TRACE postonly.poll.stopped side=%s initial_id=%s", side, initOrderID) }()

						orderID := initOrderID
						lastLimitPx := initLimitPx
						lastReprice := time.Now()

						// --- Session-level aggregates and per-order delta trackers ---
						var sessBase, sessQuote, sessFee float64
						var lastSeenBase, lastSeenQuote, lastSeenFee float64 // per current orderID

						var repriceCount int

					poll:
						for time.Now().Before(deadline) {
							// Check for cancellation before doing work
							select {
							case <-pctx.Done():
								log.Printf("TRACE postonly.poll.cancelled side=%s last_id=%s", side, orderID)
								break poll
							default:
							}

							// Check current order fill state first
							ord, gErr := t.broker.GetOrder(pctx, t.cfg.ProductID, orderID)
							if gErr == nil && ord != nil {
								// Compute deltas relative to this specific orderID
								dBase := ord.BaseSize - lastSeenBase
								dQuote := ord.QuoteSpent - lastSeenQuote
								dFee := ord.CommissionUSD - lastSeenFee
								if dBase < 0 {
									dBase = 0
								}
								if dQuote < 0 {
									dQuote = 0
								}
								if dFee < 0 {
									dFee = 0
								}
								// Accumulate session totals
								sessBase += dBase
								sessQuote += dQuote
								sessFee += dFee
								// Update last-seen for this order
								lastSeenBase = ord.BaseSize
								lastSeenQuote = ord.QuoteSpent
								lastSeenFee = ord.CommissionUSD

								log.Printf("TRACE postonly.poll.tick side=%s order_id=%s status=%s price=%.8f base=%.8f quote=%.2f fee=%.6f sess_agg[base=%.8f quote=%.2f fee=%.6f] reprices=%d",
									side, orderID, strings.ToUpper(strings.TrimSpace(ord.Status)), ord.Price, ord.BaseSize, ord.QuoteSpent, ord.CommissionUSD,
									sessBase, sessQuote, sessFee, repriceCount)

								// Determine status-specific behavior
								switch strings.ToUpper(strings.TrimSpace(ord.Status)) {
								case "FILLED":
									// fully filled → done, emit VWAP of session
									vwap := 0.0
									if sessBase > 0 {
										vwap = sessQuote / sessBase
									}
									placed := &PlacedOrder{
										Price:         vwap,
										BaseSize:      sessBase,
										QuoteSpent:    sessQuote,
										CommissionUSD: sessFee,
									}
									log.Printf("TRACE postonly.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f", orderID, ord.Price, ord.BaseSize, ord.QuoteSpent, ord.CommissionUSD)
									mtxOrders.WithLabelValues("live", string(side)).Inc()
									mtxTrades.WithLabelValues("open").Inc()
									log.Printf("TRACE postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
										side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
									log.Printf("[KPI] maker.open.filled side=%s vwap=%.8f base=%.8f quote=%.2f fee=%.6f order_id=%s",
										side, placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD, orderID)

									safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
									return

								case "PARTIALLY_FILLED":
									if t.pendingCancelRequested(side) {
										log.Printf("TRACE postonly.reprice.skip.cancel_requested side=%s order_id=%s", side, orderID)
										lastReprice = time.Now()
										break
									}
									// still open; allow reprice path (throttled)
									if time.Since(lastReprice) >= time.Duration(t.cfg.RepriceIntervalMs)*time.Millisecond {
										log.Printf("TRACE postonly.reprice.try.partially side=%s order_id=%s last_limit=%.8f reprice_count=%d",
											side, orderID, lastLimitPx, repriceCount)
										newID, newLastLimitPx, newRepriceCount, did := t.maybeRepriceOnce(
											pctx,
											side,
											orderID,
											initLimitPx,
											initBaseAtLimit,
											lastLimitPx,
											offsetBps,
											pend,
											repriceCount,
										)
										if did && newID != orderID {
											log.Printf("TRACE postonly.reprice.swap.partially side=%s old_id=%s new_id=%s new_limit=%.8f count=%d",
												side, orderID, newID, newLastLimitPx, newRepriceCount)
											// switched to a new order: reset per-order deltas (session aggregates stay)
											orderID = newID
											lastLimitPx = newLastLimitPx
											repriceCount = newRepriceCount
											lastSeenBase, lastSeenQuote, lastSeenFee = 0, 0, 0
										} else {
											log.Printf("TRACE postonly.reprice.skip.partially side=%s order_id=%s reason=no_guard_or_no_improve last_limit=%.8f count=%d",
												side, orderID, lastLimitPx, repriceCount)
											lastLimitPx = newLastLimitPx
											repriceCount = newRepriceCount
										}
										lastReprice = time.Now()
									}

								case "NEW", "PENDING_CANCEL":
									if t.pendingCancelRequested(side) {
										log.Printf("TRACE postonly.reprice.skip.cancel_requested side=%s order_id=%s", side, orderID)
										lastReprice = time.Now()
										break
									}
									// resting or in cancel transition; keep polling
									if time.Since(lastReprice) >= time.Duration(t.cfg.RepriceIntervalMs)*time.Millisecond {
										log.Printf("TRACE postonly.reprice.try.new side=%s order_id=%s last_limit=%.8f reprice_count=%d",
											side, orderID, lastLimitPx, repriceCount)
										newID, newLastLimitPx, newRepriceCount, did := t.maybeRepriceOnce(
											pctx,
											side,
											orderID,
											initLimitPx,
											initBaseAtLimit,
											lastLimitPx,
											offsetBps,
											pend,
											repriceCount,
										)
										if did && newID != orderID {
											log.Printf("TRACE postonly.reprice.swap.new side=%s old_id=%s new_id=%s new_limit=%.8f count=%d",
												side, orderID, newID, newLastLimitPx, newRepriceCount)
											orderID = newID
											lastLimitPx = newLastLimitPx
											repriceCount = newRepriceCount
											lastSeenBase, lastSeenQuote, lastSeenFee = 0, 0, 0
										} else {
											log.Printf("TRACE postonly.reprice.skip.new side=%s order_id=%s reason=no_guard_or_no_improve last_limit=%.8f count=%d",
												side, orderID, lastLimitPx, repriceCount)
											lastLimitPx = newLastLimitPx
											repriceCount = newRepriceCount
										}
										lastReprice = time.Now()
									}

								case "CANCELED", "REJECTED", "EXPIRED":

									// terminal without remaining open quantity → emit VWAP if any session fills, else non-fill
									if sessBase > 0 || sessQuote > 0 {
										vwap := 0.0
										if sessBase > 0 {
											vwap = sessQuote / sessBase
										}
										placed := &PlacedOrder{
											Price:         vwap,
											BaseSize:      sessBase,
											QuoteSpent:    sessQuote,
											CommissionUSD: sessFee,
										}
										log.Printf("[KPI] maker.open.filled side=%s vwap=%.8f base=%.8f quote=%.2f fee=%.6f order_id=%s status=%s",
											side,
											func() float64 {
												if sessBase > 0 {
													return sessQuote / sessBase
												}
												return 0
											}(),
											sessBase, sessQuote, sessFee, orderID, strings.ToUpper(strings.TrimSpace(ord.Status)))

										log.Printf("TRACE postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
											side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
										safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
									} else {
										log.Printf("TRACE postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
											side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
										safeSend(ch, OpenResult{Filled: false, Placed: nil, OrderID: orderID})
									}

									log.Printf("TRACE postonly.poll.done side=%s order_id=%s final=%s vwap=%.8f base=%.8f quote=%.2f fee=%.6f",
										side, orderID, strings.ToUpper(strings.TrimSpace(ord.Status)),
										func() float64 {
											if sessBase > 0 {
												return sessQuote / sessBase
											}
											return 0
										}(), sessBase, sessQuote, sessFee)

									return

								default:
									// unknown status; be conservative and keep polling
								}
							}

							// Sleep-or-cancel wait
							select {
							case <-pctx.Done():
								log.Printf("TRACE postonly.poll.cancelled side=%s last_id=%s", side, orderID)
								break poll
							case <-time.After(200 * time.Millisecond):
							}
						}

						// On timeout or cancellation, cancel any resting order if still open.
						_ = t.broker.CancelOrder(pctx, t.cfg.ProductID, orderID)
						log.Printf("TRACE postonly.poll.timeout side=%s last_id=%s sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
							side, orderID, sessBase, sessQuote, sessFee)
						log.Printf("TRACE postonly.timeout order_id=%s", orderID)

						// On TIMEOUT: emit VWAP if any session fills, else non-fill
						if sessBase > 0 || sessQuote > 0 {
							vwap := 0.0
							if sessBase > 0 {
								vwap = sessQuote / sessBase
							}
							placed := &PlacedOrder{
								Price:         vwap,
								BaseSize:      sessBase,
								QuoteSpent:    sessQuote,
								CommissionUSD: sessFee,
							}
							log.Printf("TRACE postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
								side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
							safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
						} else {
							log.Printf("TRACE postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
								side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
							safeSend(ch, OpenResult{Filled: false, Placed: nil, OrderID: orderID})
						}
						// Do not clear pending here; step() drain will clear & persist synchronously
					}(orderID, side, time.Now().Add(time.Duration(limitWait)*time.Second), limitPx, baseAtLimit, pend, ch, pctxUse)

					// Persist PendingOpen after poller launch.
					// Pending state is intentionally cleared later by the synchronous
					// drain path, not by the poller goroutine.
					t.mu.Lock()
					_ = t.saveStateNoLock()
					updatePreviousAIRaw()
					t.mu.Unlock()
					return fmt.Sprintf("OPEN-PENDING side=%s", side), nil
				} else if err != nil {
					log.Printf("TRACE postonly.error hold_for_recheck err=%v", err)
				}
			}
		}

		// --- NEW (Phase 2): gate market fallback by recheck flag after async timeout/error ---
		allowMarket := true
		if wantLimit {
			if side == SideBuy {
				allowMarket = t.pendingRecheckBuy
			} else if side == SideSell {
				allowMarket = t.pendingRecheckSell
			}
		}

		// If maker path did not result in a fill (or was skipped), fall back to market path (baseline behavior).
		if placed == nil {
			if !allowMarket {
				log.Printf("TRACE postonly.market_fallback.blocked side=%s reason=recheck_flag_not_set", side)

				t.mu.Lock()
				updatePreviousAIRaw()
				t.mu.Unlock()

				return "HOLD", nil
			}
			var err error
			placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
			// TODO: remove TRACE
			log.Printf("TRACE order.open request side=%s quote=%.2f baseEst=%.8f priceSnap=%.8f take=%.8f",
				side, quote, base, price, take)
			log.Printf("TRACE postonly.market_fallback.go side=%s quote=%.2f", side, quote)
			log.Printf("[KPI] taker.open side=%s quote=%.2f reason=market_fallback", side, quote)

			if err != nil {
				// Retry once with ORDER_MIN_USD on insufficient-funds style failures.
				e := strings.ToLower(err.Error())
				if quote > minNotional && (strings.Contains(e, "insufficient") || strings.Contains(e, "funds") || strings.Contains(e, "400")) {
					log.Printf("[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f", quote, err, minNotional)
					quote = minNotional
					base = quote / price
					// TODO: remove TRACE
					log.Printf("TRACE order.open retry side=%s quote=%.2f baseEst=%.8f", side, quote, base)
					placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
				}
				if err != nil {
					if t.cfg.UseDirectSlack {
						postSlack(fmt.Sprintf("ERR step: %v", err))
					}

					t.mu.Lock()
					updatePreviousAIRaw()
					t.mu.Unlock()

					return "", err
				}
			}
			// TODO: remove TRACE
			if placed != nil {
				log.Printf("TRACE order.open placed price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
					placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
			}
			mtxOrders.WithLabelValues("live", string(side)).Inc()
			mtxTrades.WithLabelValues("open").Inc()
		}
	} else {
		mtxTrades.WithLabelValues("open").Inc()
	}

	// Re-lock to mutate state (append new lot to THIS SIDE).
	t.mu.Lock()

	// --- NEW (Phase 2): reset recheck flag after successful market fallback open ---
	if !t.cfg.DryRun {
		// We only reset when a real order is being appended (placed != nil).
		if placed != nil {
			offsetBps := t.cfg.LimitPriceOffsetBps
			limitWait := t.cfg.LimitTimeoutSec
			wantLimit := strings.ToLower(strings.TrimSpace(t.cfg.OrderType)) == "limit" && offsetBps > 0 && limitWait > 0
			if wantLimit {
				if side == SideBuy {
					t.pendingRecheckBuy = false
				} else if side == SideSell {
					t.pendingRecheckSell = false
				}
			}
		}
	}

	// --- MINIMAL CHANGE: use actual filled size/price when available ---
	priceToUse := price
	baseRequested := base
	baseToUse := baseRequested
	actualQuote := quote

	if placed != nil {
		if placed.Price > 0 {
			priceToUse = placed.Price
		}
		if placed.BaseSize > 0 {
			baseToUse = placed.BaseSize
		}
		if placed.QuoteSpent > 0 {
			actualQuote = placed.QuoteSpent
		}
		// Log WARN on partial fill (filled < requested) with a small tolerance.
		const tol = 1e-9
		if baseToUse+tol < baseRequested {
			log.Printf("[WARN] partial fill: requested_base=%.8f filled_base=%.8f (%.2f%%)",
				baseRequested, baseToUse, 100.0*(baseToUse/baseRequested))
			// TODO: remove TRACE
			log.Printf("TRACE fill.open partial requested=%.8f filled=%.8f", baseRequested, baseToUse)
		}
	}

	// Prefer broker-provided commission for entry if present; otherwise fallback to FEE_RATE_PCT.
	if placed != nil {
		if placed.CommissionUSD > 0 {
			entryFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (entry); falling back to FEE_RATE_PCT=%.4f%%", feeRate)
			entryFee = actualQuote * (feeRate / 100.0)
		}
	} else {
		// DryRun path keeps previously computed entryFee and adjusts by delta as before.
	}

	if t.cfg.DryRun {
		// already deducted above for DryRun using quote; adjust to the actualQuote delta
		delta := (actualQuote - quote) * (feeRate / 100.0)
		t.equityUSD -= delta
	}

	if refundFromOpposite > 0 {
		origBase := baseToUse
		origQuote := actualQuote
		origFee := entryFee

		refundBase := refundFromOpposite / priceToUse
		if refundBase > baseToUse {
			refundBase = baseToUse
		}

		keptBase := baseToUse - refundBase
		if keptBase < 0 {
			keptBase = 0
		}

		keptQuote := actualQuote
		keptFee := entryFee
		refundQuote := refundFromOpposite
		refundFee := refundQuote * (t.cfg.FeeRatePct / 100.0)

		if origBase > 0 {
			keptQuote = origQuote * (keptBase / origBase)
			keptFee = origFee * (keptBase / origBase)
			refundQuote = origQuote * (refundBase / origBase)
			refundFee = origFee * (refundBase / origBase)
		}

		t.creditRefundService(side, refundQuote, refundFee)

		baseToUse = keptBase
		actualQuote = keptQuote
		entryFee = keptFee
	}

	newLot := &Position{
		OpenPrice:        priceToUse,
		Side:             side,
		SizeBase:         baseToUse,
		OpenTime:         now,
		EntryFee:         entryFee,
		OpenNotionalUSD:  actualQuote, // <<< USD PERSISTENCE: notional in USD at open
		Reason:           gatesReason, // side-biased; no winLow
		Take:             take,
		Version:          1,
		LotID:            len(book.Lots),
		EntryOrderID:     placedOrderID(placed),
		RefundPortionUSD: refundFromOpposite,
		ConfidenceMult:   confMult,
		EntryAIMode:      entryAIMode,
		ProfitGateUSD:    entryProfitGateUSD,
	}

	log.Printf(
		"[KPI] lot.created side=%s mode=%s conf=%.2f gate=%.2f",
		newLot.Side,
		newLot.EntryAIMode,
		newLot.ConfidenceMult,
		newLot.ProfitGateUSD,
	)

	book.Lots = append(book.Lots, newLot)
	t.consolidateDust(book, priceToUse, minNotional)
	t.didConsolidateStartup = false
	// Use wall clock for lastAdd to drive spacing/decay even if candle time is zero.
	if side == SideBuy {
		t.lastAddBuy = wallNow
		t.winLowBuy = priceToUse
		t.latchedGateBuy = 0
		t.SpareBuyUSD -= actualQuote
		if t.SpareBuyUSD < 0 {
			t.SpareBuyUSD = 0
		}
	} else {
		t.lastAddSell = wallNow
		t.winHighSell = priceToUse
		t.latchedGateSell = 0
		t.SpareSellUSD -= actualQuote
		if t.SpareSellUSD < 0 {
			t.SpareSellUSD = 0
		}
	}
	// --- NEW: capture equity snapshots at add (side-specific) ---
	if side == SideSell {
		old := t.lastAddEquitySell
		t.lastAddEquitySell = t.equityUSD
		log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f", side, old, t.lastAddEquitySell)
	} else {
		old := t.lastAddEquityBuy
		t.lastAddEquityBuy = t.equityUSD
		log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f", side, old, t.lastAddEquityBuy)
	}

	// Assign/designate runner logic
	// --- CHANGED: Do NOT auto-assign runner for first/non-equity lots; instead,
	//               promote the equity-triggered lot to runner immediately.
	if equityTriggerSell && side == SideSell {
		newIdx := len(book.Lots) - 1 // promote the equity trade lot to runner
		addRunner(book, newIdx)
		r := book.Lots[newIdx]
		// Initialize/Reset trailing fields for the new runner
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		// Apply runner targets (stretched TP)
		t.applyRunnerTargets(r)
		log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", newIdx, side, r.OpenPrice, r.Take)
	}
	// --- NEW (minimal): promote equity-triggered BUY add to runner ---
	if equityTriggerBuy && side == SideBuy {
		newIdx := len(book.Lots) - 1
		addRunner(book, newIdx)
		r := book.Lots[newIdx]
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		t.applyRunnerTargets(r)
		log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", newIdx, side, r.OpenPrice, r.Take)
	}
	// (If not equityTriggerSell/equityTriggerBuy, leave RunnerIDs unchanged so first lot is NOT the runner.)

	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f take=%.2f fee=%.4f reason=%s [%s]",
			side, actualQuote, baseToUse, newLot.Take, entryFee, newLot.Reason, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s notional=%.2f take=%.2f fee=%.4f reason=%s",
			side, newLot.OpenNotionalUSD, newLot.Take, entryFee, newLot.Reason)
	}
	if t.cfg.UseDirectSlack {
		postSlack(msg)
	}
	// persist new state (no locking while writing; snapshot constructed here under lock)
	if err := t.saveStateNoLock(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		log.Printf("TRACE state.save error=%v", err)
	}
	log.Printf("[KPI] summary equity=%.2f daily_pnl=%.2f lots_buy=%d lots_sell=%d product=%s",
		t.equityUSD, t.dailyPnL, len(t.book(SideBuy).Lots), len(t.book(SideSell).Lots), t.cfg.ProductID)
	updatePreviousAIRaw()
	t.mu.Unlock()
	return msg, nil
}

// consolidateDust collapses tiny (notional < minNotional) lots on a side.
// Behavior:
// - If there is exactly 1 lot and it's below minNotional → pad it up to minNotional.
// - If there are 2+ lots →
//  1. collapse tail dust backward,
//  2. sweep older dust forward into newest,
//  3. if at the end there's 1 dust left → pad it.
//
// RunnerIDs are kept authoritative.
func (t *Trader) consolidateDust(book *SideBook, px float64, minNotional float64) {
	// helper to pad a single lot to minNotional (synthetic size increase!)
	padSingleLot := func(lot *Position) {
		if lot == nil || px <= 0 {
			return
		}
		curNotional := lot.SizeBase * px
		if curNotional < minNotional {
			requiredBase := minNotional / px
			lot.SizeBase = requiredBase
			// keep original entry price; recompute USD notional off entry
			lot.OpenNotionalUSD = lot.SizeBase * lot.OpenPrice
			lot.Reason = strings.TrimSpace(lot.Reason + " padded-to-min")
		}
	}

	// 0 lots: nothing to do
	if len(book.Lots) == 0 {
		return
	}

	// 1 lot at start: pad and stop
	if len(book.Lots) == 1 {
		padSingleLot(book.Lots[0])
		return
	}

	ensureRunner := func(idx int) {
		for _, r := range book.RunnerIDs {
			if r == idx {
				return
			}
		}
		book.RunnerIDs = append(book.RunnerIDs, idx)
	}

	// shift RunnerIDs after removing lot at removedIdx
	shiftAfterRemoval := func(removedIdx int) {
		if len(book.RunnerIDs) == 0 {
			return
		}
		out := book.RunnerIDs[:0]
		for _, r := range book.RunnerIDs {
			if r == removedIdx {
				continue
			}
			if r > removedIdx {
				r--
			}
			out = append(out, r)
		}
		book.RunnerIDs = append([]int(nil), out...)
	}

	// merge fromIdx -> toIdx (toIdx absorbs)
	mergeInto := func(fromIdx, toIdx int) {
		a := book.Lots[toIdx] // survivor (*Position)
		b := book.Lots[fromIdx]

		// see if any was runner
		wereRunner := false
		for _, r := range book.RunnerIDs {
			if r == fromIdx || r == toIdx {
				wereRunner = true
				break
			}
		}

		// VWAP the two
		totalBase := a.SizeBase + b.SizeBase
		if totalBase > 0 {
			totalQuote := a.OpenPrice*a.SizeBase + b.OpenPrice*b.SizeBase
			a.OpenPrice = totalQuote / totalBase
		}
		a.SizeBase += b.SizeBase
		a.EntryFee += b.EntryFee
		a.OpenNotionalUSD = a.SizeBase * a.OpenPrice

		// tag reason
		a.Reason = strings.TrimSpace(a.Reason + "|merge:" + strconv.Itoa(b.LotID))

		// drop fromIdx
		book.Lots = append(book.Lots[:fromIdx], book.Lots[fromIdx+1:]...)
		shiftAfterRemoval(fromIdx)

		// re-assert runner
		if wereRunner {
			ensureRunner(toIdx)
		}
	}

	// helper: notional at idx using current px
	notionalAt := func(idx int) float64 {
		if idx < 0 || idx >= len(book.Lots) {
			return 0
		}
		return book.Lots[idx].SizeBase * px
	}

	// 1) collapse tail dust backward
	for len(book.Lots) > 1 {
		lastIdx := len(book.Lots) - 1
		if notionalAt(lastIdx) >= minNotional {
			break
		}
		mergeInto(lastIdx, lastIdx-1)
	}

	// if we now have only 1 lot, pad it (if dust) and stop
	if len(book.Lots) == 1 {
		padSingleLot(book.Lots[0])
		return
	}

	// 2) sweep older dust forward into the next valid lot (forward-merge)
	i := 0
	for i < len(book.Lots)-1 {
		if notionalAt(i) < minNotional {
			// find next index j>i; prefer the first non-dust, fall back to the tail
			j := i + 1
			for j < len(book.Lots)-1 && notionalAt(j) < minNotional {
				j++
			}
			mergeInto(i, j)
			// after merge, a new lot now occupies index i; re-check this index
			continue
		}
		i++
	}

	// final safety: if after all merges we are left with 1 lot and it's dust, pad it
	if len(book.Lots) == 1 {
		padSingleLot(book.Lots[0])
	}
}
