// ---------------------------------------------------------------------------------------------
// FILE: step.go — Synchronized trading tick (EXIT → OPEN), extracted from trader.go
//
// Overview
//   step(ctx, candles) is the single-threaded decision loop that reads the latest market
//   snapshot, evaluates exits (profit-gate, trailing, fixed TP), then evaluates a new entry
//   (market or maker-first limit) — in that strict order. It returns a short, human-readable
//   status for logs/metrics and an error if any broker call fails.
//
// Inputs / Outputs
//   Input:  []Candle history (last element is the most recent mark/close).
//           Context is used for broker/network timeouts and cancellation.
//   Output: (msg string, err error) where msg ∈ {"EXIT …","OPEN …","HOLD","FLAT …","OPEN-PENDING …"}.
//
// Concurrency & Locks
//   • Takes t.mu at the top to read/update in-memory state, and RELEASES it around ANY I/O
//     (broker calls, price fetches, Slack). Every unlock is paired with a re-lock before
//     mutating state again.
//   • Close at most ONE lot per tick to keep behavior predictable.
//
// Deterministic Flow
//   1) Daily roll/metrics refresh
//   2) EXIT scan per side (BUY then SELL):
//        - Compute fee-aware net PnL and check profit gate
//        - If gate passes:
//            • Runner/Scalp trailing: USD-based trailing; close on stop trigger
//            • Fixed-TP scalp: maintain maker-friendly TP preview (emulated post-only)
//   3) OPEN evaluation (if no exit fired):
//        - Pull balances/steps with lock released
//        - Enforce MinNotional/OrderMinUSD and step/tick snapping symmetrically
//        - Equity triggers may override pyramiding/ramping gates
//        - If ORDER_TYPE=limit with offset+timeout → maker-first (async pending)
//          else place market immediately
//
// Maker-First Async Opens (Post-Only)
//   • Per-side PendingOpen is persisted and polled until filled/timeout; channels deliver the result.
//   • On fill: append lot using actual fill price/size/fee and record EntryOrderID.
//   • On timeout/error: set a per-side “recheck” flag permitting one market fallback later.
//   • RehydratePending() can restore polling after restart using saved OrderID+Deadline.
//
// Repricing Guardrails (async maker path)
//   • Optional repricing loop honors cfg: RepriceEnable, RepriceIntervalMs, RepriceMaxCount,
//     RepriceMaxDriftBps, RepriceMinImprovTicks, RepriceMinEdgeUSD, PriceTick, BaseStep, MinNotional.
//
// Pyramiding & Equity Triggers
//   • Pyramiding adds are side-aware and gated by spacing (seconds) and adverse-move thresholds,
//     with optional exponential decay & latching. Equity triggers can stage sizes (25/50/75/100%)
//     and may auto-designate the new lot as the side’s runner.
//
// Fees, Notional & Sizing
//   • Entry/exit PnL is fee-aware. Prefer broker-reported commission; fallback to FeeRatePct.
//   • All orders satisfy exchange min-notional and step/tick rules before submission.
//
// Persistence & IDs
//   • State mutations (equity, books, exits, pending) are persisted opportunistically.
//   • Lots carry LotID and EntryOrderID; NextLotSeq is incremented on each append.
//
// Dry-Run Behavior
//   • Simulates fees and adjusts equity locally; no broker calls.
//
// Logging & Metrics
//   • TRACE/DEBUG breadcrumbs at key gates (spacing, latching, trailing, post-only lifecycle).
//     Prometheus-style counters/gauges are updated for opens/exits.
// ---------------------------------------------------------------------------------------------
package main


