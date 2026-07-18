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
//   - Lots carry EntryOrderID; NextLotSeq is incremented on each append.
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
	"sort"
	"strings"
	"time"
)

const Version = 142

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
		log.Printf("[TRACE] fallback.buffer.full: empty the buffer (drop stale) and resending")
		select {
		case <-ch:
		default:
		}
		log.Printf("[TRACE] fallback.buffer.emptied: emptied buffer and resending")
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
		log.Printf("[TRACE] refund.sell.service_credited side=%s gross=%.8f fee=%.8f net=%.8f spareSell_after=%.8f",
			side, refundQuote, refundFee, refundNet, t.SpareSellUSD)
		return
	}

	t.SpareBuyUSD += refundNet
	log.Printf("[TRACE] refund.buy.service_credited side=%s gross=%.8f fee=%.8f net=%.8f spareBuy_after=%.8f",
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
	rpStart := time.Now()
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

	driftBps := 0.0
	if initLimitPx > 0 {
		driftBps = math.Abs((newLimitPx-initLimitPx)/initLimitPx) * 10000.0
	}

	improveTicks := 0.0
	if tick > 0 {
		improveTicks = math.Abs(newLimitPx-lastLimitPx) / tick
	}

	notional := newBase * newLimitPx
	notionalOK := newBase > 0 && notional >= t.cfg.MinNotional

	log.Printf(
		"[TRACE] postonly.reprice.eval elapsed_ms=%d side=%s order_id=%s "+
			"use_bbo=%v bid=%.8f ask=%.8f "+
			"init_limit=%.8f last_limit=%.8f candidate_limit=%.8f "+
			"tick=%.8f improve_ticks=%.2f "+
			"drift_bps=%.4f max_drift_bps=%.4f "+
			"new_base=%.8f notional=%.2f min_notional=%.2f notional_ok=%v "+
			"should_reprice=%v reprice_count=%d max_count=%d",
		time.Since(rpStart).Milliseconds(),
		side,
		orderID,
		useBBO, bid, ask,
		initLimitPx, lastLimitPx, newLimitPx,
		tick, improveTicks,
		driftBps, t.cfg.RepriceMaxDriftBps,
		newBase, notional, t.cfg.MinNotional, notionalOK,
		shouldReprice, repriceCount, t.cfg.RepriceMaxCount,
	)

	if !shouldReprice {
		return orderID, lastLimitPx, repriceCount, false
	}

	if useBBO {
		log.Printf("[TRACE] postonly.reprice.touch side=%s bid=%.8f ask=%.8f new=%.8f last=%.8f", side, bid, ask, newLimitPx, lastLimitPx)
	} else {
		log.Printf("[TRACE] postonly.reprice.mark side=%s new=%.8f last=%.8f", side, newLimitPx, lastLimitPx)
	}

	// Cancel current and re-place at the new price
	_ = t.broker.CancelOrder(pctx, t.cfg.ProductID, orderID)
	newID, perr := t.broker.PlaceLimitPostOnly(pctx, t.cfg.ProductID, side, newLimitPx, newBase)
	if perr != nil || strings.TrimSpace(newID) == "" {
		return orderID, lastLimitPx, repriceCount, false
	}

	log.Printf("[TRACE] postonly.reprice side=%s old_id=%s new_id=%s limit=%.8f baseReq=%.8f",
		side, orderID, newID, newLimitPx, newBase)

	// Update focus to new order + persist (also appends old ID to History)
	t.repriceUpdatePending(side, newID, newLimitPx, newBase)

	// Return updated state
	return newID, newLimitPx, repriceCount + 1, true
}

// Return this lot's effective profit gate.
//
// Normal AI-confirmed lots use cfg.ProfitGateUSD.
// Reduced-confidence AI-FLAT lots may carry a smaller ProfitGateUSD.
// Older lots fall back to cfg.ProfitGateUSD.
func (t *Trader) lotProfitGateUSD(lot *Position) float64 {
	gate := lot.ProfitGateUSD
	if gate <= 0 {
		gate = t.cfg.ProfitGateUSD
	}
	return gate
}

