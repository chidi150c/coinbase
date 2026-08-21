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
//     • Case 4 Protected Profit: common protection for normal lots and runners
//     • Runner: bypass ordinary TP/threshold-stop-loss; exit only through Case 4
//     • Normal lot: retain ordinary fixed-TP and threshold-stop-loss behavior
//  3. OPEN evaluation (if no exit fired):
//     - Pull balances/steps with lock released
//     - Enforce MinNotional/OrderMinUSD and step/tick snapping symmetrically
//     - Equity triggers may use staged sizing; runner assignment is producer-owned
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
//     with optional exponential decay & latching. Equity triggers can stage sizes (25/50/75/100%).
//   - Runner designation is explicit producer intent carried through AssignRunner.
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
//   - TRACE/DEBUG breadcrumbs at key gates (spacing, latching, protected profit, post-only lifecycle).
//     Prometheus-style counters/gauges are updated for opens/exits.
//
// ---------------------------------------------------------------------------------------------
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

const Version = 175

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
		// log.Printf("[TRACE] fallback.buffer.full: empty the buffer (drop stale) and resending")
		select {
		case <-ch:
		default:
		}
		// log.Printf("[TRACE] fallback.buffer.emptied: emptied buffer and resending")
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
		// log.Printf("[TRACE] refund.sell.service_credited side=%s gross=%.8f fee=%.8f net=%.8f spareSell_after=%.8f",
		// side, refundQuote, refundFee, refundNet, t.SpareSellUSD)
		return
	}

	t.SpareBuyUSD += refundNet
	// log.Printf("[TRACE] refund.buy.service_credited side=%s gross=%.8f fee=%.8f net=%.8f spareBuy_after=%.8f",
	// side, refundQuote, refundFee, refundNet, t.SpareBuyUSD)
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

	// Use wall clock as authoritative "now" for pyramiding timings; fall back for zero candle time.
	wallNow := time.Now().UTC()

	now := execHistory[len(execHistory)-1].Time
	if now.IsZero() {
		now = wallNow
	}
	t.updateDaily(now)

	// Process completed asynchronous exits before making any new decisions.
	//
	// Each PendingExit owns its asynchronous completion channel.
	// Snapshot the registry under lock, then drain each exit without
	// holding the registry lock so completion may safely remove itself.
	exits := t.pendingExitsSnapshot()

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	for _, exit := range exits {
		t.drainPendingExit(
			ctx,
			exit,
			execHistory,
			livePrice,
		)
	}
	t.mu.Unlock()
	// Drain every completed asynchronous entry.
	//
	// Each PendingEntry decides for itself whether it is currently
	// eligible to commit (for example Case3A waits until its producer
	// exit has committed).
	entries := t.pendingEntriesSnapshot()

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	for _, entry := range entries {
		t.drainPendingEntry(
			entry,
			now,
			wallNow,
		)
	}

	if t.PendingReplacementRetry.Enabled {
		retry := t.PendingReplacementRetry
		repl := retry.Replacement

		sourceEntryOrderID :=
			strings.TrimSpace(repl.SourceEntryOrderID)

		// Mode B retry must wait until the originating losing position
		// has actually committed its exit.
		//
		// Do not rely only on WaitForExitOrderID disappearing from
		// pendingExits because maker-exit repricing can change that OrderID.
		if sourceEntryOrderID != "" &&
			t.positionExistsByEntryOrderID(sourceEntryOrderID) {

			// log.Printf(
			// "[TRACE] Case3A.retry.wait "+
			// "source_entry_id=%s wait_exit_id=%s reason=%s",
			// sourceEntryOrderID,
			// retry.WaitForExitOrderID,
			// retry.Reason,
			// )

		} else {
			/*
				The retry is a fresh Case3A producer lifecycle.

				Create its lifecycle identity before entering the wrapper.
				The wrapper will enrich this same ProducerAttempt with
				produced / pending / failure / cleanup events.
			*/
			attempt :=
				newProducerIntentLifecycle(
					&repl,
				)

			if attempt == nil {
				log.Printf(
					"[ERROR] Case3A.retry.lifecycle_create_failed "+
						"source_entry_id=%s",
					sourceEntryOrderID,
				)

				t.PendingReplacementRetry.Enabled = false

				return StepResult{
					Msg: "HOLD",
				}, nil
			}

			// produceEntry() ultimately calls registerPendingEntry(),
			// which acquires t.mu. Never call it while step() owns t.mu.
			t.mu.Unlock()

			orderID, err :=
				t.startCase3AReplacement(
					ctx,
					&repl,
					attempt,
				)

			t.mu.Lock()

			/*
				The wrapper enriched the SAME ProducerAttempt created above.

				Record it before applying retry-success / retry-failure policy.
			*/
			t.recordProducerAttemptLocked(
				attempt,
			)
			if err := t.saveProducerHistoryNoLock(); err != nil {
				log.Printf(
					"[WARN] producer history save failed "+
						"producer=%s decision_id=%s err=%v",
					attempt.Producer,
					attempt.DecisionID,
					err,
				)
			}

			if err != nil {
				// log.Printf(
				// "[TRACE] Case3A.retry.failed "+
				// "method=%s source_entry_id=%s err=%v",
				// repl.RecoveryMethod.String(),
				// sourceEntryOrderID,
				// err,
				// )

			} else {
				log.Printf(
					"[TRACE] Case3A.retry.started "+
						"method=%s source_entry_id=%s replacement_order_id=%s",
					repl.RecoveryMethod.String(),
					sourceEntryOrderID,
					orderID,
				)

				t.PendingReplacementRetry.Enabled = false

				if err := t.saveStateNoLock(); err != nil {
					// log.Printf(
					// "[TRACE] Case3A.retry.state_save_failed "+
					// "replacement_order_id=%s err=%v",
					// orderID,
					// err,
					// )
				}
			}
		}
	}

	// Fresh-state initialization for the equity strategy baseline.
	// Run once after valid live equity is available.
	if t.lastAddEquity <= 0 && t.equityUSD > 0 {
		t.lastAddEquity = t.equityUSD
		t.equityStageBuy = 0
		t.equityStageSell = 0

		// log.Printf(
		// "[TRACE] equity.baseline.initialized equity=%.2f",
		// t.lastAddEquity,
		// )

		if err := t.saveStateNoLock(); err != nil {
			// log.Printf(
			// "[TRACE] equity.baseline.initial_state_save_failed err=%v",
			// err,
			// )
		}
	}

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	_ = t.shouldRefit(len(execHistory)) // intentionally unused here (guard only)

	log.Printf("[TRACE] hotpath.after_drain elapsed_ms=%d",
		time.Since(hotStart).Milliseconds())

	// TODO: remove TRACE
	lsb := len(t.book(SideBuy).Lots)
	lss := len(t.book(SideSell).Lots)
	// log.Printf("[TRACE] step.start ts=%s livePrice=%.8f candleClose=%.8f lotsBuy=%d lotsSell=%d lastAddBuy=%s lastAddSell=%s winLowBuy=%.8f winHighSell=%.8f latchedGateBuy=%.8f latchedGateSell=%.8f recentLow=%.8f recentHigh=%.8f elapsed_Hours_Buy=%.1f elapsed_Hours_Sell=%.1f",
	// now.Format(time.RFC3339), livePrice, execHistory[len(execHistory)-1].Close, lsb, lss,
	// t.lastAddBuy.Format(time.RFC3339), t.lastAddSell.Format(time.RFC3339), t.winLowBuy, t.winHighSell, t.latchedGateBuy, t.latchedGateSell, t.RecentLow, t.RecentHigh, time.Since(t.lastAddBuy).Hours(), time.Since(t.lastAddSell).Hours())

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
	// applies Case 4 protected-profit behavior plus normal-lot exits, and closes at most ONE lot per step.
	//
	// Important:
	// - Profit gate is per-lot. AI-FLAT entries may have a reduced ProfitGateUSD.
	// - Normal lots retain ordinary fixed-TP and threshold-stop-loss handling.
	// - Runners bypass ordinary TP and threshold-stop-loss and exit only through Case 4.
	// - nearestTakeBuy/Sell are diagnostic/Gate2 snapshots, not separate exit orders.
	// --------------------------------------------------------------------------------------------------------
	if (lsb > 0) || (lss > 0) {

		nearestTakeBuy := 0.0
		nearestTakeSell := 0.0
		buyNearestIdx, sellNearestIdx := -1, -1
		buyModeLabel, sellModeLabel := "n/a", "n/a"
		buyNet, sellNet := 0.0, 0.0
		feeRatePct := t.cfg.FeeRatePct

		// Track nearest fee-aware profit-gate price per side.
		//
		// BUY side:
		//
		//	lowest candidate price is nearest.
		//
		// SELL side:
		//
		//	highest candidate price is nearest.
		//
		// For an unarmed lot, preview the fee-aware price required to reach
		// that lot's ProfitGateUSD. This applies equally to normal lots and runners.
		//
		// Used for diagnostics/Gate2 context only.
		updateNearest := func(
			book *SideBook,
			side OrderSide,
			idx int,
			lot *Position,
			net float64,
			price float64,
		) {
			if lot == nil {
				return
			}

			cand := lot.Take

			if cand <= 0 {
				gateUSD := t.lotProfitGateUSD(lot)

				if gateUSD > 0 {
					cand =
						activationPrice(
							lot,
							gateUSD,
							feeRatePct,
						)
				}
			}

			if cand <= 0 {
				return
			}

			mode := "ScalpFixedTP"

			if isRunner(book, idx) {
				mode = "Runner"
			}

			if side == SideBuy {
				if nearestTakeBuy == 0 ||
					cand < nearestTakeBuy {

					nearestTakeBuy = cand
					buyNearestIdx = idx
					buyModeLabel = mode
					buyNet = net
				}

				return
			}

			if side == SideSell {
				if nearestTakeSell == 0 ||
					cand > nearestTakeSell {

					nearestTakeSell = cand
					sellNearestIdx = idx
					sellModeLabel = mode
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

		// Refresh the fee-aware profit-gate Take preview.
		//
		// All lots use their own ProfitGateUSD to derive the preview price.
		// Runner status is managed separately through RunnerIDs.
		//
		// When Case 4 later arms, Take may be replaced with the
		// maker-friendly protected-profit exit price.
		refreshTakePreview := func(
			lot *Position,
		) {
			if lot == nil {
				return
			}

			gateUSD := t.lotProfitGateUSD(lot)

			lot.ExitMode = ExitModeScalpFixedTP

			lot.Take =
				activationPrice(
					lot,
					gateUSD,
					t.cfg.FeeRatePct,
				)
		}

		var stopL2 []exitCandidate
		var stopL1 []exitCandidate
		var profitL2 []exitCandidate
		var profitL1 []exitCandidate

		// Scan one side book and close at most one lot.
		//
		// Flow:
		// 1. Refresh fee-aware Take preview
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

				// refresh fee-aware profit-gate preview
				refreshTakePreview(lot)

				// compute gate

				net, pass := computeGate(lot)

				// gather nearest TAKE/mode/net while we're already here (no extra loops later)
				updateNearest(book, side, i, lot, net, price)

				gateUSD := t.lotProfitGateUSD(lot)

				// Case 4: once any lot reaches its profit gate, protect the gain.
				//
				// This protection is common to both normal lots and runners.
				// A runner remains open until Case 4 produces a protected-profit exit.
				case4Exit = false
				protectedFloor = 0.0

				if net >= gateUSD {
					if !lot.ProfitTrailActive {
						lot.ProfitTrailActive = true
						lot.ProfitPeakUSD = net

						// log.Printf(
						// 	"[TRACE] case4.armed side=%s idx=%d entry_id=%s net=%.6f gate=%.6f runner=%t",
						// 	lot.Side,
						// 	i,
						// 	lot.EntryOrderID,
						// 	net,
						// 	gateUSD,
						// 	isRunner(book, i),
						// )
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

					case4Exit =
						net > 0 &&
							net < protectedFloor
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
						makerExitPx =
							price *
								(1.0 + offBps/10000.0)
					}

					if lot.Side == SideSell && offBps > 0 {
						makerExitPx =
							price *
								(1.0 - offBps/10000.0)
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

					profitL2 = append(
						profitL2,
						cand,
					)

					// log.Printf(
					// 	"[TRACE] case4.protection_exit side=%s idx=%d entry_id=%s net=%.6f gate=%.6f peak=%.6f floor=%.6f take=%.8f runner=%t",
					// 	lot.Side,
					// 	i,
					// 	lot.EntryOrderID,
					// 	net,
					// 	gateUSD,
					// 	lot.ProfitPeakUSD,
					// 	protectedFloor,
					// 	lot.Take,
					// 	isRunner(book, i),
					// )

					// Do not clear ProfitTrailActive/ProfitPeakUSD here. The exit has
					// only been queued; if submission fails, Case 4 must remain armed
					// so the next tick can retry protection.

					i++
					continue
				}

				// Runner positions are managed exclusively by Case 4.
				// If Case 4 did not request an exit this tick, keep the runner open.
				// Runners do not enter ordinary take-profit or threshold-stop-loss paths.
				if isRunner(book, i) {
					lot.FixedTPWorking = false
					i++
					continue
				}

				if lot.ProfitTrailActive && net <= 0 {
					// Case 4 was armed, but profit moved through the positive protected
					// range before an exit could be scheduled. This is a normal lot, so
					// ordinary loss-management remains available below.
					// Keep Case 4 state intact so a later profitable recovery can still
					// be evaluated against the previously established protected floor.
				}

				strongProfitExit := net >= gateUSD*strongProfitMult

				pass = net >= gateUSD

				// Must be profitable first
				// Profit gate must pass before any exit action.
				// If the gate is not passed, normal-lot loss management may apply.
				if !pass {

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
								// log.Printf("[TRACE] stop_l1.post side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
							} else {
								// log.Printf("[TRACE] stop_l1.repost side=%s idx=%d price=%.8f net=%.6f", lot.Side, i, lot.Take, net)
							}
						}
						i++
						continue
					}

					i++
					continue
				}

				// Profit gate passed for a normal lot.
				// Apply ordinary fixed-TP behavior.
				switch lot.ExitMode {
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

					// log.Printf(
					// "[TRACE] exit.allow lot_side=%s idx=%d net=%.4f gate=%.6f "+
					// "entry_id=%s livePrice=%.8f mode=%s exitReason=%s "+
					// "strongProfit=%t exitClass=%s",
					// lot.Side,
					// i,
					// net,
					// gateUSD,
					// lot.EntryOrderID,
					// livePrice,
					// lot.ExitMode,
					// exitD.ExitReason,
					// strongProfitExit,
					// exitD.ExitClass,
					// )

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

						// log.Printf(
						// "[TRACE] tp.post side=%s idx=%d price=%.8f net=%.6f entry_id=%s",
						// lot.Side,
						// i,
						// lot.Take,
						// net,
						// lot.EntryOrderID,
						// )
					} else {
						// log.Printf(
						// "[TRACE] tp.repost side=%s idx=%d price=%.8f net=%.6f entry_id=%s",
						// lot.Side,
						// i,
						// lot.Take,
						// net,
						// lot.EntryOrderID,
						// )
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

					// log.Printf(
					// "[TRACE] tp.queue side=%s idx=%d price=%.8f net=%.6f "+
					// "exit_class=%s entry_id=%s",
					// lot.Side,
					// i,
					// lot.Take,
					// net,
					// exitD.ExitClass,
					// lot.EntryOrderID,
					// )

					i++
					continue

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
			// log.Printf(
			// "[TRACE] exit.fanout.batch candidates=%d stop_l2=%d profit_l2=%d stop_l1=%d profit_l1=%d",
			// len(selected),
			// len(stopL2),
			// len(profitL2),
			// len(stopL1),
			// len(profitL1),
			// )

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

					// log.Printf(
					// "[TRACE] exit.fanout.failed side=%s entry_id=%s reason=%s err=%v",
					// res.Side,
					// res.EntryOrderID,
					// res.Reason,
					// res.Err,
					// )

					continue
				}

				succeeded++

				// log.Printf(
				// "[TRACE] exit.fanout.done side=%s entry_id=%s reason=%s msg=%q",
				// res.Side,
				// res.EntryOrderID,
				// res.Reason,
				// res.Msg,
				// )

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

	log.Printf(
		"[TRACE] hotpath.after_exit_scan elapsed_ms=%d",
		time.Since(hotStart).Milliseconds(),
	)

	feeMult := 1.0 + (t.cfg.FeeRatePct / 100.0)

	// Sum reserved base for live long lots.
	var reservedLongBase float64

	if bb := t.book(SideBuy); bb != nil {
		for _, lot := range bb.Lots {
			if lot != nil {
				reservedLongBase += lot.SizeBase
			}
		}
	}

	// Compute reserved quote for live short lots.
	var reservedShortQuoteWithFee float64

	if sb := t.book(SideSell); sb != nil {
		for _, lot := range sb.Lots {
			if lot == nil {
				continue
			}

			q := lot.SizeBase * price
			reservedShortQuoteWithFee += q * feeMult
		}
	}

	// Include reservations held by all active asynchronous entries.
	//
	// Pending SELL entries reserve base when short entries require existing base.
	// Pending BUY entries reserve quote, including estimated entry fees.
	// Include reservations held by all active asynchronous entries.
	// t.mu is already held here, so inspect the registry directly.
	for _, entry := range t.pendingEntries {
		if entry == nil ||
			entry.Completed ||
			entry.Intent == nil {
			continue
		}

		intent := entry.Intent

		switch entry.Side {
		case SideSell:
			if t.cfg.RequireBaseForShort {
				reservedLongBase += intent.BaseAtLimit
			}

		case SideBuy:
			reservedShortQuoteWithFee += intent.Quote * feeMult
		}
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
		// log.Printf(
		// "[TRACE] case5.ai.failed elapsed_ms=%d err=%v",
		// aiResult.Elapsed.Milliseconds(),
		// aiResult.Err,
		// )

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    Flat,
			Signal: Flat,
		}, nil
	}
	// Reset Case13A on the AI transition itself, before any later evaluator
	// can cause this tick to return early. afterStepStateUpdate() writes this
	// tick's Raw only after step() returns, so t.previousAIRaw is still the
	// preceding tick here.
	if t.previousAIRaw != Buy &&
		aiResult.Raw == Buy {
		t.case13AReferencePrice = 0
	}

	if macdSnapshot.Err != nil {
		// log.Printf(
		// "[TRACE] case5.macd.failed elapsed_ms=%d err=%v",
		// macdSnapshot.Elapsed.Milliseconds(),
		// macdSnapshot.Err,
		// )

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    aiResult.Raw,
			Signal: Flat,
		}, nil
	}
	if emaResult.Err != nil {
		// log.Printf(
		// "[TRACE] case5.ema.failed elapsed_ms=%d err=%v",
		// emaResult.Elapsed.Milliseconds(),
		// emaResult.Err,
		// )

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    aiResult.Raw,
			Signal: Flat,
		}, nil
	}
	if pyramidRaw.Err != nil {
		// log.Printf(
		// "[TRACE] case5.pyramid.failed elapsed_ms=%d err=%v",
		// pyramidRaw.Elapsed.Milliseconds(),
		// pyramidRaw.Err,
		// )

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
	eps, regimeEPS, baseEPS := computeLogicEPS(
		t.cfg.MACDLineEPS,
		aiResult.Raw,
		aiResult.Confidence,
		t.MarketRegime,
		regimeMult,
	)
	macdResult := interpretMACD(
		macdSnapshot,
		eps,
		regimeEPS,
		baseEPS,
	)

	// ----------------------------------------------------------
	// 5.1. Interpret PyramidRaw after AI arrives
	// -----------------------------------------------------------
	pyramidResult := interpretPyramidRaw(
		pyramidRaw,
		aiResult.Confidence,
	)
	if pyramidResult.Err != nil {
		// log.Printf(
		// "[TRACE] case5.pyramid_interpret.failed elapsed_ms=%d err=%v",
		// pyramidResult.Elapsed.Milliseconds(),
		// pyramidResult.Err,
		// )

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

	// Preserve the original selected-side Pyramid state behavior using
	// the decision that existed before the Case 5 override stage.
	t.applyPyramidDecisionTransitions(
		pyramidResult,
	)

	equityResult, _ := t.evaluateEquityProducerMaterial(
		aiResult,
		macdResult,
		emaResult,
		balanceSnapshotMaxAge,
		reservedShortQuoteWithFee,
		reservedLongBase,
	)

	// log.Printf(
	// 	"[TRACE] case5.equity raw_ms=%d interpret_ms=%d legacy=%s "+
	// 		"buy_pass=%t sell_pass=%t buy_trigger=%t sell_trigger=%t "+
	// 		"buy_quote=%.8f sell_base=%.8f reason=%s",
	// 	equityRaw.Elapsed.Milliseconds(),
	// 	equityResult.Elapsed.Milliseconds(),
	// 	d.Signal,
	// 	equityRaw.BuyThresholdPassed,
	// 	equityRaw.SellThresholdPassed,
	// 	equityResult.BuyTrigger,
	// 	equityResult.SellTrigger,
	// 	equityResult.ProposedBuyQuote,
	// 	equityResult.ProposedSellBase,
	// 	equityResult.Reason,
	// )

	pendingCounts :=
		t.pendingProducerCountsNoLock()

	// Case 5 may retain or override the legacy AI + Logic decision using
	// the complete AI, MACD, EMA and Pyramid materials.
	entryDecision := t.combineEntryRawMaterials(
		aiResult,
		macdResult,
		emaResult,
		pyramidResult,
		equityResult,
		price,
		pendingCounts,
	)

	log.Printf(
		"[TRACE] hotpath.after_decision elapsed_ms=%d",
		time.Since(hotStart).Milliseconds(),
	)

	d := entryDecision

	/*
		Gate Analysis telemetry reuses values already computed for this tick.

		The helper only decides whether this tick belongs to the next 10-second
		sample bucket. Disk persistence is asynchronous and does not hold t.mu.
	*/
	t.recordGateAnalysisPointLocked(
		wallNow,
		price,
		d.LogicEPS,
		d.LogicMACDTurn,
	)

	if d.Signal != Flat {
		t.applyPyramidRebaseTransactions(
			pyramidResult,
			d.Signal,
		)
	}

	intent, attempt := newProducerDecisionLifecycle(
		&d,
	)

	if attempt != nil &&
		intent != nil &&
		(d.Signal == Buy || d.Signal == Sell) {

		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageDecision,
			"",
			nil,
			false,
			false,
		)

		if event, exists :=
			attempt.Events[ProducerStageDecision]; exists {

			event.Price = price
			attempt.Events[ProducerStageDecision] = event
		}

		log.Printf(
			"[PRODUCER] stage=decision "+
				"producer=%s side=%s reason=%q",
			d.Producer,
			d.Signal,
			d.ProducerReason,
		)
	}

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

	side, ok := d.SignalToSide()
	if !ok {
		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageDecisionFailed,
			EntryProduceErrInvalidSide,
			fmt.Errorf(
				"decision signal cannot map to order side: %v",
				d.Signal,
			),
			false,
			true,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "FLAT",
			Raw:    d.Raw,
			Signal: d.Signal,
		}, nil
	}

	executionBalance, executionCacheOK :=
		t.getBalanceSpare(
			balanceSnapshotMaxAge,
			reservedShortQuoteWithFee,
			reservedLongBase,
		)

	if !executionCacheOK {
		ageMS := int64(-1)

		if !executionBalance.Snapshot.UpdatedAt.IsZero() {
			ageMS =
				time.Since(
					executionBalance.Snapshot.UpdatedAt,
				).Milliseconds()
		}

		log.Printf(
			"[WARN] balance.execution_cache.unavailable "+
				"side=%s source=%s final=%s age_ms=%d",
			side,
			d.Producer,
			d.Signal,
			ageMS,
		)

		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageDecisionFailed,
			EntryProduceErrDecisionBalanceUnavailable,
			fmt.Errorf(
				"execution balance unavailable age_ms=%d",
				ageMS,
			),
			false,
			true,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    d.Raw,
			Signal: d.Signal,
		}, nil
	}

	availQuote := executionBalance.AvailQuote
	quoteStep := executionBalance.QuoteStep
	availBase := executionBalance.AvailBase
	baseStep := executionBalance.BaseStep

	spare := 0.0

	switch side {
	case SideBuy:
		spare = executionBalance.SpareQuote

	case SideSell:
		spare = executionBalance.SpareBase
	}

	// log.Printf(
	// "[TRACE] balance.execution_cache.hit "+
	// "side=%s producer=%s final=%s age_ms=%d "+
	// "quote=%.8f quoteStep=%.8f base=%.8f baseStep=%.8f",
	// side,
	// d.Producer,
	// d.Signal,
	// time.Since(executionBalance.Snapshot.UpdatedAt).Milliseconds(),
	// availQuote,
	// quoteStep,
	// availBase,
	// baseStep,
	// )

	log.Printf(
		"[TRACE] hotpath.after_execution_balance elapsed_ms=%d final=%s",
		time.Since(hotStart).Milliseconds(),
		d.Signal,
	)
	// --------------------------------------------------------------------------------------------------------
	//---ADD path continues-----
	// --------------------------------------------------------------------------------------------------------

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

	// Prevent duplicate opens while pending on this side (exits already ran) ---
	// Extra belt-and-suspenders: if a pending exists and we haven't hit its Deadline, keep waiting.
	// if side == SideBuy && entry.Intent != nil && time.Now().Before(entry.Intent.Deadline) {
	// 	t.mu.Unlock()
	// 	return StepResult{Msg: "OPEN-PENDING side=BUY", Raw: d.Raw, Signal: d.Signal}, nil
	// }
	// if side == SideSell && entry.Intent != nil && time.Now().Before(entry.Intent.Deadline) {
	// 	t.mu.Unlock()
	// 	return StepResult{Msg: "OPEN-PENDING side=SELL", Raw: d.Raw, Signal: d.Signal}, nil
	// }

	// -----------------------------------------------------------------------------
	// Case 3B-Opposite - DOWN-Regime BUY Protection
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

			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageDecisionBlocked,
				EntryProduceErrDecisionCase3BBlocked,
				fmt.Errorf(
					"Case3B BUY blocked above previous threshold-stop loss exit price %.8f",
					last.ClosePrice,
				),
				false,
				true,
			)

			t.mu.Unlock()
			return StepResult{Msg: "HOLD Case3B block BUY above last loss-exit SELL price", Raw: d.Raw, Signal: d.Signal}, nil
		}
	}
	// -----------------------------------------------------------------------------
	// Case 3B - UP-Regime SELL Protection
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

			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageDecisionBlocked,
				EntryProduceErrDecisionCase3BBlocked,
				fmt.Errorf(
					"Case3B SELL blocked below previous threshold-stop loss exit price %.8f",
					last.ClosePrice,
				),
				false,
				true,
			)

			t.mu.Unlock()
			return StepResult{Msg: "HOLD Case3B block SELL below last loss-exit BUY price", Raw: d.Raw, Signal: d.Signal}, nil
		}
	}

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageDecisionBlocked,
			EntryProduceErrDecisionLongOnlyBlocked,
			fmt.Errorf(
				"SELL blocked because LongOnly is enabled",
			),
			false,
			true,
		)
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
			// log.Printf("[TRACE] consolidate.startup done px=%.8f minNotional=%.2f", price, minNotional)
		}
		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageDecisionBlocked,
			EntryProduceErrDecisionLotCapReached,
			fmt.Errorf(
				"entry blocked by max concurrent lot cap current=%d max=%d",
				lsb+lss,
				t.cfg.MaxConcurrentLots,
			),
			false,
			true,
		)
		t.mu.Unlock()
		log.Printf("[DEBUG] GATE1 lot cap reached (%d); HOLD", t.cfg.MaxConcurrentLots)
		return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
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

		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageDecisionFailed,
			EntryProduceErrDecisionInvalidConfidence,
			fmt.Errorf(
				"decision confidence must be > 0: %.8f",
				confMult,
			),
			false,
			true,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "HOLD",
			Raw:    d.Raw,
			Signal: d.Signal,
		}, nil
	}

	entryAIMode :=
		string(d.Producer)

	if entryAIMode == "" {
		entryAIMode = "UNKNOWN"
	}

	// Calculate the ordinary confidence-adjusted target first.
	baseEntryProfitGateUSD :=
		t.cfg.ProfitGateUSD * confMult

	if baseEntryProfitGateUSD < 0.30 {
		baseEntryProfitGateUSD = 0.30
	}

	// Producers may reduce or increase the ordinary target.
	// A zero/unset multiplier preserves existing behavior.
	profitGateMultiplier :=
		d.ProfitGateMultiplier

	if profitGateMultiplier <= 0 {
		profitGateMultiplier = 1.0
	}

	producerProfitGateUSD :=
		baseEntryProfitGateUSD *
			profitGateMultiplier

	// Recovery debt is independent of producer target policy and must not
	// be reduced by the producer multiplier.
	recoveryAddUSD :=
		t.recoveryTargetAddUSD()

	entryProfitGateUSD :=
		producerProfitGateUSD +
			recoveryAddUSD

	// log.Printf(
	// "[TRACE] entry.profit_gate "+
	// "producer=%s confidence=%.2f "+
	// "base_usd=%.4f multiplier=%.4f "+
	// "producer_usd=%.4f recovery_add_usd=%.4f "+
	// "resolved_usd=%.4f",
	// d.Producer,
	// confMult,
	// baseEntryProfitGateUSD,
	// profitGateMultiplier,
	// producerProfitGateUSD,
	// recoveryAddUSD,
	// entryProfitGateUSD,
	// )

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
	// log.Printf("[TRACE] sizing.pre side=%s eq=%.2f quote=%.2f price=%.8f base=%.8f", side, t.equityUSD, quote, price, base)

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// -----------------------------------------------------------------------------------------------
	// --- Spare and Reservation Inventory ---
	// -----------------------------------------------------------------------------------------------
	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// TODO: remove TRACE
		// log.Printf("[TRACE] buy.gate.pre availQuote=%.2f reservedShort=%.2f needQuoteRaw=%.2f quoteStep=%.8f",
		// availQuote, reservedShortQuoteWithFee, quote, quoteStep)

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
					// log.Printf("[TRACE] buy.gate.block minNotional need=%.2f spare=%.2f", neededQuote, spare)

					short := neededQuote - spare
					if short > 0 {
						// remember that a BUY was blocked by this amount
						t.refundBuyUSD = short
					}
					t.addDecisionProducerEvent(
						intent,
						attempt,
						ProducerStageDecisionFailed,
						EntryProduceErrDecisionInsufficientFunds,
						fmt.Errorf(
							"BUY insufficient funds after min-notional adjustment: needed_quote=%.8f spare_quote=%.8f min_notional=%.8f",
							neededQuote,
							spare,
							minNotional,
						),
						false,
						true,
					)
					t.mu.Unlock()
					return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
				}
			}

			// Use the final neededQuote; recompute base.
			quote = neededQuote
			base = quote / price

			// log.Printf("[TRACE] buy.gate.post needQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
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
				t.addDecisionProducerEvent(
					intent,
					attempt,
					ProducerStageDecisionFailed,
					EntryProduceErrDecisionInsufficientFunds,
					fmt.Errorf(
						"BUY insufficient funds after degrade-to-spare: requested_quote=%.8f usable_quote=%.8f spare_quote=%.8f min_notional=%.8f",
						neededQuote,
						useQuote,
						spare,
						minNotional,
					),
					false,
					true,
				)
				t.mu.Unlock()
				return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
			}

			// SUCCESSFUL DEGRADE-TO-SPARE PATH
			quote = useQuote
			base = quote / price

			// add sizing_reduced event HERE

			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageSizingReduced,
				"",
				nil,
				false,
				false,
			)

			// log.Printf("[TRACE] buy.gate.post.degraded useQuote=%.2f spare=%.2f base=%.8f", quote, spare, base)
		}
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && t.cfg.RequireBaseForShort {
		// TODO: remove TRACE
		// log.Printf("[TRACE] sell.gate.pre availBase=%.8f reservedLong=%.8f needBaseRaw=%.8f baseStep=%.8f",
		// availBase, reservedLongBase, base, baseStep)

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
					// log.Printf("[TRACE] sell.gate.block minNotional need=%.8f spare=%.8f", base, spare)

					// convert the short to USD at current price so we can reuse later on BUY
					shortBase := base - spare
					shortUSD := shortBase * price
					if shortUSD > 0 {
						t.refundSellUSD = shortUSD
					}

					t.addDecisionProducerEvent(
						intent,
						attempt,
						ProducerStageDecisionFailed,
						EntryProduceErrDecisionInsufficientFunds,
						fmt.Errorf(
							"SELL insufficient funds after min-notional adjustment: needed_base=%.8f spare_base=%.8f min_notional=%.8f",
							base,
							spare,
							minNotional,
						),
						false,
						true,
					)

					t.mu.Unlock()
					return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
				}
			}

			// log.Printf("[TRACE] sell.gate.post needBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
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

				t.addDecisionProducerEvent(
					intent,
					attempt,
					ProducerStageDecisionFailed,
					EntryProduceErrDecisionInsufficientFunds,
					fmt.Errorf(
						"SELL insufficient funds after degrade-to-spare: requested_base=%.8f usable_base=%.8f spare_base=%.8f min_notional=%.8f",
						neededBase,
						useBase,
						spare,
						minNotional,
					),
					false,
					true,
				)

				t.mu.Unlock()
				return StepResult{Msg: "HOLD", Raw: d.Raw, Signal: d.Signal}, nil
			}

			// SUCCESSFUL DEGRADE-TO-SPARE PATH
			base = useBase
			quote = base * price

			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageSizingReduced,
				"",
				nil,
				false,
				false,
			)

			// log.Printf("[TRACE] sell.gate.post.degraded useBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)

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

			gatesReason := d.ProducerReason
			if t.refundBuyUSD == 0 {
				d.ProducerReason = strings.TrimSpace(gatesReason + "|refund=buy-full")

			} else {
				d.ProducerReason = strings.TrimSpace(gatesReason + "|refund=buy-partial")
			}
			intent.ProducerReason =
				d.ProducerReason
		}
	} else if t.refundBuyUSD > 0 && side == SideSell && confMult < refundMinConf {
		// log.Printf("[TRACE] refund.block side=%s conf=%.2f need>=%.2f refundBuyUSD=%.2f",
		// side, confMult, refundMinConf, t.refundBuyUSD)
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

			gatesReason := d.ProducerReason
			if t.refundSellUSD == 0 {
				d.ProducerReason = strings.TrimSpace(gatesReason + "|refund=sell-full")
			} else {
				d.ProducerReason = strings.TrimSpace(gatesReason + "|refund=sell-partial")
			}
			intent.ProducerReason =
				d.ProducerReason
		}
	} else if t.refundSellUSD > 0 && side == SideBuy && confMult < refundMinConf {
		// log.Printf("[TRACE] refund.block side=%s conf=%.2f need>=%.2f refundSellUSD=%.2f",
		// side, confMult, refundMinConf, t.refundSellUSD)
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
	// A timed-out maker attempt sets a side-level one-shot preference.
	// The next valid entry attempt on that side skips maker once.
	recheckNow := false

	t.mu.Lock()

	switch side {
	case SideBuy:
		recheckNow = t.pendingRecheckBuy
		if recheckNow {
			t.pendingRecheckBuy = false
		}

	case SideSell:
		recheckNow = t.pendingRecheckSell
		if recheckNow {
			t.pendingRecheckSell = false
		}
	}

	t.mu.Unlock()

	if wantLimit && recheckNow {
		wantLimit = false

		log.Printf(
			"[TRACE] postonly.skip reason=recheck_market_next_tick side=%s",
			side,
		)
	}

	// Maker-first entry production now routes through the unified producer:
	//
	//	step()
	//	  → startProducerBuyEntry()/startProducerSellEntry()
	//	  → PendingIntent
	//	  → produceEntry()
	//	  → submitPendingIntent()
	//	  → buildPendingEntry()
	//	  → registerPendingEntry()
	//	  → startEntryPoller()
	if wantLimit {
		var limitPx float64

		if side == SideBuy {
			limitPx = price * (1.0 - offsetBps/10000.0)
		} else {
			limitPx = price * (1.0 + offsetBps/10000.0)
		}

		// Preserve existing side-aware tick snapping.
		tick := t.cfg.PriceTick
		if tick > 0 {
			if side == SideBuy {
				limitPx =
					math.Floor(limitPx/tick) *
						tick
			} else {
				limitPx =
					math.Ceil(limitPx/tick) *
						tick
			}
		}

		if limitPx <= 0 {
			log.Printf(
				"[DEBUG] postonly.invalid_limit "+
					"side=%s limit=%.8f live=%.8f",
				side,
				limitPx,
				price,
			)

			t.mu.Lock()

			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageDecisionFailed,
				EntryProduceErrInvalidPrice,
				fmt.Errorf(
					"invalid maker limit price: side=%s limit_price=%.8f live_price=%.8f",
					side,
					limitPx,
					price,
				),
				false,
				true,
			)

			t.mu.Unlock()

			return StepResult{
				Msg:    "HOLD",
				Raw:    d.Raw,
				Signal: d.Signal,
			}, nil
		}

		baseAtLimit := quote / limitPx

		// Preserve existing base-step snapping.
		if t.cfg.BaseStep > 0 {
			baseAtLimit =
				math.Floor(baseAtLimit/t.cfg.BaseStep) *
					t.cfg.BaseStep
		}

		log.Printf(
			"[TRACE] hotpath.before_submit "+
				"elapsed_ms=%d side=%s limit=%.2f live=%.2f",
			time.Since(hotStart).Milliseconds(),
			side,
			limitPx,
			price,
		)

		if baseAtLimit > 0 &&
			baseAtLimit*limitPx >= minNotional {

			log.Printf(
				"[TRACE] postonly.place "+
					"side=%s limit=%.8f baseReq=%.8f timeout_sec=%d",
				side,
				limitPx,
				baseAtLimit,
				limitWait,
			)

			/*
				Complete the execution-specific fields on the SAME PendingIntent
				created at Decision stage.

				Do not create a new intent or ProducerAttempt here.
			*/
			intent.Side = side
			intent.LimitPx = limitPx
			intent.BaseAtLimit = baseAtLimit
			intent.Quote = baseAtLimit * limitPx
			intent.Take = take

			intent.ProductID = t.cfg.ProductID
			intent.EntryMethod = entryAIMode

			intent.RefundPortionUSD =
				refundFromOpposite

			intent.ConfidenceMult =
				confMult

			intent.ProfitGateUSD =
				entryProfitGateUSD

			intent.PendingCancelPolicy =
				d.PendingCancelPolicy

			intent.ProducerReason =
				d.ProducerReason

			// Runner ownership is resolved by the producer decision. Execution
			// carries that instruction forward without inferring it from Equity.
			intent.AssignRunner =
				d.AssignRunner

			if intent.History == nil {
				intent.History =
					make([]string, 0, 5)
			}

			var (
				entry *PendingEntry
				err   error
			)

			switch side {
			case SideBuy:
				entry, err =
					t.startProducerBuyEntry(
						ctx,
						intent,
						attempt,
					)

			case SideSell:
				entry, err =
					t.startProducerSellEntry(
						ctx,
						intent,
						attempt,
					)

			default:
				err = fmt.Errorf(
					"unsupported entry side: %s",
					side,
				)
			}

			/*
				The BUY/SELL source wrapper has now completed the portion of the
				producer lifecycle that it owns.

				On success, attempt contains:
				  - produced
				  - pending

				On entry-production failure, attempt contains:
				  - produced
				  - entry_failed
				  - cleanup_cancelled or cleanup_cancel_failed, when applicable

				This Step-level caller is above the source wrapper, so it owns
				recording that completed/current ProducerAttempt into producerHistory.

				recordProducerAttemptLocked() requires t.mu.
			*/
			if attempt != nil {
				t.mu.Lock()
				t.recordProducerAttemptLocked(attempt)
				if err := t.saveProducerHistoryNoLock(); err != nil {
					log.Printf(
						"[WARN] producer history save failed "+
							"producer=%s decision_id=%s err=%v",
						attempt.Producer,
						attempt.DecisionID,
						err,
					)
				}
				t.mu.Unlock()
			}

			if err == nil && entry != nil {
				log.Printf(
					"[TRACE] hotpath.order.done "+
						"elapsed_ms=%d orderID=%s",
					time.Since(hotStart).Milliseconds(),
					entry.OrderID,
				)

				log.Printf(
					"[TRACE] postonly.pending.set "+
						"producer=%s side=%s order_id=%s "+
						"limit=%.8f base=%.8f quote=%.2f "+
						"dl=%s assign_runner=%t",
					entry.Producer,
					entry.Side,
					entry.OrderID,
					entry.Intent.LimitPx,
					entry.Intent.BaseAtLimit,
					entry.Intent.Quote,
					entry.Intent.Deadline.Format(time.RFC3339),
					entry.Intent.AssignRunner,
				)

				return StepResult{
					Msg: fmt.Sprintf(
						"OPEN-PENDING side=%s",
						side,
					),
					Raw:    d.Raw,
					Signal: d.Signal,
				}, nil
			}

			if err != nil {
				log.Printf(
					"[DEBUG] postonly.error "+
						"hold_for_recheck side=%s err=%v",
					side,
					err,
				)

				/*
					The wrapper has already completed this producer lifecycle:

						decision
						→ produced
						→ entry_failed
						→ optional cleanup stage

					and the attempt was recorded above.

					Do not continue this same failed lifecycle into market fallback
					or append another terminal/deferred outcome.
				*/
				return StepResult{
					Msg:    "HOLD",
					Raw:    d.Raw,
					Signal: d.Signal,
				}, nil
			}

			if entry == nil {
				/*
					Nil entry with nil error is an internal invariant violation.
					The normal wrappers should never return this combination.
				*/
				t.mu.Lock()

				t.addDecisionProducerEvent(
					intent,
					attempt,
					ProducerStageEntryFailed,
					EntryProduceErrBuildOrder,
					fmt.Errorf(
						"producer wrapper returned nil PendingEntry with nil error",
					),
					false,
					true,
				)

				t.mu.Unlock()

				return StepResult{
					Msg:    "HOLD",
					Raw:    d.Raw,
					Signal: d.Signal,
				}, nil
			}

		} else {
			/*
				The maker order became ineligible after base-step snapping.

				No maker submission occurred, so there is nothing to wait for
				or recheck. Allow this SAME Decision lifecycle to continue
				directly to the market fallback path.
			*/
			log.Printf(
				"[DEBUG] postonly.skip "+
					"reason=snapped_size_below_min "+
					"side=%s base=%.8f limit=%.8f notional=%.8f min_notional=%.8f",
				side,
				baseAtLimit,
				limitPx,
				baseAtLimit*limitPx,
				minNotional,
			)

			wantLimit = false
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

	// If maker path did not result in a fill (or was skipped), fall back to market path.
	if placed == nil {
		if !allowMarket {
			log.Printf(
				"[DEBUG] postonly.market_fallback.blocked "+
					"side=%s reason=recheck_flag_not_set",
				side,
			)

			t.mu.Lock()

			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageDecisionDeferred,
				"",
				nil,
				false,
				true,
			)

			t.mu.Unlock()

			return StepResult{
				Msg:    "HOLD",
				Raw:    d.Raw,
				Signal: d.Signal,
			}, nil
		}

		// before order submit
		log.Printf(
			"[TRACE] hotpath.before_submit.market_quote elapsed_ms=%d side=%s live=%.2f",
			time.Since(hotStart).Milliseconds(),
			side,
			price,
		)

		/*
		   The direct market fallback is also a producer production attempt.

		   It therefore uses the SAME Decision-created ProducerAttempt and
		   adds stage=produced before submitting to the broker.
		*/
		if attempt != nil &&
			intent != nil {

			if attempt.Events == nil {
				attempt.Events =
					make(map[ProducerStage]ProducerEvent)
			}

			attempt.Events[ProducerStageProduced] =
				ProducerEvent{
					Time:      time.Now().UTC(),
					CreatedAt: intent.CreatedAt,

					Producer: intent.Producer,
					Side:     attempt.Side,
					Stage:    ProducerStageProduced,

					DecisionID: intent.DecisionID,

					Reason: intent.ProducerReason,
				}
		}

		var err error

		placed, err =
			t.broker.PlaceMarketQuote(
				ctx,
				t.cfg.ProductID,
				side,
				quote,
			)

		// TODO: remove TRACE
		log.Printf("[TRACE] order.open request side=%s quote=%.2f baseEst=%.8f priceSnap=%.8f take=%.8f",
			side, quote, base, price, take)
		log.Printf("[TRACE] postonly.market_fallback.go side=%s quote=%.2f", side, quote)
		// log.Printf("[KPI] taker.open side=%s quote=%.2f reason=market_fallback", side, quote)
		log.Printf("[TRACE] hotpath.order.done elapsed_ms=%d",
			time.Since(hotStart).Milliseconds())

		if err != nil {
			// Retry once with ORDER_MIN_USD on insufficient-funds style failures.

			if quote > minNotional &&
				isBinanceInsufficientBalance(err) {

				log.Printf(
					"[WARN] open order %.2f USD failed with Binance insufficient balance (%v); retrying with ORDER_MIN_USD=%.2f",
					quote,
					err,
					minNotional,
				)

				quote = minNotional
				base = quote / price

				// TODO: remove TRACE
				log.Printf(
					"[TRACE] order.open retry side=%s quote=%.2f baseEst=%.8f",
					side,
					quote,
					base,
				)

				placed, err =
					t.broker.PlaceMarketQuote(
						ctx,
						t.cfg.ProductID,
						side,
						quote,
					)
			}

			if err != nil {
				if t.cfg.UseDirectSlack {
					postSlack(
						fmt.Sprintf(
							"ERR step: %v",
							err,
						),
					)
				}

				code :=
					EntryProduceErrSubmitNetworkFailed

				var binanceErr *BinanceBridgeError

				if errors.As(
					err,
					&binanceErr,
				) {
					msg :=
						strings.TrimSpace(
							binanceErr.BinanceMsg,
						)

					switch {
					case (binanceErr.BinanceCode == -2010 ||
						binanceErr.BinanceCode == -1010) &&
						msg ==
							"Account has insufficient balance for requested action.":

						code =
							EntryProduceErrInsufficientBalance

					case binanceErr.BinanceCode == -1007:

						code =
							EntryProduceErrSubmitTimeout

					case binanceErr.BinanceCode == -1003:

						code =
							EntryProduceErrRateLimited

					default:
						code =
							EntryProduceErrExchangeRejected
					}
				}

				t.mu.Lock()

				t.addDecisionProducerEvent(
					intent,
					attempt,
					ProducerStageEntryFailed,
					code,
					fmt.Errorf(
						"direct market entry submission failed: side=%s quote=%.8f: %w",
						side,
						quote,
						err,
					),
					false,
					true,
				)

				t.mu.Unlock()

				return StepResult{
					Msg:    "",
					Raw:    d.Raw,
					Signal: d.Signal,
				}, err
			}

		}

		if attempt != nil &&
			intent != nil &&
			placed != nil {

			if attempt.Events == nil {
				attempt.Events =
					make(map[ProducerStage]ProducerEvent)
			}

			attempt.Events[ProducerStageFilled] =
				ProducerEvent{
					Time:      time.Now().UTC(),
					CreatedAt: intent.CreatedAt,

					Producer: intent.Producer,
					Side:     attempt.Side,
					Stage:    ProducerStageFilled,

					DecisionID: intent.DecisionID,
					OrderID:    placed.ID,

					Reason: intent.ProducerReason,
					Price:  placed.Price,
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
			// log.Printf("[TRACE] fill.open partial requested=%.8f filled=%.8f", baseRequested, baseToUse)
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
		OpenNotionalUSD:  actualQuote,      // <<< USD PERSISTENCE: notional in USD at open
		ProducerReason:   d.ProducerReason, // side-biased; no winLow
		Take:             take,
		Version:          Version,
		EntryOrderID:     placedOrderID(placed),
		RefundPortionUSD: refundFromOpposite,
		ConfidenceMult:   confMult,
		EntryMethod:      entryAIMode,
		ProfitGateUSD:    entryProfitGateUSD,
		Producer:         d.Producer,
	}

	// log.Printf(
	// "[KPI] lot.created producer=%s side=%s mode=%s conf=%.2f gate=%.2f",
	// newLot.Producer,
	// newLot.Side,
	// newLot.EntryMethod,
	// newLot.ConfidenceMult,
	// newLot.ProfitGateUSD,
	// )

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

	// Equity owns its baseline lifecycle. A successful fill from any other
	// producer must not restart or suppress the Equity trigger cycle.
	if d.Producer == EntryProducerEquity {
		oldEquityBaseline := t.lastAddEquity
		t.lastAddEquity = t.equityUSD
		log.Printf(
			"[TRACE] equity.baseline.set side=%s producer=%s old=%.2f new=%.2f",
			side,
			d.Producer,
			oldEquityBaseline,
			t.lastAddEquity,
		)
	}

	// Runner assignment is producer-owned. The direct-market path executes
	// the explicit decision instruction; it does not infer runner status
	// from Equity or from a generic policy.
	if d.AssignRunner {
		newIdx := len(book.Lots) - 1
		addRunner(book, newIdx)

		log.Printf(
			"[PRODUCER] runner_assigned producer=%s side=%s idx=%d order_id=%s",
			d.Producer,
			side,
			newIdx,
			newLot.EntryOrderID,
		)
	}

	msg := ""
	msg = fmt.Sprintf("[LIVE ORDER] %s notional=%.2f take=%.2f fee=%.4f reason=%s",
		side, newLot.OpenNotionalUSD, newLot.Take, entryFee, newLot.ProducerReason)

	if t.cfg.UseDirectSlack {
		postSlack(msg)
	}

	// Persist the newly committed local position state.
	if err := t.saveStateNoLock(); err != nil {
		log.Printf(
			"[WARN] saveState: %v",
			err,
		)

		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageEntryFailed,
			EntryProduceErrPersistState,
			fmt.Errorf(
				"direct market entry filled but local state persistence failed: order_id=%s: %w",
				placedOrderID(placed),
				err,
			),
			true,
			true,
		)

		t.mu.Unlock()

		return StepResult{
			Msg:    "",
			Raw:    d.Raw,
			Signal: d.Signal,
		}, err
	}

	// Only a successful Case13A fill/commit advances the reference, using
	// the actual fill price selected above.
	if d.Producer == EntryProducerCase13APeakSell {
		t.case13AReferencePrice = priceToUse
	}

	// Exchange fill is now represented by a successfully persisted local Position.
	// The producer lifecycle can therefore advance to committed.
	if attempt != nil &&
		intent != nil {

		if attempt.Events == nil {
			attempt.Events =
				make(map[ProducerStage]ProducerEvent)
		}

		attempt.Events[ProducerStageCommitted] =
			ProducerEvent{
				Time:      time.Now().UTC(),
				CreatedAt: intent.CreatedAt,

				Producer: intent.Producer,
				Side:     attempt.Side,
				Stage:    ProducerStageCommitted,

				DecisionID: intent.DecisionID,
				OrderID:    placedOrderID(placed),

				Reason: intent.ProducerReason,
			}

		t.recordProducerAttemptLocked(
			attempt,
		)

		if err := t.saveProducerHistoryNoLock(); err != nil {
			log.Printf(
				"[ERROR] producer.history.save_failed "+
					"stage=%s producer=%s decision_id=%s err=%v",
				ProducerStageCommitted,
				attempt.Producer,
				attempt.DecisionID,
				err,
			)
		}
	}

	// log.Printf("[KPI] summary equity=%.2f daily_pnl=%.2f lots_buy=%d lots_sell=%d product=%s",
	// t.equityUSD, t.dailyPnL, len(t.book(SideBuy).Lots), len(t.book(SideSell).Lots), t.cfg.ProductID)
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

		// tag ProducerReason
		a.ProducerReason = strings.TrimSpace(a.ProducerReason + "|merge:" + b.EntryOrderID)

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

	// log.Printf(
	// "[TRACE] dust.archive side=%s open=%.8f base=%.8f notional=%.4f minNotional=%.2f lastAddReset=%s",
	// side,
	// lot.OpenPrice,
	// lot.SizeBase,
	// lot.SizeBase*px,
	// minNotional,
	// wallNow.Format(time.RFC3339),
	// )
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

type BalanceSpare struct {
	Snapshot balanceSnapshot

	AvailQuote float64
	QuoteStep  float64
	AvailBase  float64
	BaseStep   float64

	SpareQuote float64
	SpareBase  float64
}

func (t *Trader) getBalanceSpare(
	maxAge time.Duration,
	reservedShortQuoteWithFee float64,
	reservedLongBase float64,
) (BalanceSpare, bool) {

	snapshot, ok := t.getBalanceSnapshot(maxAge)
	if !ok {
		return BalanceSpare{
			Snapshot: snapshot,
		}, false
	}

	spareQuote :=
		snapshot.AvailQuote -
			reservedShortQuoteWithFee

	spareBase :=
		snapshot.AvailBase -
			reservedLongBase

	if spareQuote < 0 {
		spareQuote = 0
	}

	if spareBase < 0 {
		spareBase = 0
	}

	return BalanceSpare{
		Snapshot: snapshot,

		AvailQuote: snapshot.AvailQuote,
		QuoteStep:  snapshot.QuoteStep,
		AvailBase:  snapshot.AvailBase,
		BaseStep:   snapshot.BaseStep,

		SpareQuote: spareQuote,
		SpareBase:  spareBase,
	}, true
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
					// log.Printf("[TRACE] balance.cache.refresher.stopped")
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

					// log.Printf(
					// "[TRACE] balance.cache.refreshed elapsed_ms=%d",
					// time.Since(started).Milliseconds(),
					// )
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

const pendingRegistrationAdditionalNetUSD = 0.05

func pendingRegistrationLatchPrice(
	side OrderSide,
	entryPrice float64,
	baseQty float64,
	feeRatePct float64,
) float64 {
	if entryPrice <= 0 || baseQty <= 0 {
		return entryPrice
	}

	fr := feeRatePct / 100.0

	switch side {
	case SideBuy:
		return entryPrice -
			pendingRegistrationAdditionalNetUSD/
				(baseQty*(1.0+fr))

	case SideSell:
		den := baseQty * (1.0 - fr)
		if den <= 0 {
			return entryPrice
		}

		return entryPrice +
			pendingRegistrationAdditionalNetUSD/den

	default:
		return entryPrice
	}
}