import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

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
//   newOrderID        → possibly updated (if we re-priced successfully), else the input orderID
//   newLastLimitPx    → updated lastLimitPx if we re-priced
//   newRepriceCount   → incremented if we re-priced
//   didReprice        → true if we re-priced (placed a new order)
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
		if gErr != nil || px <= 0 { return orderID, lastLimitPx, repriceCount, false }
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
			newLimitPx = math.Floor(newLimitPx/tick) * tick     // round down for buys
		} else {
			newLimitPx = math.Ceil(newLimitPx/tick) * tick      // round up for sells  ✅
		}
	}
	// anti-cross nudge when using BBO
	if useBBO && tick > 0 {
		if side == SideBuy {
			if newLimitPx >= ask {
				cand := ask - tick
				if cand <= 0 { return orderID, lastLimitPx, repriceCount, false }
				newLimitPx = cand
			}
		} else {
			if newLimitPx <= bid {
				newLimitPx = bid + tick
			}
		}
	}else if useBBO && tick <= 0 {
		// If no tick, still ensure we don't cross the book when using BBO
		if side == SideBuy && newLimitPx >= ask { newLimitPx = math.Nextafter(ask, 0) }   // nudge below ask
		if side == SideSell && newLimitPx <= bid { newLimitPx = math.Nextafter(bid, +1) } // nudge above bid
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
func (t *Trader) step(ctx context.Context, c []Candle) (string, error) {
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

	// --- NEW (Phase 4): drain async maker-first results per-side (non-blocking) ---
	if t.pendingBuyCh != nil {
		select {
		case res := <-t.pendingBuyCh:
			log.Printf("TRACE postonly.drain.recv side=%s order_id=%s filled=%v placed_nil=%v", SideBuy, res.OrderID, res.Filled, res.Placed == nil)
			// Decide whether to accept this fill
			accept := false
			if res.Filled && res.Placed != nil {
				log.Printf("TRACE postonly.drain.placed side=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f", SideBuy, res.OrderID, res.Placed.Price, res.Placed.BaseSize, res.Placed.QuoteSpent, res.Placed.CommissionUSD)
				if t.pendingBuy != nil {
					// Accept if it matches current or any historical order id
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
					// No pending in memory, but we received a fill → accept to avoid orphaning
					accept = true
					log.Printf("[WARN] postonly.fill.without_pending side=%s order_id=%s", SideBuy, res.OrderID)
				}
			}

			if accept {
				// Append lot using res.Placed (price/base/fee), set a safe Reason
				side := SideBuy
				book := t.book(side)
				priceToUse := res.Placed.Price
				baseToUse := res.Placed.BaseSize
				quoteSpent := res.Placed.QuoteSpent
				entryFee := res.Placed.CommissionUSD
				if entryFee <= 0 {
					entryFee = quoteSpent * (t.cfg.FeeRatePct / 100.0)
				}

				// Compute history match explicitly (clearer than inline func)
				matchHistory := false
				if t.pendingBuy != nil {
					for _, id := range t.pendingBuy.History {
						if id == res.OrderID { matchHistory = true; break }
					}
				}

				log.Printf("TRACE postonly.drain.accept side=%s match_current=%v match_history=%v pending_nil=%v",
					SideBuy,
					t.pendingBuy != nil && res.OrderID == t.pendingBuy.OrderID,
					matchHistory,
					t.pendingBuy == nil,
				)

				newLot := &Position{
					OpenPrice:    priceToUse,
					Side:         side,
					SizeBase:     baseToUse,
					OpenTime:     now,
					EntryFee:     entryFee,
					OpenNotionalUSD:  quoteSpent,   // <<< USD PERSISTENCE: notional in USD at open
					Reason:       "async postonly filled",
					Take:         0, // or carry from pending if available
					Version:      1,
					LotID:        t.NextLotSeq,
					EntryOrderID: res.OrderID,
				}
				if t.pendingBuy != nil {
					newLot.Reason = t.pendingBuy.Reason
					newLot.Take = t.pendingBuy.Take
				}
				idx := len(book.Lots) // the new lot’s index after append
				if idx >= 6 {
					if !strings.Contains(newLot.Reason, "mode=choppy") {
						newLot.Reason = strings.TrimSpace(newLot.Reason + " mode=choppy")
					}
				} else if idx >= 3 && idx <= 5 {
					if !strings.Contains(newLot.Reason, "mode=strict") {
						newLot.Reason = strings.TrimSpace(newLot.Reason + " mode=strict")
					}
				}
				t.NextLotSeq++
				book.Lots = append(book.Lots, newLot)
				if t.pendingBuy != nil && t.pendingBuy.EquityBuy {
					book.RunnerID = len(book.Lots) - 1 // the newly appended lot
					r := book.Lots[book.RunnerID]
					r.TrailActive = false
					r.TrailPeak = r.OpenPrice
					r.TrailStop = 0
					t.applyRunnerTargets(r)
					log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", book.RunnerID, side, r.OpenPrice, r.Take)
				}
				// side-specific resets...
				t.lastAddBuy = wallNow
				t.winLowBuy = priceToUse
				t.latchedGateBuy = 0
				old := t.lastAddEquityBuy
				t.lastAddEquityBuy = t.equityUSD
				log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f", side, old, t.lastAddEquityBuy)

				msg := fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
					side, quoteSpent, newLot.Take, entryFee, newLot.Reason, "async postonly filled")
				if t.cfg.Extended().UseDirectSlack {
					postSlack(msg)
				}
				// save
				if err := t.saveStateNoLock(); err != nil {
					log.Printf("[WARN] saveState (filled BUY): %v", err)
				}
			} else {
				// Non-fill (timeout/error) → enable one market fallback
				t.pendingRecheckBuy = true
				log.Printf("TRACE postonly.recheck side=%s set=true reason=timeout_or_error order_id=%s", SideBuy, res.OrderID)
			}

			// Cancel & clear pending either way
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
		}
	}

	if t.pendingSellCh != nil {
		select {
		case res := <-t.pendingSellCh:
			log.Printf("TRACE postonly.drain.recv side=%s order_id=%s filled=%v placed_nil=%v", SideSell, res.OrderID, res.Filled, res.Placed == nil)
			// Decide whether to accept this fill
			accept := false
			if res.Filled && res.Placed != nil {
				log.Printf("TRACE postonly.drain.placed side=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f", SideSell, res.OrderID, res.Placed.Price, res.Placed.BaseSize, res.Placed.QuoteSpent, res.Placed.CommissionUSD)
				if t.pendingSell != nil {
					// Accept if it matches current or any historical order id
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
					// No pending in memory, but we received a fill → accept to avoid orphaning
					accept = true
					log.Printf("[WARN] postonly.fill.without_pending side=%s order_id=%s", SideSell, res.OrderID)
				}
			}

			if accept {
				// Append lot using res.Placed (price/base/fee), set a safe Reason
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
						if id == res.OrderID { matchHistory = true; break }
					}
				}

				log.Printf("TRACE postonly.drain.accept side=%s match_current=%v match_history=%v pending_nil=%v",
					SideSell,
					t.pendingSell != nil && res.OrderID == t.pendingSell.OrderID,
					matchHistory,
					t.pendingSell == nil,
				)

				newLot := &Position{
					OpenPrice:    priceToUse,
					Side:         side,
					SizeBase:     baseToUse,
					OpenTime:     now,
					EntryFee:     entryFee,
					OpenNotionalUSD:  quoteSpent,   // <<< USD PERSISTENCE
					Reason:       "async postonly filled",
					Take:         0, // or carry from pending if available
					Version:      1,
					LotID:        t.NextLotSeq,
					EntryOrderID: res.OrderID,
				}
				if t.pendingSell != nil {
					newLot.Reason = t.pendingSell.Reason
					newLot.Take = t.pendingSell.Take
				}
				idx := len(book.Lots) // the new lot’s index after append
				if idx >= 6 {
					if !strings.Contains(newLot.Reason, "mode=choppy") {
						newLot.Reason = strings.TrimSpace(newLot.Reason + " mode=choppy")
					}
				} else if idx >= 3 && idx <= 5 {
					if !strings.Contains(newLot.Reason, "mode=strict") {
						newLot.Reason = strings.TrimSpace(newLot.Reason + " mode=strict")
					}
				}
				t.NextLotSeq++
				book.Lots = append(book.Lots, newLot)
				if t.pendingSell != nil && t.pendingSell.EquitySell {
					book.RunnerID = len(book.Lots) - 1 // the newly appended lot
					r := book.Lots[book.RunnerID]
					r.TrailActive = false
					r.TrailPeak = r.OpenPrice
					r.TrailStop = 0
					t.applyRunnerTargets(r)
					log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", book.RunnerID, side, r.OpenPrice, r.Take)
				}
				// side-specific resets...
				t.lastAddSell = wallNow
				t.winHighSell = priceToUse
				t.latchedGateSell = 0
				old := t.lastAddEquitySell
				t.lastAddEquitySell = t.equityUSD
				log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f", side, old, t.lastAddEquitySell)

				msg := fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
					side, quoteSpent, newLot.Take, entryFee, newLot.Reason, "async postonly filled")
				if t.cfg.Extended().UseDirectSlack {
					postSlack(msg)
				}
				// save
				if err := t.saveStateNoLock(); err != nil {
					log.Printf("[WARN] saveState (filled SELL): %v", err)
				}
			} else {
				// Non-fill (timeout/error) → enable one market fallback
				t.pendingRecheckSell = true
				log.Printf("TRACE postonly.recheck side=%s set=true reason=timeout_or_error order_id=%s", SideSell, res.OrderID)
			}

			// Cancel & clear pending either way
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
		}
	}

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	_ = t.shouldRefit(len(c)) // intentionally unused here (guard only)

	// TODO: remove TRACE
	lsb := len(t.book(SideBuy).Lots)
	lss := len(t.book(SideSell).Lots)
	log.Printf("TRACE step.start ts=%s price=%.8f lotsBuy=%d lotsSell=%d lastAddBuy=%s lastAddSell=%s winLowBuy=%.8f winHighSell=%.8f latchedGateBuy=%.8f latchedGateSell=%.8f",
		now.Format(time.RFC3339), c[len(c)-1].Close, lsb, lss,
		t.lastAddBuy.Format(time.RFC3339), t.lastAddSell.Format(time.RFC3339), t.winLowBuy, t.winHighSell, t.latchedGateBuy, t.latchedGateSell)

	price := c[len(c)-1].Close

	// --- Effective min-notional for this tick: prefer cfg.MinNotional, fallback to cfg.OrderMinUSD ---
	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}

	// --------------------------------------------------------------------------------------------------------
	// --- EXIT path: evaluate profit gate and trailing/TP logic per lot (side-aware) and close at most one.
	// --------------------------------------------------------------------------------------------------------
	if (lsb > 0) || (lss > 0) {

		nearestTakeBuy := 0.0
		nearestTakeSell := 0.0

		// Helper: compute net PnL and gate state for one lot, update EstExitFeeUSD/UnrealizedPnLUSD
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
			return net, net >= t.cfg.ProfitGateUSD
		}

		// Helper to classify ExitMode (idempotent) and set a preview Take
		setExitMode := func(book *SideBook, idx int, lot *Position) {
			feeRatePct := t.cfg.FeeRatePct

			if idx == book.RunnerID {
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
			// 0..2 → trailing (scalp); 3..5 → fixed TP
			if idx >= 0 && idx <= 2 {
				// ScalpTrailing: idx 0..2
				lot.ExitMode = ExitModeScalpTrailing
				lot.TrailDistancePct = t.cfg.TrailDistancePctScalp
				lot.TrailActivateGateUSD = t.cfg.TrailActivateUSDScalp
				lot.Take = activationPrice(lot, lot.TrailActivateGateUSD, feeRatePct)
				if lot.TrailPeak == 0 {
					lot.TrailPeak = lot.OpenPrice
				}
				return
			} else {
				// ScalpFixedTP: Take = fee-aware profit-gate price (preview);
				// when gate passes you’ll arm FixedTPWorking and use this for post-only exits.
				lot.ExitMode = ExitModeScalpFixedTP
				lot.TrailDistancePct = 0
				lot.TrailActivateGateUSD = t.cfg.ProfitGateUSD
				lot.Take = activationPrice(lot, lot.TrailActivateGateUSD, feeRatePct)
			}
		}

		// Helper to scan a side book
		scanSide := func(side OrderSide) (string, bool, error) {
			book := t.book(side)
			for i := 0; i < len(book.Lots); {
				lot := book.Lots[i]
				// classify per spec
				setExitMode(book, i, lot)

				// compute gate
				net, pass := computeGate(lot)
				
				// Override gating for FixedTP buckets:
				// - idx 3..5 → stricter 4× gate
				// - idx ≥ 6  → normal 1× gate (ScalpChoppyTP semantics)
				if lot.ExitMode == ExitModeScalpFixedTP {
					if i >= 3 && i <= 5 {
						pass = net >= 4.0 * t.cfg.ProfitGateUSD
						lot.TrailActivateGateUSD = 4.0 * t.cfg.ProfitGateUSD // preview consistency
					} else if i >= 6 {
						pass = net >= t.cfg.ProfitGateUSD
						lot.TrailActivateGateUSD = t.cfg.ProfitGateUSD
					}
				}

				// reset arming when gate not passed
				if !pass {
					// strict gating: no exit arms
					lot.TrailActive = false
					lot.TrailPeak = 0
					lot.TrailStop = 0
					lot.FixedTPWorking = false
					i++
					continue
				} else {
					// first pass breadcrumb
					if lot.Take == 0 && !lot.TrailActive && !lot.FixedTPWorking {
						log.Printf("TRACE GATE_PASS !!!!!!! side=%s net=%.6f gate=%.6f", lot.Side, net, t.cfg.ProfitGateUSD)
					}
				}

				// --- EXIT arming when profit gate passed ---
				switch lot.ExitMode {
				case ExitModeRunnerTrailing, ExitModeScalpTrailing:
					// trailing path (USD activate)
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
						msg, err := t.closeLot(ctx, c, side, i, "trailing_stop")
						if err != nil {
							t.mu.Unlock()
							return "", true, err
						}
						t.mu.Unlock()
						return msg, true, nil
					}

				case ExitModeScalpFixedTP:
					// emulate a maker-friendly TP "post" at/near mark
					offBps := t.cfg.TPMakerOffsetBps
					tp := price
					if lot.Side == SideBuy && offBps > 0 {
						tp = price * (1.0 + offBps/10000.0)
					}
					if lot.Side == SideSell && offBps > 0 {
						tp = price * (1.0 - offBps/10000.0)
					}
					// place/re-post every tick while gate holds (minimal emulation)
					if !lot.FixedTPWorking || (lot.Side == SideBuy && tp < lot.Take) || (lot.Side == SideSell && tp > lot.Take) {
						lot.Take = tp
						lot.FixedTPWorking = true
						log.Printf("TRACE tp.post side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
					} else {
						log.Printf("TRACE tp.repost side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
					}
				}

				// --- Legacy trigger block (now governed by our gated Stop/Take) ---
				trigger := false
				exitReason := ""
				if lot.ExitMode == ExitModeScalpFixedTP {
					exitReason = "take_profit"
				} else if lot.ExitMode == ExitModeRunnerTrailing || lot.ExitMode == ExitModeScalpTrailing {
					exitReason = "trailing_stop"
				}
				if lot.Side == SideBuy && (lot.Take > 0 && price >= lot.Take) {
					trigger = true
				}
				if lot.Side == SideSell && (lot.Take > 0 && price <= lot.Take) {
					trigger = true
				}
				if trigger {
					msg, err := t.closeLot(ctx, c, side, i, exitReason)
					if err != nil {
						t.mu.Unlock()
						return "", true, err
					}
					if lot.ExitMode == ExitModeScalpFixedTP {
						log.Printf("TRACE tp.filled side=%s idx=%d mark=%.8f take=%.8f", lot.Side, i, price, lot.Take)
					}
					t.mu.Unlock()
					return msg, true, nil
				}

				// nearest summary (unchanged)
				if lot.Side == SideBuy {
					if lot.Take > 0 && (nearestTakeBuy == 0 || lot.Take < nearestTakeBuy) {
						nearestTakeBuy = lot.Take
					}
				} else {
					if lot.Take > 0 && (nearestTakeSell == 0 || lot.Take > nearestTakeSell) {
						nearestTakeSell = lot.Take
					}
				}
				i++
			}
			return "", false, nil
		}

		// BUY side first, then SELL
		if msg, done, err := scanSide(SideBuy); done || err != nil {
			return msg, err
		}
		if msg, done, err := scanSide(SideSell); done || err != nil {
			return msg, err
		}

		log.Printf("[DEBUG] Nearest Take (to close a buy lot)=%.2f, (to close a sell lot)=%.2f, across %d BuyLots, and %d SellLots", nearestTakeBuy, nearestTakeSell, lsb, lss)
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
	d := decide(c, t.model, t.mdlExt, t.cfg.BuyThreshold, t.cfg.SellThreshold, t.cfg.UseMAFilter)
	totalLots := lsb + lss
	log.Printf("[DEBUG] Total Lots=%d, Decision=%s Reason = %s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v ver-6",
		totalLots, d.Signal, d.Reason, t.cfg.BuyThreshold, t.cfg.SellThreshold, t.cfg.LongOnly)

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	// Determine the side and its book
	side := d.SignalToSide()
	book := t.book(side)

	// --- NEW (Phase 4): prevent duplicate opens while pending on this side (exits already ran) ---
	// Extra belt-and-suspenders: if a pending exists and we haven't hit its Deadline, keep waiting.
	if side == SideBuy && t.pendingBuy != nil && time.Now().Before(t.pendingBuy.Deadline) {
		t.mu.Unlock()
		return "OPEN-PENDING side=BUY", nil
	}
	if side == SideSell && t.pendingSell != nil && time.Now().Before(t.pendingSell.Deadline) {
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

	// --- track lowest price since last add (BUY path) and highest price (SELL path) ---
	if !t.lastAddBuy.IsZero() {
		if t.winLowBuy == 0 || price < t.winLowBuy {
			t.winLowBuy = price
		}
	}
	if !t.lastAddSell.IsZero() {
		if t.winHighSell == 0 || price > t.winHighSell {
			t.winHighSell = price
		}
	}

	// --- NEW (minimal): equity strategy trigger detection (SELL runner add of entire spare base)
	equityTriggerSell := false
	var equitySpareBase float64
	if t.lastAddEquitySell > 0 && t.equityUSD >= t.lastAddEquitySell*1.01 && d.Signal == Sell {
		// Only proceed if not long-only; respect existing guard
		if t.cfg.LongOnly {
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
	if t.lastAddEquityBuy > 0 && t.equityUSD <= t.lastAddEquityBuy*0.99 && d.Signal == Buy {
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

	log.Printf("[DEBUG] EQUITY Trading: equityUSD=%.2f lastAddEquitySell=%.2f pct_diff_sell=%.6f lastAddEquityBuy=%.2f pct_diff_buy=%.6f", t.equityUSD, t.lastAddEquitySell, t.equityUSD/t.lastAddEquitySell, t.lastAddEquityBuy, t.equityUSD/t.lastAddEquityBuy)

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}
	if d.Signal == Flat && !equityTriggerSell && !equityTriggerBuy {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT [%s]", d.Reason), nil
	}

	// GATE1 Respect lot cap (both sides)
	if (lsb+lss) >= maxConcurrentLots() && !((equityTriggerBuy && d.Signal == Buy) || (equityTriggerSell && d.Signal == Sell)) {
		t.mu.Unlock()
		log.Printf("[DEBUG] GATE1 lot cap reached (%d); HOLD", maxConcurrentLots())
		return "HOLD", nil
	}

	// Determine if we are opening equity triggered trade or attempting a pyramid add (side-aware).
	isAdd := len(book.Lots) > 0 && t.cfg.AllowPyramiding && (d.Signal == Buy || d.Signal == Sell)
	// --- NEW: skip pyramiding gates for equity-triggered paths (minimal) ---
	skipPyramidGates := equityTriggerSell || equityTriggerBuy

	// --- NEW: variables to capture gate audit fields for the reason string (side-biased; no winLow) ---
	var (
		reasonGatePrice float64
		reasonLatched   float64
		reasonEffPct    float64
		reasonBasePct   float64
		reasonElapsedHr float64
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
			effPct = decayed
		}

		// Capture for reason string
		reasonBasePct = basePct
		reasonEffPct = effPct
		reasonElapsedHr = elapsedMin / 60.0

		// Time (in minutes) to hit the floor once (t_floor_min)
		tFloorMin := 0.0
		if lambda > 0 && basePct > floor {
			tFloorMin = math.Log(basePct/floor) / lambda
		}

		// TODO: remove TRACE
		log.Printf("TRACE pyramid.adverse side=%s lastAddAgoMin=%.2f basePct=%.4f effPct=%.4f lambda=%.5f floor=%.4f tFloorMin=%.2f",
			side, elapsedMin, basePct, effPct, lambda, floor, tFloorMin)

		// Use side-aware latest entry for adverse gate anchoring
		last := t.latestEntryBySide(side)

		if last > 0 {
			if side == SideBuy {
				// BUY adverse tracker (side-aware)
				if elapsedMin >= tFloorMin {
					if t.winLowBuy == 0 || price < t.winLowBuy {
						t.winLowBuy = price
					}
				} else {
					t.winLowBuy = 0
				}
				// latch at 2*t_floor_min
				if t.latchedGateBuy == 0 && elapsedMin >= 2.0*tFloorMin && t.winLowBuy > 0 {
					t.latchedGateBuy = t.winLowBuy
					// --- breadcrumb ---
					log.Printf("[DEBUG] LATCH SET BUY: latchedGate=%.2f winLow=%.2f elapsedMin=%.1f tFloorMin=%.2f",
						t.latchedGateBuy, t.winLowBuy, elapsedMin, tFloorMin)
				}
				// baseline gate: last * (1.0 - effPct); latched replaces baseline
				gatePrice := last * (1.0 - effPct/100.0)
				if t.latchedGateBuy > 0 {
					gatePrice = t.latchedGateBuy
				}
				// clamp to min(winLowBuy, last BUY open)
				clampP := last
				if t.winLowBuy > 0 && t.winLowBuy < clampP {
					clampP = t.winLowBuy
				}
				if gatePrice > clampP {
					gatePrice = clampP
				}

				// mirror for reason/log fields
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateBuy

				// --- breadcrumb when baseline condition met ---
				if price <= gatePrice {
					log.Printf("[DEBUG] pyramid: BUY baseline met price=%.2f gatePrice=%.2f last=%.2f eff_pct=%.3f elapsedMin=%.1f",
						price, gatePrice, last, effPct, elapsedMin)
				}

				if !(price <= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (BUY); price=%.2f last_gate<=%.2f win_low=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winLowBuy, effPct, basePct, reasonElapsedHr)
					log.Printf("TRACE pyramid.block.buy price=%.8f last=%.8f gate=%.8f latched=%.8f", price, last, gatePrice, t.latchedGateBuy)
					return "HOLD", nil
				}
			} else { // SELL
				// SELL adverse tracker (side-aware)
				if elapsedMin >= tFloorMin {
					if t.winHighSell == 0 || price > t.winHighSell {
						t.winHighSell = price
					}
				} else {
					t.winHighSell = 0
				}
				if t.latchedGateSell == 0 && elapsedMin >= 2.0*tFloorMin && t.winHighSell > 0 {
					t.latchedGateSell = t.winHighSell
					// --- breadcrumb ---
					log.Printf("[DEBUG] LATCH SET SELL: latchedGate=%.2f winHigh=%.2f elapsedMin=%.1f tFloorMin=%.2f",
						t.latchedGateSell, t.winHighSell, elapsedMin, tFloorMin)
				}
				// baseline gate: last * (1.0 + effPct); latched replaces baseline
				gatePrice := last * (1.0 + effPct/100.0)
				if t.latchedGateSell > 0 {
					gatePrice = t.latchedGateSell
				}
				// clamp to max(winHighSell, last SELL open)
				clampP := last
				if t.winHighSell > 0 && t.winHighSell > clampP {
					clampP = t.winHighSell
				}
				if gatePrice < clampP {
					gatePrice = clampP
				}

				// mirror for legacy reason/log fields
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateSell

				// --- breadcrumb when baseline condition met ---
				if price >= gatePrice {
					log.Printf("[DEBUG] pyramid: SELL baseline met price=%.2f gatePrice=%.2f last=%.2f eff_pct=%.3f elapsedMin=%.1f",
						price, gatePrice, last, effPct, elapsedMin)
				}

				if !(price >= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (SELL); price=%.2f last_gate>=%.2f win_high=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winHighSell, effPct, basePct, reasonElapsedHr)
					log.Printf("TRACE pyramid.block.sell price=%.8f last=%.8f gate=%.8f latched=%.8f", price, last, gatePrice, t.latchedGateSell)
					return "HOLD", nil
				}
			}
		}
	}

	// Sizing (risk % of current equity, with optional volatility adjust already supported).
	riskPct := t.cfg.RiskPerTradePct
	if t.cfg.Extended().VolRiskAdjust {
		f := volRiskFactor(c)
		riskPct = riskPct * f
		SetVolRiskFactorMetric(f)
	}

	// --- NEW: side-aware risk ramping (optional) ---
	if t.cfg.RampEnable && !(equityTriggerSell || equityTriggerBuy) {
		k := len(book.Lots) // number of existing lots on THIS SIDE
		switch strings.ToLower(strings.TrimSpace(t.cfg.RampMode)) {
		case "exp":
			start := t.cfg.RampStartPct
			g := t.cfg.RampGrowth
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
			if f > 0 {
				riskPct = f
			}
		default: // linear
			start := t.cfg.RampStartPct
			step := t.cfg.RampStepPct
			f := start + float64(k)*step
			f = clamp(f, 0, t.cfg.RampMaxPct)
			if f > 0 {
				riskPct = f
			}
		}
	}

	quote := (riskPct / 100.0) * t.equityUSD
	if quote < minNotional {
		quote = minNotional
	}
	base := quote / price

	// --- NEW: staged sizing for EQUITY triggers (SELL in BASE, BUY in QUOTE) ---
	// --- NEW: override sizing for normal Sell using stage function of spare base as the order size (SELL only) ---
	if equityTriggerSell && side == SideSell && equitySpareBase > 0 {
		stagesSell := equityStagesSell()
		startStage := clampStage(t.equityStageSell, len(stagesSell))
		chosen := -1
		var targetBase float64
		for s := startStage; s < len(stagesSell); s++ {
			tb := equitySpareBase * stagesSell[s]
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
	log.Printf("TRACE sizing.pre side=%s eq=%.2f riskPct=%.4f quote=%.2f price=%.8f base=%.8f", side, t.equityUSD, riskPct, quote, price, base)

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
		if spare+spareEps < neededQuote {
			// --- breadcrumb ---
			log.Printf("[WARN] FUNDS_EXHAUSTED BUY need=%.2f quote, spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
				neededQuote, spare, availQuote, reservedShortQuoteWithFee, quoteStep)
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE BUY: need=%.2f quote, spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
				neededQuote, spare, availQuote, reservedShortQuoteWithFee, quoteStep)
			log.Printf("TRACE buy.gate.block need=%.2f spare=%.2f", neededQuote, spare)
			return "HOLD", nil
		}

		// Enforce exchange minimum notional after snapping, then snap UP to step to keep >= min; re-check spare.
		if neededQuote < minNotional {
			neededQuote = minNotional
			if quoteStep > 0 {
				steps := math.Ceil(neededQuote / quoteStep)
				neededQuote = steps * quoteStep
			}
			if spare+spareEps < neededQuote {
				// --- breadcrumb ---
				log.Printf("[WARN] FUNDS_EXHAUSTED BUY need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
					neededQuote, spare, availQuote, reservedShortQuoteWithFee, quoteStep)
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE BUY: need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
					neededQuote, spare, availQuote, reservedShortQuoteWithFee, quoteStep)
				log.Printf("TRACE buy.gate.block minNotional need=%.2f spare=%.2f", neededQuote, spare)
				return "HOLD", nil
			}
		}

		// Use the final neededQuote; recompute base.
		quote = neededQuote
		base = quote / price

		// TODO: remove TRACE
		log.Printf("TRACE buy.gate.post needQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && t.cfg.RequireBaseForShort {
		// TODO: remove TRACE
		log.Printf("TRACE sell.gate.pre availBase=%.8f reservedLong=%.8f needBaseRaw=%.8f baseStep=%.8f",
			availBase, reservedLongBase, base, baseStep)

		// Floor the *needed* base to baseStep (if known) and cap by spare availability
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
		if spare+spareEps < neededBase {
			// --- breadcrumb ---
			log.Printf("[WARN] FUNDS_EXHAUSTED SELL need=%.8f base, spare=%.8f (avail=%.8f, reserved_longs=%.8f, baseStep=%.8f)",
				neededBase, spare, availBase, reservedLongBase, baseStep)
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE SELL: need=%.8f base, spare=%.8f (avail=%.8f, reserved_longs=%.8f, baseStep=%.8f)",
				neededBase, spare, availBase, reservedLongBase, baseStep)
			log.Printf("TRACE sell.gate.block need=%.8f spare=%.8f", neededBase, spare)
			return "HOLD", nil
		}

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
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE SELL: need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, baseStep=%.8f)",
					base, spare, availBase, reservedLongBase, baseStep)
				log.Printf("TRACE sell.gate.block minNotional need=%.8f spare=%.8f", base, spare)
				return "HOLD", nil
			}
		}

		// TODO: remove TRACE
		log.Printf("TRACE sell.gate.post needBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
	}

	var take float64
	if t.cfg.ScalpTPDecayEnable && !((equityTriggerBuy && side == SideBuy) || (equityTriggerSell && side == SideSell)) {
		// This is a scalp add on THIS SIDE: compute k = number of existing scalps in this side
		k := len(book.Lots)
		if book.RunnerID >= 0 && book.RunnerID < len(book.Lots) {
			k = len(book.Lots) - 1 // exclude the side's runner
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
		gatesReason = fmt.Sprintf("EQUITY Trading: equityUSD=%.2f lastAddEquitySell=%.2f pct_diff_sell=%.6f equitySpareBase=%.8f ", t.equityUSD, t.lastAddEquitySell, t.equityUSD/t.lastAddEquitySell, equitySpareBase)
	} else if equityTriggerBuy && side == SideBuy && equitySpareQuote > 0 {
		gatesReason = fmt.Sprintf("EQUITY Trading: equityUSD=%.2f lastAddEquityBuy=%.2f pct_diff_buy=%.6f equitySpareQuote=%.2f", t.equityUSD, t.lastAddEquityBuy, t.equityUSD/t.lastAddEquityBuy, equitySpareQuote)
	} else if side == SideBuy {
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|PriceDownGoingUp=%v|LowBottom=%v",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr,
			d.PriceDownGoingUp, d.LowBottom,
		)
	} else { // SideSell
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|HighPeak=%v|PriceUpGoingDown=%v",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr,
			d.HighPeak, d.PriceUpGoingDown,
		)
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
		if side == SideBuy && t.pendingRecheckBuy { recheckNow = true }
		if side == SideSell && t.pendingRecheckSell { recheckNow = true }
		
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
			limitPx := price
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
			if baseAtLimit <= 0 || baseAtLimit*limitPx < minNotional {
				// If we cannot satisfy min-notional with snapped values, skip maker path; fall through to market path below.
			} else {
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

					// Create per-side pending & ctx
					pctx, cancel := context.WithCancel(ctx)
					t.mu.Lock()
					if side == SideBuy {
						t.pendingBuyCtx = pctx
						t.pendingBuyCancel = cancel
						t.pendingBuy = &PendingOpen{
							Side:        side,
							LimitPx:     limitPx,
							BaseAtLimit: baseAtLimit,
							Quote:       quote,
							Take:        take,
							Reason:      gatesReason, // set later below
							ProductID:   t.cfg.ProductID,
							CreatedAt:   time.Now().UTC(),
							Deadline:    time.Now().Add(time.Duration(limitWait) * time.Second),
							EquityBuy:   equityTriggerBuy,
							EquitySell:  equityTriggerSell,
							OrderID:     orderID,
							History:     make([]string, 0, 5), // NEW
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
							Side:        side,
							LimitPx:     limitPx,
							BaseAtLimit: baseAtLimit,
							Quote:       quote,
							Take:        take,
							Reason:      gatesReason, // set later below
							ProductID:   t.cfg.ProductID,
							CreatedAt:   time.Now().UTC(),
							Deadline:    time.Now().Add(time.Duration(limitWait) * time.Second),
							EquityBuy:   equityTriggerBuy,
							EquitySell:  equityTriggerSell,
							OrderID:     orderID,
							History:     make([]string, 0, 5), // NEW
						}
						if side == SideSell && t.pendingSell != nil {
							log.Printf("TRACE postonly.pending.set side=%s order_id=%s limit=%.8f base=%.8f quote=%.2f dl=%s eqFlags[buy=%v sell=%v]",
								side, t.pendingSell.OrderID, t.pendingSell.LimitPx, t.pendingSell.BaseAtLimit, t.pendingSell.Quote,
								t.pendingSell.Deadline.Format(time.RFC3339), t.pendingSell.EquityBuy, t.pendingSell.EquitySell)
						}
					}
					// Capture immutable copy for poller
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

					// Spawn poller (Phase 4: smart reprice loop) with environment guardrails
					go func(initOrderID string, side OrderSide, deadline time.Time, initLimitPx, initBaseAtLimit float64, pend *PendingOpen, ch chan OpenResult, pctx context.Context) {
						log.Printf("TRACE postonly.poll.start side=%s init_id=%s init_limit=%.8f init_base=%.8f deadline=%s offset_bps=%.3f",
							side, initOrderID, initLimitPx, initBaseAtLimit, deadline.Format(time.RFC3339), offsetBps)
						defer func (){log.Printf("TRACE postonly.poll.stopped side=%s initial_id=%s", side, initOrderID)}()
								

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
									log.Printf("TRACE postonly.poll.done side=%s order_id=%s final=FILLED vwap=%.8f base=%.8f quote=%.2f fee=%.6f",
										side, orderID, func() float64 { if sessBase>0 { return sessQuote / sessBase }; return 0 }(), sessBase, sessQuote, sessFee)
									log.Printf("TRACE postonly.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f", orderID, ord.Price, ord.BaseSize, ord.QuoteSpent, ord.CommissionUSD)
									mtxOrders.WithLabelValues("live", string(side)).Inc()
									mtxTrades.WithLabelValues("open").Inc()
									log.Printf("TRACE postonly.poll.emit side=%s order_id=%s filled=%v base=%.8f quote=%.2f fee=%.6f",
										side, orderID, (sessBase > 0 || sessQuote > 0), sessBase, sessQuote, sessFee)
									safeSend(ch, OpenResult{Filled: true, Placed: placed, OrderID: orderID})
									return

								case "PARTIALLY_FILLED":
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
										func() float64 { if sessBase>0 { return sessQuote / sessBase }; return 0 }(), sessBase, sessQuote, sessFee)

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

					// Build reason string now that all gates are known
					// (We set it after spawning to minimize time outside the lock.)
					t.mu.Lock()
					// persist pending state
					_ = t.saveStateNoLock()
					t.mu.Unlock()
					return fmt.Sprintf("OPEN-PENDING side=%s", side), nil
				} else if err != nil {
					log.Printf("TRACE postonly.error fallback=market err=%v", err)
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
				return "HOLD", nil
			}
			// TODO: remove TRACE
			log.Printf("TRACE order.open request side=%s quote=%.2f baseEst=%.8f priceSnap=%.8f take=%.8f",
				side, quote, base, price, take)
			var err error
			log.Printf("TRACE postonly.market_fallback.go side=%s quote=%.2f", side, quote)
			placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
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
					if t.cfg.Extended().UseDirectSlack {
						postSlack(fmt.Sprintf("ERR step: %v", err))
					}
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

	newLot := &Position{
		OpenPrice:    priceToUse,
		Side:         side,
		SizeBase:     baseToUse,
		OpenTime:     now,
		EntryFee:     entryFee,
		OpenNotionalUSD:  actualQuote,      // <<< USD PERSISTENCE: notional in USD at open
		Reason:       gatesReason, // side-biased; no winLow
		Take:         take,
		Version:      1,
		LotID:        t.NextLotSeq,
		EntryOrderID: "", // market path has no known order id here
	}
	idx := len(book.Lots) // the new lot’s index after append
	if idx >= 6 {
		if !strings.Contains(newLot.Reason, "mode=choppy") {
			newLot.Reason = strings.TrimSpace(newLot.Reason + " mode=choppy")
		}
	} else if idx >= 3 && idx <= 5 {
		if !strings.Contains(newLot.Reason, "mode=strict") {
			newLot.Reason = strings.TrimSpace(newLot.Reason + " mode=strict")
		}
	}
	t.NextLotSeq++
	book.Lots = append(book.Lots, newLot)
	// Use wall clock for lastAdd to drive spacing/decay even if candle time is zero.
	if side == SideBuy {
		t.lastAddBuy = wallNow
		t.winLowBuy = priceToUse
		t.latchedGateBuy = 0
	} else {
		t.lastAddSell = wallNow
		t.winHighSell = priceToUse
		t.latchedGateSell = 0
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
		book.RunnerID = len(book.Lots) - 1 // promote the equity trade lot to runner
		r := book.Lots[book.RunnerID]
		// Initialize/Reset trailing fields for the new runner
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		// Apply runner targets (stretched TP)
		t.applyRunnerTargets(r)
		log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", book.RunnerID, side, r.OpenPrice, r.Take)
	}
	// --- NEW (minimal): promote equity-triggered BUY add to runner ---
	if equityTriggerBuy && side == SideBuy {
		book.RunnerID = len(book.Lots) - 1
		r := book.Lots[book.RunnerID]
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		t.applyRunnerTargets(r)
		log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", book.RunnerID, side, r.OpenPrice, r.Take)
	}
	// (If not equityTriggerSell/equityTriggerBuy, leave RunnerID unchanged so first lot is NOT the runner.)

	// Rebuild legacy aggregate view for logs/compat
	// t.refreshAggregateFromBooks()

	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f take=%.2f fee=%.4f reason=%s [%s]",
			side, actualQuote, baseToUse, newLot.Take, entryFee, newLot.Reason, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s notional=%.2f take=%.2f fee=%.4f reason=%s",
			side, newLot.OpenNotionalUSD, newLot.Take, entryFee, newLot.Reason)
	}
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state (no locking while writing; snapshot constructed here under lock)
	if err := t.saveStateNoLock(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		log.Printf("TRACE state.save error=%v", err)
	}
	t.mu.Unlock()
	return msg, nil
}