// step consumes the current candle history and may place/close a position.
// It returns a human-readable status string for logging.
func (t *Trader) step(ctx context.Context, execHistory []Candle, signalHistory []Candle, livePrice float64, hotStart time.Time) (StepResult, error) {
	if len(execHistory) == 0 {
		return StepResult{Msg: "NO_DATA"}, nil
	}

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	// Use wall clock as authoritative "now" for pyramiding timings; fall back for zero candle time.
	wallNow := time.Now().UTC()

	now := execHistory[len(execHistory)-1].Time
	if now.IsZero() {
		now = wallNow
	}
	t.updateDaily(now)

	// Process completed asynchronous maker exits before making any new decisions.
	//
	// Poller goroutines only report completion through pendingExitCh.
	// All state mutation (lot removal, partial handling, P/L, exit records,
	// runner updates, state save, etc.) is performed here on the main trading
	// thread so Trader state remains single-writer and deterministic.
	//
	// This keeps the main loop non-blocking while ensuring completed exits are
	// reflected before evaluating new entries, exits, or pyramiding decisions.
	t.drainPendingExitCh(ctx, execHistory, livePrice)

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
			log.Printf("[TRACE] postonly.drain.recv side=%s order_id=%s filled=%v placed_nil=%v",
				SideBuy, res.OrderID, res.Filled, res.Placed == nil)

			// Decide whether this async result is safe to apply to state.
			// Repricing can create multiple order IDs, so we accept:
			// 1) the current pending order ID, or
			// 2) any historical replaced order ID recorded in PendingOpen.History.
			accept := false
			if res.Filled && res.Placed != nil {
				log.Printf("[TRACE] postonly.drain.placed side=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f",
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

				log.Printf("[TRACE] postonly.drain.accept side=%s match_current=%v match_history=%v pending_nil=%v",
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
					Version:         Version,
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
				t.archiveOrphanDust(book, priceToUse, t.cfg.MinNotional)
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

					log.Printf("[TRACE] runner.assign idx=%d side=%s open=%.8f take=%.8f",
						newIdx, side, r.OpenPrice, r.Take)
				}

				// Reset BUY-side add anchors after accepted fill.
				t.lastAddBuy = wallNow
				t.winLowBuy = priceToUse
				t.latchedGateBuy = 0

				old := t.lastAddEquity
				t.lastAddEquity = t.equityUSD
				log.Printf("[TRACE] equity.baseline.set side=%s old=%.2f new=%.2f",
					side, old, t.lastAddEquity)

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
						"[TRACE] postonly.cancel.ack side=%s order_id=%s fallback=false reason=signal_changed",
						SideBuy,
						res.OrderID,
					)
				} else {
					t.pendingRecheckBuy = true
					log.Printf(
						"[TRACE] postonly.recheck side=%s set=true reason=timeout_or_error order_id=%s",
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
			log.Printf("[TRACE] postonly.drain.recv side=%s order_id=%s filled=%v placed_nil=%v",
				SideSell, res.OrderID, res.Filled, res.Placed == nil)

			accept := false
			if res.Filled && res.Placed != nil {
				log.Printf("[TRACE] postonly.drain.placed side=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f",
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

				log.Printf("[TRACE] postonly.drain.accept side=%s match_current=%v match_history=%v pending_nil=%v",
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
					Version:         Version,
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
				t.archiveOrphanDust(book, priceToUse, t.cfg.MinNotional)
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

					log.Printf("[TRACE] runner.assign idx=%d side=%s open=%.8f take=%.8f",
						newIdx, side, r.OpenPrice, r.Take)
				}

				t.lastAddSell = wallNow
				t.winHighSell = priceToUse
				t.latchedGateSell = 0

				old := t.lastAddEquity
				t.lastAddEquity = t.equityUSD
				log.Printf("[TRACE] equity.baseline.set side=%s old=%.2f new=%.2f",
					side, old, t.lastAddEquity)

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
						"[TRACE] postonly.cancel.ack side=%s order_id=%s fallback=false reason=signal_changed",
						SideSell,
						res.OrderID,
					)
				} else {
					t.pendingRecheckSell = true
					log.Printf(
						"[TRACE] postonly.recheck side=%s set=true reason=timeout_or_error order_id=%s",
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

	if t.PendingReplacementRetry.Enabled {
		repl := t.PendingReplacementRetry.Replacement

		if t.pendingSell == nil {
			err := t.startCase3BReplacement(ctx, repl)
			if err != nil {
				log.Printf("[TRACE] case3B.retry.failed method=%s err=%v", repl.Method.String(), err)
			} else {
				log.Printf("[TRACE] case3B.retry.started method=%s", repl.Method.String())
				t.PendingReplacementRetry.Enabled = false
				_ = t.saveStateNoLock()
			}
		}
	}

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	_ = t.shouldRefit(len(execHistory)) // intentionally unused here (guard only)

	log.Printf("[TRACE] hotpath.after_drain elapsed_ms=%d",
		time.Since(hotStart).Milliseconds())

	// TODO: remove TRACE
	lsb := len(t.book(SideBuy).Lots)
	lss := len(t.book(SideSell).Lots)
	log.Printf("[TRACE] step.start ts=%s livePrice=%.8f candleClose=%.8f lotsBuy=%d lotsSell=%d lastAddBuy=%s lastAddSell=%s winLowBuy=%.8f winHighSell=%.8f latchedGateBuy=%.8f latchedGateSell=%.8f recentLow=%.8f recentHigh=%.8f elapsed_Hours_Buy=%.1f elapsed_Hours_Sell=%.1f",
		now.Format(time.RFC3339), livePrice, execHistory[len(execHistory)-1].Close, lsb, lss,
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
	// 	t.consolidateDust(t.book(SideBuy), price, minNotional)
	// 	t.consolidateDust(t.book(SideSell), price, minNotional)
	// 	t.archiveOrphanDust(t.book(SideBuy), price, minNotional)
	// 	t.archiveOrphanDust(t.book(SideSell), price, minNotional)
	// 	if err := t.saveStateNoLock(); err != nil {
	// 		log.Printf("[WARN] saveState (startup consolidate): %v", err)
	// 	}
	// 	t.didConsolidateStartup = true
	// 	log.Printf("[TRACE] consolidate.startup done px=%.8f minNotional=%.2f", price, minNotional)
	// }

	if msg, done, err := t.maybeCloseDustBasket(ctx, SideBuy, price); done || err != nil {
		t.mu.Unlock()
		return StepResult{Msg: msg}, err
	}

	if msg, done, err := t.maybeCloseDustBasket(ctx, SideSell, price); done || err != nil {
		t.mu.Unlock()
		return StepResult{Msg: msg}, err
	}

	log.Printf("[TRACE] hotpath.after_dust elapsed_ms=%d",
		time.Since(hotStart).Milliseconds())

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
			gateUSD := t.lotProfitGateUSD(lot)
			return net, net >= gateUSD
		}

		// Classify exit mode and refresh fee-aware Take preview.
		//
		// Runner:
		//   trailing activation + runner trail distance.
		//
		// ScalpFixedTP:
		//   fee-aware Take derived from this lot's ProfitGateUSD.
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
			gateUSD := t.lotProfitGateUSD(lot)
			lot.ExitMode = ExitModeScalpFixedTP
			lot.TrailDistancePct = 0
			lot.TrailActivateGateUSD = gateUSD
			lot.Take = activationPrice(lot, gateUSD, feeRatePct)
		}

		var stopL2 []exitCandidate
		var stopL1 []exitCandidate
		var profitL2 []exitCandidate
		var profitL1 []exitCandidate

		// Scan one side book and close at most one lot.
		//
		// Flow:
		// 1. Refresh exit mode and Take
		// 2. Compute fee-aware net PnL
		// 3. Update nearest snapshot
		// 4. Skip non-profitable lots
		// 5. No-longer Require AI/logic exit approval
		// 6. Trigger side-aware exit
		scanSide := func(side OrderSide) (string, bool, error) {

			book := t.book(side)
			lossLimit := -math.Abs(t.cfg.StopLossPnLUSD)
			enableStopLoss := t.cfg.EnableThresholdStopLoss

			deepLossMult := 1.4
			strongProfitMult := 1.4

			profitGivebackUSD := 0.15
			case4Exit := false
			protectedFloor := 0.0

			for i := 0; i < len(book.Lots); {
				lot := book.Lots[i]

				if lot.SizeBase*price < minNotional {
					lot.FixedTPWorking = false
					i++
					continue
				}

				// classify per spec
				setExitMode(book, i, lot)

				// compute gate

				net, pass := computeGate(lot)

				// gather nearest TAKE/mode/net while we're already here (no extra loops later)
				updateNearest(book, side, i, lot, net, price)

				gateUSD := t.lotProfitGateUSD(lot)

				// Case 4: once the lot reaches its profit gate, protect the gain.
				case4Exit = false
				protectedFloor = 0.0

				if lot.ExitMode == ExitModeScalpFixedTP {
					if net >= gateUSD {
						if !lot.ProfitTrailActive {
							lot.ProfitTrailActive = true
							lot.ProfitPeakUSD = net

							log.Printf(
								"[TRACE] case4.armed side=%s idx=%d entry_id=%s net=%.6f gate=%.6f",
								lot.Side,
								i,
								lot.EntryOrderID,
								net,
								gateUSD,
							)
						}

						if net > lot.ProfitPeakUSD {
							lot.ProfitPeakUSD = net
						}
					}

					if lot.ProfitTrailActive {
						protectedFloor = math.Max(
							gateUSD,
							lot.ProfitPeakUSD-profitGivebackUSD,
						)

						case4Exit = net > 0 && net < protectedFloor
					}
				}

				if case4Exit {
					// Exit as L2_PROFIT_PROTECTION.
					// Guaranteed still profitable at decision time.
					exitD := ExitDecision{
						Side:          lot.Side,
						MarketRegime:  t.MarketRegime,
						RegimeMult:    t.RegimeMultiplier,
						ExitReason:    "profit_protection",
						ExitClass:     "L2_PROFIT_PROTECTION",
						ExitNetPNLUSD: net,
					}

					// Set a maker-friendly exit price near the current market price.
					offBps := t.cfg.TPMakerOffsetBps
					makerExitPx := price

					if lot.Side == SideBuy && offBps > 0 {
						makerExitPx = price * (1.0 + offBps/10000.0)
					}
					if lot.Side == SideSell && offBps > 0 {
						makerExitPx = price * (1.0 - offBps/10000.0)
					}

					lot.Take = makerExitPx
					lot.FixedTPWorking = true

					cand := exitCandidate{
						side:         side,
						idx:          i,
						entryOrderID: lot.EntryOrderID,
						reason:       exitD.ExitReason,
						decision:     decisionExitReason(exitD),
						net:          net,
					}

					profitL2 = append(profitL2, cand)

					log.Printf(
						"[TRACE] case4.protection_exit side=%s idx=%d entry_id=%s net=%.6f gate=%.6f peak=%.6f floor=%.6f take=%.8f",
						lot.Side,
						i,
						lot.EntryOrderID,
						net,
						gateUSD,
						lot.ProfitPeakUSD,
						protectedFloor,
						lot.Take,
					)
					lot.ProfitTrailActive = false
					lot.ProfitPeakUSD = 0
					i++
					continue

				} else if lot.ProfitTrailActive && net <= 0 {
					// Protection was armed, but price moved through the protected positive
					// range before an exit could be scheduled. Do not classify this as profit.
					// Continue into the ordinary stop-loss path below.
					log.Printf(
						"[TRACE] case4.protection_missed side=%s idx=%d entry_id=%s net=%.6f gate=%.6f peak=%.6f floor=%.6f",
						lot.Side,
						i,
						lot.EntryOrderID,
						net,
						gateUSD,
						lot.ProfitPeakUSD,
						protectedFloor,
					)
				}

				strongProfitExit := net >= gateUSD*strongProfitMult

				if lot.ExitMode == ExitModeScalpFixedTP {
					pass = net >= gateUSD
					lot.TrailActivateGateUSD = gateUSD
				}

				// Must be profitable first
				// Profit gate must pass before any exit action.
				// If profit disappears, clear transient trailing/TP state.
				if !pass {
					if lot.ExitMode == ExitModeRunnerTrailing {
						lot.TrailActive = false
						lot.TrailPeak = 0
						lot.TrailStop = 0
						lot.FixedTPWorking = false
						i++
						continue
					}

					exitD := ExitDecision{
						Side:             lot.Side,
						MarketRegime:     t.MarketRegime,
						RegimeMult:       t.RegimeMultiplier,
						ExitReason:       "threshold_stop_loss",
						ExitNetPNLUSD:    net,
						StopLossPNLUSD:   t.cfg.StopLossPnLUSD,
						StopLossLimitUSD: lossLimit,
					}

					deepLossLimit := lossLimit * deepLossMult
					deepLossExit := net <= deepLossLimit

					// ============================================================================
					// CASE7 - Disable BUY threshold_stop_loss
					// Revert Case 7 by restoring:
					// if enableStopLoss && net <= lossLimit {
					// ============================================================================
					if enableStopLoss && lot.Side == SideSell && net <= lossLimit {
						exitD.ExitNetPNLUSD = net
						exitD.StopLossLimitUSD = lossLimit
						cand := exitCandidate{
							side:         side,
							idx:          i,
							entryOrderID: lot.EntryOrderID,
							reason:       "threshold_stop_loss",
							net:          net,
						}

						if deepLossExit {
							exitD.ExitClass = "L2_DEEP_LOSS"
							cand.decision = decisionExitReason(exitD)
							stopL2 = append(stopL2, cand)
						} else {
							exitD.ExitClass = "L1_THRESHOLD_WARNING"
							cand.decision = decisionExitReason(exitD)
							stopL1 = append(stopL1, cand)

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
								log.Printf("[TRACE] stop_l1.post side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
							} else {
								log.Printf("[TRACE] stop_l1.repost side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
							}
						}
						i++
						continue
					}

					i++
					continue
				}

				// Profit gate passed.
				// Apply exit-mode-specific behavior.
				switch lot.ExitMode {
				case ExitModeRunnerTrailing:
					exitD := ExitDecision{
						Side:          lot.Side,
						MarketRegime:  t.MarketRegime,
						RegimeMult:    t.RegimeMultiplier,
						ExitReason:    "trailing_stop",
						ExitClass:     "L1_TRAILING_STOP",
						ExitNetPNLUSD: net,
					}
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
						msg, err := t.closeLot(ctx, livePrice, side, i, "trailing_stop", decisionExitReason(exitD))
						if err != nil {
							return "", true, err
						}
						return msg, true, nil
					}

				case ExitModeScalpFixedTP:
					//-------flow reminder-----------------------------
					// ProfitGate passed
					// arm Take as maker-friendly limit
					// call closeLot()
					// closeLot tries post-only at Take
					// if not filled by timeout, fallback market
					//-------------------------------------------------------

					exitD := ExitDecision{
						Side:          lot.Side,
						MarketRegime:  t.MarketRegime,
						RegimeMult:    t.RegimeMultiplier,
						ExitReason:    "take_profit",
						ExitNetPNLUSD: net,
					}

					if strongProfitExit {
						exitD.ExitClass = "L2_STRONG_PROFIT"
					} else {
						exitD.ExitClass = "L1_PROFIT_GATE"
					}

					log.Printf(
						"[TRACE] exit.allow lot_side=%s idx=%d net=%.4f gate=%.6f "+
							"entry_id=%s livePrice=%.8f mode=%s exitReason=%s "+
							"strongProfit=%t exitClass=%s",
						lot.Side,
						i,
						net,
						gateUSD,
						lot.EntryOrderID,
						livePrice,
						lot.ExitMode,
						exitD.ExitReason,
						strongProfitExit,
						exitD.ExitClass,
					)

					offBps := t.cfg.TPMakerOffsetBps
					makerExitPx := price

					if lot.Side == SideBuy && offBps > 0 {
						makerExitPx = price * (1.0 + offBps/10000.0)
					}
					if lot.Side == SideSell && offBps > 0 {
						makerExitPx = price * (1.0 - offBps/10000.0)
					}

					if !lot.FixedTPWorking ||
						(lot.Side == SideBuy && makerExitPx < lot.Take) ||
						(lot.Side == SideSell && makerExitPx > lot.Take) {

						lot.Take = makerExitPx
						lot.FixedTPWorking = true

						log.Printf(
							"[TRACE] tp.post side=%s idx=%d price=%.8f net=%.6f entry_id=%s",
							lot.Side,
							i,
							lot.Take,
							net,
							lot.EntryOrderID,
						)
					} else {
						log.Printf(
							"[TRACE] tp.repost side=%s idx=%d price=%.8f net=%.6f entry_id=%s",
							lot.Side,
							i,
							lot.Take,
							net,
							lot.EntryOrderID,
						)
					}

					notional := lot.SizeBase * price
					if notional < minNotional {
						lot.FixedTPWorking = false
						i++
						continue
					}

					cand := exitCandidate{
						side:         side,
						idx:          i,
						entryOrderID: lot.EntryOrderID,
						reason:       exitD.ExitReason,
						decision:     decisionExitReason(exitD),
						net:          net,
					}

					if strongProfitExit {
						profitL2 = append(profitL2, cand)
					} else {
						profitL1 = append(profitL1, cand)
					}

					log.Printf(
						"[TRACE] tp.queue side=%s idx=%d price=%.8f net=%.6f "+
							"exit_class=%s entry_id=%s",
						lot.Side,
						i,
						lot.Take,
						net,
						exitD.ExitClass,
						lot.EntryOrderID,
					)

					i++
					continue

				}

				//--------------------information----------------------------
				// Take = post-only exit price
				// FixedTPWorking = maker exit armed
				// trigger = send maker exit attempt now
				//----------------------------------------------------------

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
			t.mu.Unlock()
			return StepResult{Msg: msg}, err
		}
		if msg, done, err := scanSide(SideSell); done || err != nil {
			t.mu.Unlock()
			return StepResult{Msg: msg}, err
		}

		// Build the fan-out set while preserving the existing selection policy:
		//
		// - all L2 deep losses
		// - all L2 strong profits
		// - worst L1 warning loss
		// - best L1 AI profit
		var selected []exitCandidate

		selected = append(selected, stopL2...)
		selected = append(selected, profitL2...)

		if len(stopL1) > 0 {
			sort.Slice(stopL1, func(i, j int) bool {
				// Most negative L1 loss first.
				return stopL1[i].net < stopL1[j].net
			})

			selected = append(selected, stopL1[0])
		}

		if len(profitL1) > 0 {
			sort.Slice(profitL1, func(i, j int) bool {
				// Highest L1 profit first.
				return profitL1[i].net > profitL1[j].net
			})

			selected = append(selected, profitL1[0])
		}

		if len(selected) > 0 {
			log.Printf(
				"[TRACE] exit.fanout.batch candidates=%d stop_l2=%d profit_l2=%d stop_l1=%d profit_l1=%d",
				len(selected),
				len(stopL2),
				len(profitL2),
				len(stopL1),
				len(profitL1),
			)

			// Workers acquire t.mu individually.
			// Never wait for them while step() still holds the trader lock.
			t.mu.Unlock()

			results := t.fanOutExits(
				ctx,
				livePrice,
				selected,
			)

			var (
				msgs      []string
				succeeded int
				failed    int
			)

			for _, res := range results {
				if res.Err != nil {
					failed++

					log.Printf(
						"[TRACE] exit.fanout.failed side=%s entry_id=%s reason=%s err=%v",
						res.Side,
						res.EntryOrderID,
						res.Reason,
						res.Err,
					)

					continue
				}

				succeeded++

				log.Printf(
					"[TRACE] exit.fanout.done side=%s entry_id=%s reason=%s msg=%q",
					res.Side,
					res.EntryOrderID,
					res.Reason,
					res.Msg,
				)

				if strings.TrimSpace(res.Msg) != "" {
					msgs = append(msgs, res.Msg)
				}
			}

			return StepResult{
				Msg: fmt.Sprintf(
					"EXIT-FANOUT total=%d succeeded=%d failed=%d\n%s",
					len(results),
					succeeded,
					failed,
					strings.Join(msgs, "\n"),
				),
			}, nil
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

	log.Printf("[TRACE] hotpath.after_exit_scan elapsed_ms=%d",
		time.Since(hotStart).Milliseconds())

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

	//-------------------------------------------------------------
	//2. Fan out only AI, MACD and EMA
	//-----------------------------------------------------------
	aiCh := make(chan AIResult, 1)
	macdSnapCh := make(chan MACDSnapshotResult, 1)
	emaCh := make(chan EMAPatternResult, 1)

	go func() {
		aiCh <- t.evaluateAI(signalHistory)
	}()

	go func() {
		macdSnapCh <- t.evaluateMACDSnapshot(execHistory)
	}()

	go func() {
		emaCh <- t.evaluateEMAPatternSnapshot(execHistory)
	}()

	// -------------------------------------------------------------
	// 3. Evaluate Pyramid on the main thread
	// While those three goroutines run:
	// -------------------------------------------------------------
	pyramidRaw :=
		t.evaluatePyramidRaw(
			livePrice,
			wallNow,
		)

	equityRaw :=
		t.evaluateEquityRaw()

	// ------------------------------------------------------
	// 4. Fan in the concurrent results
	// -------------------------------------------------------
	aiResult := <-aiCh
	macdSnapshot := <-macdSnapCh
	emaResult := <-emaCh

	// ----------------------------------------------------------
	// Validate them:
	// --------------------------------------------------------
	if aiResult.Err != nil {
		log.Printf(
			"[TRACE] case5.ai.failed elapsed_ms=%d err=%v",
			aiResult.Elapsed.Milliseconds(),
			aiResult.Err,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    Flat,
			Signal: Flat,
		}, nil
	}
	if macdSnapshot.Err != nil {
		log.Printf(
			"[TRACE] case5.macd.failed elapsed_ms=%d err=%v",
			macdSnapshot.Elapsed.Milliseconds(),
			macdSnapshot.Err,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    aiResult.Raw,
			Signal: Flat,
		}, nil
	}
	if emaResult.Err != nil {
		log.Printf(
			"[TRACE] case5.ema.failed elapsed_ms=%d err=%v",
			emaResult.Elapsed.Milliseconds(),
			emaResult.Err,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    aiResult.Raw,
			Signal: Flat,
		}, nil
	}
	if pyramidRaw.Err != nil {
		log.Printf(
			"[TRACE] case5.pyramid.failed elapsed_ms=%d err=%v",
			pyramidRaw.Elapsed.Milliseconds(),
			pyramidRaw.Err,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    aiResult.Raw,
			Signal: Flat,
		}, nil
	}

	// ----------------------------------------------------------
	// 5.0. Interpret MACD after AI arrives
	// -----------------------------------------------------------
	regimeMult := t.RegimeMultiplier
	if regimeMult <= 0 {
		regimeMult = 1.0
	}
	eps := computeLogicEPS(
		t.cfg.MACDLineEPS,
		aiResult.Raw,
		aiResult.Confidence,
		t.MarketRegime,
		regimeMult,
	)
	macdResult := interpretMACD(
		macdSnapshot,
		eps,
	)

	// ----------------------------------------------------------
	// 5.1. Interpret PyramidRaw after AI arrives
	// -----------------------------------------------------------
	pyramidResult := interpretPyramidRaw(
		pyramidRaw,
		aiResult.Confidence,
	)
	if pyramidResult.Err != nil {
		log.Printf(
			"[TRACE] case5.pyramid_interpret.failed elapsed_ms=%d err=%v",
			pyramidResult.Elapsed.Milliseconds(),
			pyramidResult.Err,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    aiResult.Raw,
			Signal: Flat,
		}, nil
	}
	// Only timer-extension maintenance from raw evaluation.
	t.applyPyramidRawTransitions(
		pyramidRaw.State,
	)

	// -----------------------------------------------------------------
	// Preserve the legacy AI + Logic decision used by the original
	// Pyramid state-maintenance block.
	//
	// Pyramid state transitions are committed using this legacy signal.
	// Case 5 then receives all raw/interpreted materials and may retain
	// or override that legacy signal when producing the final entry
	// decision.
	// -----------------------------------------------------------------
	normalBuy :=
		macdResult.StrongNegative &&
			macdResult.MomentumUp &&
			emaResult.PatternBuy

	normalSell :=
		macdResult.StrongPositive &&
			macdResult.MomentumDown &&
			emaResult.PatternSell

	logicOpinion := Flat

	if normalBuy {
		logicOpinion = Buy
	} else if normalSell {
		logicOpinion = Sell
	}

	legacySignal := finalSignalFromAILogic(
		aiResult.Raw,
		logicOpinion,
	)

	// Preserve the original selected-side Pyramid state behavior using
	// the decision that existed before the Case 5 override stage.
	t.applyPyramidDecisionTransitions(
		pyramidResult,
		legacySignal,
	)

	// -----------------------------------------------------------------
	// Case 5 Equity funding snapshot.
	//
	// The background refresher remains the sole balance-cache writer.
	// This block only reads the cache and derives both side-specific spare
	// materials before Equity interpretation.
	// -----------------------------------------------------------------
	var (
		symQ       string
		availQuote float64
		quoteStep  float64

		symB      string
		availBase float64
		baseStep  float64

		spareQuote float64
		spareBase  float64
	)

	if legacySignal == Buy || legacySignal == Sell {
		var cacheOK bool

		log.Printf(
			"[TRACE] hotpath.before_balance elapsed_ms=%d legacy=%s",
			time.Since(hotStart).Milliseconds(),
			legacySignal,
		)

		snapshot, cacheOK :=
			t.getBalanceSnapshot(
				balanceSnapshotMaxAge,
			)

		if !cacheOK {
			ageMS := int64(-1)
			if !snapshot.UpdatedAt.IsZero() {
				ageMS =
					time.Since(
						snapshot.UpdatedAt,
					).Milliseconds()
			}

			log.Printf(
				"[WARN] balance.cache.unavailable legacy=%s age_ms=%d",
				legacySignal,
				ageMS,
			)

			t.mu.Unlock()

			return StepResult{
				Msg:    "HOLD balance cache unavailable or stale",
				Raw:    aiResult.Raw,
				Signal: Flat,
			}, nil
		}

		symQ = snapshot.SymQuote
		availQuote = snapshot.AvailQuote
		quoteStep = snapshot.QuoteStep

		symB = snapshot.SymBase
		availBase = snapshot.AvailBase
		baseStep = snapshot.BaseStep

		log.Printf(
			"[TRACE] balance.cache.hit legacy=%s age_ms=%d quote=%.8f base=%.8f",
			legacySignal,
			time.Since(snapshot.UpdatedAt).Milliseconds(),
			availQuote,
			availBase,
		)

		switch legacySignal {
		case Buy:
			if strings.TrimSpace(symQ) == "" ||
				quoteStep <= 0 {

				log.Printf(
					"[WARN] balance.cache.invalid_quote symbol=%q step=%.8f",
					symQ,
					quoteStep,
				)

				t.mu.Unlock()

				return StepResult{
					Msg:    "HOLD invalid cached quote metadata",
					Raw:    aiResult.Raw,
					Signal: Flat,
				}, nil
			}

		case Sell:
			if strings.TrimSpace(symB) == "" ||
				baseStep <= 0 {

				log.Printf(
					"[WARN] balance.cache.invalid_base symbol=%q step=%.8f",
					symB,
					baseStep,
				)

				t.mu.Unlock()

				return StepResult{
					Msg:    "HOLD invalid cached base metadata",
					Raw:    aiResult.Raw,
					Signal: Flat,
				}, nil
			}
		}

		spareQuote =
			availQuote -
				reservedShortQuoteWithFee

		spareBase =
			availBase -
				reservedLongBase
	}

	equityResult := interpretEquityRaw(
		equityRaw,
		legacySignal,
		spareQuote,
		spareBase,
		quoteStep,
		baseStep,
	)

	log.Printf(
		"[TRACE] case5.equity raw_ms=%d interpret_ms=%d legacy=%s "+
			"buy_pass=%t sell_pass=%t buy_trigger=%t sell_trigger=%t "+
			"buy_quote=%.8f sell_base=%.8f reason=%s",
		equityRaw.Elapsed.Milliseconds(),
		equityResult.Elapsed.Milliseconds(),
		legacySignal,
		equityRaw.BuyThresholdPassed,
		equityRaw.SellThresholdPassed,
		equityResult.BuyTrigger,
		equityResult.SellTrigger,
		equityResult.ProposedBuyQuote,
		equityResult.ProposedSellBase,
		equityResult.Reason,
	)

	// Case 5 may retain or override the legacy AI + Logic decision using
	// the complete AI, MACD, EMA and Pyramid materials.
	entryDecision := t.combineEntryRawMaterials(
		aiResult,
		macdResult,
		emaResult,
		pyramidResult,
		equityResult,
		legacySignal,
		logicOpinion,
	)

	log.Printf(
		"[TRACE] hotpath.after_decision elapsed_ms=%d",
		time.Since(hotStart).Milliseconds(),
	)

	// 8. Copy Pyramid audit fields for the selected side
	var selectedPyramid PyramidSideResult

	switch entryDecision.Signal {
	case Buy:
		selectedPyramid = pyramidResult.Buy

	case Sell:
		selectedPyramid = pyramidResult.Sell
	}

	// Restore the existing reason values:

	reasonGatePrice := selectedPyramid.EffectiveGatePrice
	reasonLatched := selectedPyramid.Latched
	reasonEffPct := selectedPyramid.EffPct
	reasonBasePct := selectedPyramid.BasePct
	reasonElapsedHr := selectedPyramid.ElapsedHr
	reasonTFloorHr := selectedPyramid.TFloorHr

	log.Printf(
		"[TRACE] case5.fanin "+
			"ai_ms=%d macd_ms=%d ema_ms=%d pyramid_ms=%d equity_ms=%d "+
			"aiRaw=%s macd=%s ema=%s logic=%s legacy=%s "+
			"pyrBuy{spacing=%t adverse=%t gate=%t} "+
			"pyrSell{spacing=%t adverse=%t gate=%t} "+
			"eqBuy=%t eqSell=%t source=%s final=%s",
		aiResult.Elapsed.Milliseconds(),
		macdSnapshot.Elapsed.Milliseconds(),
		emaResult.Elapsed.Milliseconds(),
		pyramidRaw.Elapsed.Milliseconds(),
		equityResult.Elapsed.Milliseconds(),
		aiResult.Raw,
		macdResult.Opinion,
		emaResult.Opinion,
		logicOpinion,
		legacySignal,
		pyramidResult.Buy.SpacingPass,
		pyramidResult.Buy.AdversePass,
		pyramidResult.Buy.GatePassed,
		pyramidResult.Sell.SpacingPass,
		pyramidResult.Sell.AdversePass,
		pyramidResult.Sell.GatePassed,
		equityResult.BuyTrigger,
		equityResult.SellTrigger,
		entryDecision.DecisionSource,
		entryDecision.Signal,
	)

	d := entryDecision

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
		t.mu.Unlock()
		return StepResult{Msg: "HOLD pending BUY cancel requested: signal changed", Raw: d.Raw, Signal: d.Signal}, nil
	}

	if t.pendingSell != nil && d.Signal != Sell {
		orderID := t.pendingSell.OrderID
		t.pendingSell.CancelRequested = true
		t.pendingRecheckSell = false

		t.mu.Unlock()
		_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, orderID)
		t.mu.Lock()

		_ = t.saveStateNoLock()
		t.mu.Unlock()
		return StepResult{Msg: "HOLD pending SELL cancel requested: signal changed", Raw: d.Raw, Signal: d.Signal}, nil
	}

	// --------------------------------------------------------------------------------------------------------
	//---ADD path continues-----
	// --------------------------------------------------------------------------------------------------------

	totalLots := lsb + lss

	log.Printf(
		"[DEBUG] Total Lots=%d Raw=%s Decision=%s price=%.8f %s LongOnly=%v ver=%d",
		totalLots,
		d.Raw,
		d.Signal,
		price,
		decisionEntryReason(d),
		t.cfg.LongOnly,
		Version,
	)

	// Determine the side and its book
	side, ok := d.SignalToSide()
	if !ok {
		log.Printf("[TRACE] signal.no_side signal=%s raw=%s final=%s", d.Signal, d.Raw, d.Signal)
		t.mu.Unlock()
		return StepResult{Msg: "FLAT", Raw: d.Raw, Signal: d.Signal}, nil
	}

	book := t.book(side)

	// Case 5 already evaluated Equity thresholds, direction, spare funding,
	// exchange-step snapping, and proposed triggers.
	equityTriggerBuy :=
		equityResult.BuyTrigger

	equityTriggerSell :=
		equityResult.SellTrigger

	equitySpareQuote :=
		equityResult.ProposedBuyQuote

	equitySpareBase :=
		equityResult.ProposedSellBase

	// Preserve the execution-stage side-specific spare variable expected by the
	// existing sizing and order-placement pipeline.
	spare := 0.0

	switch side {
	case SideBuy:
		spare = equityResult.SpareQuote

	case SideSell:
		spare = equityResult.SpareBase
	}

	// Existing execution classification.
	isAdd :=
		len(book.Lots) > 0 &&
			t.cfg.AllowPyramiding &&
			(d.Signal == Buy || d.Signal == Sell)

	skipPyramidGates :=
		equityTriggerSell ||
			equityTriggerBuy

	// Prevent duplicate opens while pending on this side (exits already ran) ---
	// Extra belt-and-suspenders: if a pending exists and we haven't hit its Deadline, keep waiting.
	if side == SideBuy && t.pendingBuy != nil && time.Now().Before(t.pendingBuy.Deadline) {
		t.mu.Unlock()
		return StepResult{Msg: "OPEN-PENDING side=BUY", Raw: d.Raw, Signal: d.Signal}, nil
	}
	if side == SideSell && t.pendingSell != nil && time.Now().Before(t.pendingSell.Deadline) {
		t.mu.Unlock()
		return StepResult{Msg: "OPEN-PENDING side=SELL", Raw: d.Raw, Signal: d.Signal}, nil
	}

	// -----------------------------------------------------------------------------
	// Case 3A-Opposite - DOWN-Regime BUY Protection
	//
	// If the immediately previous exit was a BUY threshold-stop loss,
	// block any new BUY entry above that loss-exit SELL price while the
	// market regime remains DOWN.
	// -----------------------------------------------------------------------------
	if side == SideBuy &&
		t.MarketRegime == RegimeDown &&
		len(t.lastExits) > 0 {

		last := t.lastExits[len(t.lastExits)-1]

		if last.Side == SideBuy &&
			strings.HasPrefix(last.Reason, "threshold_stop_loss") &&
			last.PNLUSD < 0 &&
			price > last.ClosePrice {

			log.Printf(
				"[TRACE] case3A.block_buy regime=%s buy_price=%.8f last_exit_sell_price=%.8f last_exit_net=%.6f",
				t.MarketRegime,
				price,
				last.ClosePrice,
				last.PNLUSD,
			)

			t.mu.Unlock()
			return StepResult{Msg: "HOLD case3A block BUY above last loss-exit SELL price", Raw: d.Raw, Signal: d.Signal}, nil
		}
	}
	// -----------------------------------------------------------------------------
	// Case 3A - UP-Regime SELL Protection
	//
	// If the immediately previous exit was a SELL threshold-stop loss,
	// block any new SELL entry below that loss-exit BUY price while the
	// market regime remains UP.
	// -----------------------------------------------------------------------------
	if side == SideSell &&
		t.MarketRegime == RegimeUp &&
		len(t.lastExits) > 0 {

		last := t.lastExits[len(t.lastExits)-1]

		if last.Side == SideSell &&
			strings.HasPrefix(last.Reason, "threshold_stop_loss") &&
			last.PNLUSD < 0 &&
			price < last.ClosePrice {

			log.Printf(
				"[TRACE] case3A.block_sell regime=%s sell_price=%.8f last_exit_buy_price=%.8f last_exit_net=%.6f",
				t.MarketRegime,
				price,
				last.ClosePrice,
				last.PNLUSD,
			)

			t.mu.Unlock()
			return StepResult{Msg: "HOLD case3A block SELL below last loss-exit BUY price", Raw: d.Raw, Signal: d.Signal}, nil
		}
	}

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return StepResult{Msg: fmt.Sprintf("FLAT (long-only) [%s]", decisionEntryReason(d)), Raw: d.Raw, Signal: d.Signal}, nil
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
			t.archiveOrphanDust(t.book(SideBuy), price, minNotional)
			t.archiveOrphanDust(t.book(SideSell), price, minNotional)

			if err := t.saveStateNoLock(); err != nil {
				log.Printf("[WARN] saveState (startup consolidate): %v", err)
			}
			t.didConsolidateStartup = true
			log.Printf("[TRACE] consolidate.startup done px=%.8f minNotional=%.2f", price, minNotional)
		}
		t.mu.Unlock()
		log.Printf("[DEBUG] GATE1 lot cap reached (%d); HOLD", t.cfg.MaxConcurrentLots)
		return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
	}

	if isAdd && !skipPyramidGates {
		var pyramidSide PyramidSideResult

		switch legacySignal {
		case Buy:
			pyramidSide = pyramidResult.Buy

		case Sell:
			pyramidSide = pyramidResult.Sell
		}

		if legacySignal == Buy || legacySignal == Sell {
			log.Printf(
				"[TRACE] pyramid.spacing since_last=%.1fs need>=%ds",
				pyramidSide.ElapsedSec,
				pyramidSide.Raw.SpacingNeed,
			)

			if !pyramidSide.SpacingPass {
				log.Printf(
					"[DEBUG] GATE2 pyramid: blocked by spacing; since_last=%vHours need>=%ds",
					fmt.Sprintf("%.1f", pyramidSide.ElapsedHr),
					pyramidSide.Raw.SpacingNeed,
				)
			}

			if pyramidSide.SpacingPass {
				if pyramidSide.Raw.DecayLambda > 0 {
					log.Printf(
						"[TRACE] pyramid.conf_gate side=%s confidence=%.4f gateMult=%.4f decayedPct=%.4f effPct=%.4f",
						pyramidSide.Side,
						pyramidSide.Confidence,
						pyramidSide.GateMult,
						pyramidSide.DecayedPct,
						pyramidSide.EffPct,
					)
				}

				log.Printf(
					"[TRACE] pyramid.adverse side=%s lastAddAgoMin=%.2f basePct=%.4f effPct=%.4f lambda=%.5f floor=%.4f tFloorMin=%.2f",
					pyramidSide.Side,
					pyramidSide.ElapsedMin,
					pyramidSide.BasePct,
					pyramidSide.EffPct,
					pyramidSide.Raw.DecayLambda,
					pyramidSide.Raw.DecayFloor,
					pyramidSide.TFloorMin,
				)

				if pyramidSide.UsedSoftGate {
					switch legacySignal {
					case Buy:
						log.Printf(
							"[DEBUG] SOFT GATE BUY: elapsedMin=%.1f tFloorMin=%.2f old_gate=%.2f recentLow=%.2f soft_gate=%.2f winLow=%.2f price=%.2f",
							pyramidSide.ElapsedMin,
							pyramidSide.TFloorMin,
							pyramidSide.BaselineGatePrice,
							pyramidSide.Raw.RecentExtreme,
							pyramidSide.SoftGatePrice,
							pyramidSide.WinExtreme,
							pyramidSide.CurrentPrice,
						)

					case Sell:
						log.Printf(
							"[DEBUG] SOFT GATE SELL: elapsedMin=%.1f tFloorMin=%.2f old_gate=%.2f recentHigh=%.2f soft_gate=%.2f winHigh=%.2f price=%.2f",
							pyramidSide.ElapsedMin,
							pyramidSide.TFloorMin,
							pyramidSide.BaselineGatePrice,
							pyramidSide.Raw.RecentExtreme,
							pyramidSide.SoftGatePrice,
							pyramidSide.WinExtreme,
							pyramidSide.CurrentPrice,
						)
					}
				}

				if pyramidSide.GatePassed {
					switch legacySignal {
					case Buy:
						log.Printf(
							"[DEBUG] pyramid: BUY baseline met price=%.2f gatePrice=%.2f last=%.2f eff_pct=%.3f elapsedMin=%.1f",
							pyramidSide.CurrentPrice,
							pyramidSide.EffectiveGatePrice,
							pyramidSide.LastAnchor,
							pyramidSide.EffPct,
							pyramidSide.ElapsedMin,
						)

					case Sell:
						log.Printf(
							"[DEBUG] pyramid: SELL baseline met price=%.2f gatePrice=%.2f last=%.2f eff_pct=%.3f elapsedMin=%.1f",
							pyramidSide.CurrentPrice,
							pyramidSide.EffectiveGatePrice,
							pyramidSide.LastAnchor,
							pyramidSide.EffPct,
							pyramidSide.ElapsedMin,
						)
					}
				} else {
					switch legacySignal {
					case Buy:
						log.Printf(
							"[DEBUG] pyramid: blocked by last gate (BUY): price=%.2f gatePrice=%.2f",
							pyramidSide.CurrentPrice,
							pyramidSide.EffectiveGatePrice,
						)

						log.Printf(
							"[TRACE] pyramid.block.buy price=%.8f gate=%.8f last=%.8f effPct=%.4f",
							pyramidSide.CurrentPrice,
							pyramidSide.EffectiveGatePrice,
							pyramidSide.LastAnchor,
							pyramidSide.EffPct,
						)

					case Sell:
						log.Printf(
							"[DEBUG] pyramid: blocked by last gate (SELL): price=%.2f gatePrice=%.2f",
							pyramidSide.CurrentPrice,
							pyramidSide.EffectiveGatePrice,
						)

						log.Printf(
							"[TRACE] pyramid.block.sell price=%.8f gate=%.8f last=%.8f effPct=%.4f",
							pyramidSide.CurrentPrice,
							pyramidSide.EffectiveGatePrice,
							pyramidSide.LastAnchor,
							pyramidSide.EffPct,
						)
					}
				}
			}
		}
	}

	log.Printf("[TRACE] hotpath.before_sizing elapsed_ms=%d",
		time.Since(hotStart).Milliseconds())

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
		f := volRiskFactor(execHistory)
		if f <= 0 {
			f = 1.0
		}
		quote = quote * f
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
		t.mu.Unlock()
		return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
	}

	entryAIMode := "AI_MATCH"
	if d.Raw == Flat {
		entryAIMode = "AI_FLAT"
	}

	entryProfitGateUSD := t.cfg.ProfitGateUSD * confMult
	if entryProfitGateUSD < 0.30 {
		entryProfitGateUSD = 0.30
	}

	recoveryAddUSD := t.recoveryTargetAddUSD()
	entryProfitGateUSD += recoveryAddUSD

	log.Printf(
		"[TRACE] recovery.entry debt=%.4f add=%.4f targetNetUSD=%.4f",
		t.RecoveryDebtUSD,
		recoveryAddUSD,
		entryProfitGateUSD,
	)

	//Applying confidence multiplier to scalp, that of equity comes later
	if !(equityTriggerSell || equityTriggerBuy) {
		oldQuote := quote
		quote *= confMult
		log.Printf(
			"[TRACE] sizing.confidence side=%s pUp=%.5f mult=%.2f quote_before=%.2f quote_after=%.2f",
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
				"[TRACE] sizing.equity.confidence side=%s pUp=%.5f mult=%.2f size_before=%.2f size_after=%.2f",
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
				"[TRACE] sizing.equity.confidence side=%s pUp=%.5f mult=%.2f quote_before=%.2f quote_after=%.2f",
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
	log.Printf("[TRACE] sizing.pre side=%s eq=%.2f quote=%.2f price=%.8f base=%.8f", side, t.equityUSD, quote, price, base)

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// -----------------------------------------------------------------------------------------------
	// --- Spare and Reservation Inventory ---
	// -----------------------------------------------------------------------------------------------
	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// TODO: remove TRACE
		log.Printf("[TRACE] buy.gate.pre availQuote=%.2f reservedShort=%.2f needQuoteRaw=%.2f quoteStep=%.8f",
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
					log.Printf("[TRACE] buy.gate.block minNotional need=%.2f spare=%.2f", neededQuote, spare)

					short := neededQuote - spare
					if short > 0 {
						// remember that a BUY was blocked by this amount
						t.refundBuyUSD = short
					}
					t.mu.Unlock()
					return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
				}
			}

			// Use the final neededQuote; recompute base.
			quote = neededQuote
			base = quote / price

			log.Printf("[TRACE] buy.gate.post needQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
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
				t.mu.Unlock()
				return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
			}

			// ok, we can place a smaller order using the spare
			quote = useQuote
			base = quote / price

			log.Printf("[TRACE] buy.gate.post.degraded useQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
		}
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && t.cfg.RequireBaseForShort {
		// TODO: remove TRACE
		log.Printf("[TRACE] sell.gate.pre availBase=%.8f reservedLong=%.8f needBaseRaw=%.8f baseStep=%.8f",
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
					log.Printf("[TRACE] sell.gate.block minNotional need=%.8f spare=%.8f", base, spare)

					// convert the short to USD at current price so we can reuse later on BUY
					shortBase := base - spare
					shortUSD := shortBase * price
					if shortUSD > 0 {
						t.refundSellUSD = shortUSD
					}
					t.mu.Unlock()
					return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
				}
			}

			log.Printf("[TRACE] sell.gate.post needBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
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
				t.mu.Unlock()
				return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
			}

			// ok, we can place a smaller order using the spare
			base = useBase
			quote = base * price

			log.Printf("[TRACE] sell.gate.post.degraded useBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
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

	// --- side-biased Lot reason ---
	var gatesReason string

	if equityTriggerSell && side == SideSell && equitySpareBase > 0 {
		gatesReason = fmt.Sprintf(
			"equityTrading=true|equityUSD=%.2f|lastAddEquity=%.2f|sellEquityMultiplier=%.6f|sellEquityTriggerMult=%.2f|equitySpareBase=%.8f|confidenceMult=%.2f",
			t.equityUSD,
			t.lastAddEquity,
			t.equityUSD/t.lastAddEquity,
			t.cfg.SellEquityTriggerMult,
			equitySpareBase,
			confMult,
		)
	} else if equityTriggerBuy && side == SideBuy && equitySpareQuote > 0 {
		gatesReason = fmt.Sprintf(
			"equityTrading=true|equityUSD=%.2f|lastAddEquity=%.2f|buyEquityMultiplier=%.6f|buyEquityTriggerMult=%.2f|equitySpareQuote=%.2f|confidenceMult=%.2f",
			t.equityUSD,
			t.lastAddEquity,
			t.equityUSD/t.lastAddEquity,
			t.cfg.BuyEquityTriggerMult,
			equitySpareQuote,
			confMult,
		)
	} else {
		gatesReason = fmt.Sprintf(
			"gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|latchTargetHr=%.2f|targetNetUSD=%.2f",
			reasonGatePrice,
			reasonLatched,
			reasonEffPct,
			reasonBasePct,
			reasonElapsedHr,
			2.0*reasonTFloorHr,
			entryProfitGateUSD,
		)
	}

	gatesReason = appendReason(gatesReason, decisionEntryReason(d))

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
		log.Printf("[TRACE] refund.block side=%s conf=%.2f need>=%.2f refundBuyUSD=%.2f",
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
		log.Printf("[TRACE] refund.block side=%s conf=%.2f need>=%.2f refundSellUSD=%.2f",
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
		log.Printf("[TRACE] postonly.skip reason=recheck_market_next_tick side=%s", side)
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

		// before order submit
		log.Printf("[TRACE] hotpath.before_submit elapsed_ms=%d side=%s limit=%.2f live=%.2f",
			time.Since(hotStart).Milliseconds(), side, limitPx, price)

		if baseAtLimit > 0 && baseAtLimit*limitPx >= minNotional {
			log.Printf("[TRACE] postonly.place side=%s limit=%.8f baseReq=%.8f timeout_sec=%d", side, limitPx, baseAtLimit, limitWait)
			orderID, err := t.broker.PlaceLimitPostOnly(ctx, t.cfg.ProductID, side, limitPx, baseAtLimit)
			if err == nil && strings.TrimSpace(orderID) != "" {
				log.Printf("[TRACE] hotpath.order.done elapsed_ms=%d orderID=%s",
					time.Since(hotStart).Milliseconds(),
					orderID)
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
						log.Printf("[TRACE] postonly.pending.set side=%s order_id=%s limit=%.8f base=%.8f quote=%.2f dl=%s eqFlags[buy=%v sell=%v]",
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
						log.Printf("[TRACE] postonly.pending.set side=%s order_id=%s limit=%.8f base=%.8f quote=%.2f dl=%s eqFlags[buy=%v sell=%v]",
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
					log.Printf("[TRACE] postonly.poll.start side=%s init_id=%s init_limit=%.8f init_base=%.8f deadline=%s offset_bps=%.3f",
						side, initOrderID, initLimitPx, initBaseAtLimit, deadline.Format(time.RFC3339), offsetBps)
					defer func() { log.Printf("[TRACE] postonly.poll.stopped side=%s initial_id=%s", side, initOrderID) }()

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
							log.Printf("[TRACE] postonly.poll.cancelled side=%s last_id=%s", side, orderID)
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

							log.Printf("[TRACE] postonly.poll.tick side=%s order_id=%s status=%s price=%.8f base=%.8f quote=%.2f fee=%.6f sess_agg[base=%.8f quote=%.2f fee=%.6f] reprices=%d",
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
								log.Printf("[TRACE] postonly.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f", orderID, ord.Price, ord.BaseSize, ord.QuoteSpent, ord.CommissionUSD)
								log.Printf("[TRACE] postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
									side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
								log.Printf("[KPI] maker.open.filled side=%s vwap=%.8f base=%.8f quote=%.2f fee=%.6f order_id=%s",
									side, placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD, orderID)

								safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
								return

							case "PARTIALLY_FILLED":
								if t.pendingCancelRequested(side) {
									log.Printf("[TRACE] postonly.reprice.skip.cancel_requested side=%s order_id=%s", side, orderID)
									lastReprice = time.Now()
									break
								}
								// still open; allow reprice path (throttled)
								if time.Since(lastReprice) >= time.Duration(t.cfg.RepriceIntervalMs)*time.Millisecond {
									log.Printf("[TRACE] postonly.reprice.try.partially side=%s order_id=%s last_limit=%.8f reprice_count=%d",
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
										log.Printf("[TRACE] postonly.reprice.swap.partially side=%s old_id=%s new_id=%s new_limit=%.8f count=%d",
											side, orderID, newID, newLastLimitPx, newRepriceCount)
										// switched to a new order: reset per-order deltas (session aggregates stay)
										orderID = newID
										lastLimitPx = newLastLimitPx
										repriceCount = newRepriceCount
										lastSeenBase, lastSeenQuote, lastSeenFee = 0, 0, 0
									} else {
										log.Printf("[TRACE] postonly.reprice.skip.partially side=%s order_id=%s reason=no_guard_or_no_improve last_limit=%.8f count=%d",
											side, orderID, lastLimitPx, repriceCount)
										lastLimitPx = newLastLimitPx
										repriceCount = newRepriceCount
									}
									lastReprice = time.Now()
								}

							case "NEW", "PENDING_CANCEL":
								if t.pendingCancelRequested(side) {
									log.Printf("[TRACE] postonly.reprice.skip.cancel_requested side=%s order_id=%s", side, orderID)
									lastReprice = time.Now()
									break
								}
								// resting or in cancel transition; keep polling
								if time.Since(lastReprice) >= time.Duration(t.cfg.RepriceIntervalMs)*time.Millisecond {
									log.Printf("[TRACE] postonly.reprice.try.new side=%s order_id=%s last_limit=%.8f reprice_count=%d",
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
										log.Printf("[TRACE] postonly.reprice.swap.new side=%s old_id=%s new_id=%s new_limit=%.8f count=%d",
											side, orderID, newID, newLastLimitPx, newRepriceCount)
										orderID = newID
										lastLimitPx = newLastLimitPx
										repriceCount = newRepriceCount
										lastSeenBase, lastSeenQuote, lastSeenFee = 0, 0, 0
									} else {
										log.Printf("[TRACE] postonly.reprice.skip.new side=%s order_id=%s reason=no_guard_or_no_improve last_limit=%.8f count=%d",
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

									log.Printf("[TRACE] postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
										side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
									safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
								} else {
									log.Printf("[TRACE] postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
										side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
									safeSend(ch, OpenResult{Filled: false, Placed: nil, OrderID: orderID})
								}

								log.Printf("[TRACE] postonly.poll.done side=%s order_id=%s final=%s vwap=%.8f base=%.8f quote=%.2f fee=%.6f",
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
							log.Printf("[TRACE] postonly.poll.cancelled side=%s last_id=%s", side, orderID)
							break poll
						case <-time.After(200 * time.Millisecond):
						}
					}

					// On timeout or cancellation, cancel any resting order if still open.
					_ = t.broker.CancelOrder(pctx, t.cfg.ProductID, orderID)
					log.Printf("[TRACE] postonly.poll.timeout side=%s last_id=%s sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
						side, orderID, sessBase, sessQuote, sessFee)
					log.Printf("[TRACE] postonly.timeout order_id=%s", orderID)

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
						log.Printf("[TRACE] postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
							side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
						safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
					} else {
						log.Printf("[TRACE] postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
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
				t.mu.Unlock()
				return StepResult{Msg: fmt.Sprintf("OPEN-PENDING side=%s", side), Raw: d.Raw, Signal: d.Signal}, nil
			} else if err != nil {
				log.Printf("[TRACE] postonly.error hold_for_recheck err=%v", err)
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
			log.Printf("[TRACE] postonly.market_fallback.blocked side=%s reason=recheck_flag_not_set", side)
			return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
		}

		// before order submit
		log.Printf("[TRACE] hotpath.before_submit.market_quote elapsed_ms=%d side=%s live=%.2f",
			time.Since(hotStart).Milliseconds(), side, price)

		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
		// TODO: remove TRACE
		log.Printf("[TRACE] order.open request side=%s quote=%.2f baseEst=%.8f priceSnap=%.8f take=%.8f",
			side, quote, base, price, take)
		log.Printf("[TRACE] postonly.market_fallback.go side=%s quote=%.2f", side, quote)
		log.Printf("[KPI] taker.open side=%s quote=%.2f reason=market_fallback", side, quote)
		log.Printf("[TRACE] hotpath.order.done elapsed_ms=%d",
			time.Since(hotStart).Milliseconds())

		if err != nil {
			// Retry once with ORDER_MIN_USD on insufficient-funds style failures.
			e := strings.ToLower(err.Error())
			if quote > minNotional && (strings.Contains(e, "insufficient") || strings.Contains(e, "funds") || strings.Contains(e, "400")) {
				log.Printf("[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f", quote, err, minNotional)
				quote = minNotional
				base = quote / price
				// TODO: remove TRACE
				log.Printf("[TRACE] order.open retry side=%s quote=%.2f baseEst=%.8f", side, quote, base)
				placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
			}
			if err != nil {
				if t.cfg.UseDirectSlack {
					postSlack(fmt.Sprintf("ERR step: %v", err))
				}
				return StepResult{Msg: "", Raw: d.Raw, Signal: d.Signal}, err
			}
		}
		// TODO: remove TRACE
		if placed != nil {
			log.Printf("[TRACE] order.open placed price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
				placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
		}

	}

	// Re-lock to mutate state (append new lot to THIS SIDE).
	t.mu.Lock()

	// --- NEW (Phase 2): reset recheck flag after successful market fallback open ---

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
			log.Printf("[TRACE] fill.open partial requested=%.8f filled=%.8f", baseRequested, baseToUse)
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

	// already deducted above for DryRun using quote; adjust to the actualQuote delta
	delta := (actualQuote - quote) * (feeRate / 100.0)
	t.equityUSD -= delta

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
		Version:          Version,
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
	t.archiveOrphanDust(book, priceToUse, minNotional)
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

	old := t.lastAddEquity
	t.lastAddEquity = t.equityUSD
	log.Printf(
		"[TRACE] equity.baseline.set side=%s old=%.2f new=%.2f",
		side,
		old,
		t.lastAddEquity,
	)

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
		log.Printf("[TRACE] runner.assign idx=%d side=%s open=%.8f take=%.8f", newIdx, side, r.OpenPrice, r.Take)
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
		log.Printf("[TRACE] runner.assign idx=%d side=%s open=%.8f take=%.8f", newIdx, side, r.OpenPrice, r.Take)
	}
	// (If not equityTriggerSell/equityTriggerBuy, leave RunnerIDs unchanged so first lot is NOT the runner.)

	msg := ""
	msg = fmt.Sprintf("[LIVE ORDER] %s notional=%.2f take=%.2f fee=%.4f reason=%s",
		side, newLot.OpenNotionalUSD, newLot.Take, entryFee, newLot.Reason)

	if t.cfg.UseDirectSlack {
		postSlack(msg)
	}
	// persist new state (no locking while writing; snapshot constructed here under lock)
	if err := t.saveStateNoLock(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		log.Printf("[TRACE] state.save error=%v", err)
	}
	log.Printf("[KPI] summary equity=%.2f daily_pnl=%.2f lots_buy=%d lots_sell=%d product=%s",
		t.equityUSD, t.dailyPnL, len(t.book(SideBuy).Lots), len(t.book(SideSell).Lots), t.cfg.ProductID)
	t.mu.Unlock()
	return StepResult{Msg: msg, Raw: d.Raw, Signal: d.Signal}, nil
}

type exitCandidate struct {
	side         OrderSide
	idx          int
	entryOrderID string
	reason       string
	decision     string
	net          float64
}

// consolidateDust collapses tiny (notional < minNotional) lots on a side.
// Behavior:
// - If there is exactly 1 lot leave it as is.
// - If there are 2+ lots →
//  1. collapse tail dust backward,
//  2. sweep older dust forward into newest,
//
// RunnerIDs are kept authoritative.
func (t *Trader) consolidateDust(book *SideBook, px float64, minNotional float64) {
	// 0 lots: nothing to do
	if len(book.Lots) == 0 {
		return
	}

	// 1 lot at start: pad and stop
	if len(book.Lots) == 1 {
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
		a.Reason = strings.TrimSpace(a.Reason + "|merge:" + b.EntryOrderID)

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
}
func (t *Trader) archiveOrphanDust(book *SideBook, px float64, minNotional float64) {
	if book == nil || len(book.Lots) != 1 || px <= 0 || minNotional <= 0 {
		return
	}

	lot := book.Lots[0]
	if lot == nil || lot.SizeBase*px >= minNotional {
		return
	}

	side := lot.Side
	wallNow := time.Now().UTC()

	if side == SideBuy {
		t.dustBuyLots = append(t.dustBuyLots, lot)
		t.lastAddBuy = wallNow
		t.winLowBuy = 0
		t.latchedGateBuy = 0
		t.equityStageBuy = 0
	} else if side == SideSell {
		t.dustSellLots = append(t.dustSellLots, lot)
		t.lastAddSell = wallNow
		t.winHighSell = 0
		t.latchedGateSell = 0
		t.equityStageSell = 0
	} else {
		return
	}

	book.Lots = nil
	book.RunnerIDs = nil

	log.Printf(
		"[TRACE] dust.archive side=%s open=%.8f base=%.8f notional=%.4f minNotional=%.2f lastAddReset=%s",
		side,
		lot.OpenPrice,
		lot.SizeBase,
		lot.SizeBase*px,
		minNotional,
		wallNow.Format(time.RFC3339),
	)
}

type balanceSnapshot struct {
	SymQuote string
	SymBase  string

	AvailQuote float64
	QuoteStep  float64

	AvailBase float64
	BaseStep  float64

	UpdatedAt time.Time
}

const balanceSnapshotMaxAge = 3 * time.Second

func (t *Trader) setBalanceSnapshot(snapshot balanceSnapshot) {
	t.balanceMu.Lock()
	t.balanceSnapshot = snapshot
	t.balanceMu.Unlock()
}

func (t *Trader) getBalanceSnapshot(maxAge time.Duration) (balanceSnapshot, bool) {
	t.balanceMu.RLock()
	snapshot := t.balanceSnapshot
	t.balanceMu.RUnlock()

	if snapshot.UpdatedAt.IsZero() {
		return balanceSnapshot{}, false
	}

	if maxAge > 0 && time.Since(snapshot.UpdatedAt) > maxAge {
		return snapshot, false
	}

	if snapshot.SymQuote == "" ||
		snapshot.SymBase == "" ||
		snapshot.QuoteStep <= 0 ||
		snapshot.BaseStep <= 0 {

		return snapshot, false
	}

	return snapshot, true
}

func (t *Trader) invalidateBalanceSnapshot() {
	t.balanceMu.Lock()
	t.balanceSnapshot.UpdatedAt = time.Time{}
	t.balanceMu.Unlock()
}

func (t *Trader) refreshBalanceSnapshot(ctx context.Context) error {
	type quoteResult struct {
		symbol string
		avail  float64
		step   float64
		err    error
	}

	type baseResult struct {
		symbol string
		avail  float64
		step   float64
		err    error
	}

	quoteCh := make(chan quoteResult, 1)
	baseCh := make(chan baseResult, 1)

	// Fetch quote and base concurrently outside the trading mutex.
	go func() {
		symbol, avail, step, err :=
			t.broker.GetAvailableQuote(ctx, t.cfg.ProductID)

		quoteCh <- quoteResult{
			symbol: symbol,
			avail:  avail,
			step:   step,
			err:    err,
		}
	}()

	go func() {
		symbol, avail, step, err :=
			t.broker.GetAvailableBase(ctx, t.cfg.ProductID)

		baseCh <- baseResult{
			symbol: symbol,
			avail:  avail,
			step:   step,
			err:    err,
		}
	}()

	quote := <-quoteCh
	base := <-baseCh

	if quote.err != nil {
		return fmt.Errorf(
			"GetAvailableQuote failed: %w",
			quote.err,
		)
	}

	if base.err != nil {
		return fmt.Errorf(
			"GetAvailableBase failed: %w",
			base.err,
		)
	}

	if strings.TrimSpace(quote.symbol) == "" {
		return fmt.Errorf("GetAvailableQuote returned empty symbol")
	}

	if strings.TrimSpace(base.symbol) == "" {
		return fmt.Errorf("GetAvailableBase returned empty symbol")
	}

	if quote.step <= 0 {
		return fmt.Errorf(
			"invalid quote step %.8f",
			quote.step,
		)
	}

	if base.step <= 0 {
		return fmt.Errorf(
			"invalid base step %.8f",
			base.step,
		)
	}

	t.setBalanceSnapshot(balanceSnapshot{
		SymQuote: quote.symbol,
		SymBase:  base.symbol,

		AvailQuote: quote.avail,
		QuoteStep:  quote.step,

		AvailBase: base.avail,
		BaseStep:  base.step,

		UpdatedAt: time.Now(),
	})

	return nil
}

func (t *Trader) startBalanceRefresher(ctx context.Context) {
	t.balanceRefreshOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					log.Printf("[TRACE] balance.cache.refresher.stopped")
					return

				case <-ticker.C:
					refreshCtx, cancel := context.WithTimeout(
						ctx,
						2*time.Second,
					)

					started := time.Now()
					err := t.refreshBalanceSnapshot(refreshCtx)
					cancel()

					if err != nil {
						log.Printf(
							"[WARN] balance.cache.refresh.failed elapsed_ms=%d err=%v",
							time.Since(started).Milliseconds(),
							err,
						)
						continue
					}

					log.Printf(
						"[TRACE] balance.cache.refreshed elapsed_ms=%d",
						time.Since(started).Milliseconds(),
					)
				}
			}
		}()
	})
}

func (t *Trader) reserveCachedQuote(amount float64) {
	if amount <= 0 {
		return
	}

	t.balanceMu.Lock()
	defer t.balanceMu.Unlock()

	t.balanceSnapshot.AvailQuote -= amount
	if t.balanceSnapshot.AvailQuote < 0 {
		t.balanceSnapshot.AvailQuote = 0
	}
}

func (t *Trader) reserveCachedBase(amount float64) {
	if amount <= 0 {
		return
	}

	t.balanceMu.Lock()
	defer t.balanceMu.Unlock()

	t.balanceSnapshot.AvailBase -= amount
	if t.balanceSnapshot.AvailBase < 0 {
		t.balanceSnapshot.AvailBase = 0
	}
}
