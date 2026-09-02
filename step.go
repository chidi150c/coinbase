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
//     - Build one frozen ResourceSnapshot after existing reservations
//     - Evaluate all ordinary producers independently
//     - Apply per-producer admission (Case3B/LongOnly) without terminating the batch
//     - Build all producer sizing/resource requests from the same snapshot
//     - Allocate by ProducerPriority with proportional equal-priority sharing
//     - Execute every approved/partial allocation independently
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
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

const Version = 187

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

	// One complete EXIT -> ENTRY cycle is serialized. Parallel producer trading
	// means multiple independent producer trades within this cycle; it does not
	// mean concurrent step() goroutines mutating Trader state or racing frozen
	// resource snapshots.
	t.producerAllocationMu.Lock()
	defer t.producerAllocationMu.Unlock()

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

	// [TRACE] hotpath.after_drain intentionally disabled.

	// TODO: remove TRACE
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

	// [TRACE] hotpath.after_dust intentionally disabled.

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
	lsb := len(t.book(SideBuy).Lots)
	lss := len(t.book(SideSell).Lots)
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

		// Case3A recovery replacements keep all existing exit logic. This helper
		// is only an additional final permission check at exit time and applies
		// uniformly to both recovery methods:
		//
		//   RecoveryByPositionSize  (Mode A)
		//   RecoveryByProfitTarget  (Mode B)
		//
		//   UP:
		//     existing exit behavior is unchanged.
		//
		//   DOWN / NORMAL:
		//     the replacement may not take a profit exit until fee-aware net PnL
		//     has reached:
		//
		//         lot.ProfitGateUSD + lot.RecoveryNetUSD
		//
		// ProfitGateUSD remains the ordinary per-lot profit target.
		// RecoveryNetUSD remains the original trade-specific recovery obligation.
		case3ARecoveryProfitExitAllowed := func(
			lot *Position,
			net float64,
		) bool {
			if lot == nil ||
				lot.Producer != EntryProducerCase3AReplacement {
				return true
			}

			if lot.RecoveryNetUSD <= 0 {
				return true
			}

			// First Case3A profit exit while the replacement is in UP:
			// allow the ordinary ProfitGateUSD exit. The confirmed-fill
			// path credits realized recovery and marks Case3AUpRecoveryUsed.
			if t.MarketRegime == RegimeUp &&
				!lot.Case3AUpRecoveryUsed {
				return net >= lot.ProfitGateUSD
			}

			// After the one-time UP recovery exit has been used, or in
			// NORMAL/DOWN, require ordinary profit plus remaining recovery.
			requiredNet :=
				lot.ProfitGateUSD +
					lot.RecoveryNetUSD

			return net >= requiredNet
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
					// Case3A Mode A and Mode B use the existing Case4 signal, but
					// DOWN/NORMAL may not actually exit until normal profit +
					// recovery obligation has been achieved. UP remains unchanged.
					if !case3ARecoveryProfitExitAllowed(
						lot,
						net,
					) {
						lot.FixedTPWorking = false
						i++
						continue
					}

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
					regimeStopLoss :=
						(lot.Side == SideBuy &&
							t.MarketRegime == RegimeDown) ||
							(lot.Side == SideSell &&
								t.MarketRegime == RegimeUp)

					if enableStopLoss &&
						regimeStopLoss &&
						net <= lossLimit {
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
				//
				// For Case3A Mode A and Mode B this is the additional exit-time
				// policy only: UP exits exactly as before; DOWN/NORMAL must also
				// recover the trade-specific RecoveryNetUSD on top of the existing
				// ProfitGateUSD.
				if !case3ARecoveryProfitExitAllowed(
					lot,
					net,
				) {
					lot.FixedTPWorking = false
					i++
					continue
				}

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
				noAction  int
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

				if res.Acted {
					succeeded++
				} else {
					noAction++
				}

				// log.Printf(
				// "[TRACE] exit.fanout.done side=%s entry_id=%s reason=%s acted=%t msg=%q",
				// res.Side,
				// res.EntryOrderID,
				// res.Reason,
				// res.Acted,
				// res.Msg,
				// )

				if strings.TrimSpace(res.Msg) != "" {
					msgs = append(msgs, res.Msg)
				}
			}

			// Preserve exit-first semantics. A real exit/recovery action owns the
			// tick, and a genuine exit failure remains terminal for this tick.
			// If every selected candidate was a nil-error no-op, reacquire t.mu and
			// continue into Gate Analysis and ordinary producer coordination.
			if succeeded > 0 || failed > 0 {
				return StepResult{
					Msg: fmt.Sprintf(
						"EXIT-FANOUT total=%d succeeded=%d no_action=%d failed=%d\n%s",
						len(results),
						succeeded,
						noAction,
						failed,
						strings.Join(msgs, "\n"),
					),
				}, nil
			}

			t.mu.Lock()
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

	// Build the ONE immutable resource snapshot for this entry-allocation
	// cycle. Existing pending reservations above are already included exactly
	// once in the spare calculation. Equity and every ordinary producer reuse
	// this same snapshot; no later getBalanceSpare() lookup is permitted.
	//
	// ResourceSnapshot remains the authoritative funding view. The historical
	// SpareBuyUSD/SpareSellUSD compatibility mirrors are refreshed downstream
	// by processParallelProducerEntriesLocked() after the complete AllocationPlan
	// has been established, so step() does not create a second funding authority
	// or duplicate pending reservations here.
	resourceSnapshot, resourceSnapshotOK :=
		t.buildResourceSnapshotLocked(
			balanceSnapshotMaxAge,
			reservedShortQuoteWithFee,
			reservedLongBase,
			price,
			minNotional,
		)
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

	// Standardized continuation episode reset.
	//
	// Continuation state is producer + side specific, but the episode reset is
	// mirrored by AI direction:
	//
	//   current AI BUY  -> clear every ordinary SELL continuation reference
	//   current AI SELL -> clear every ordinary BUY continuation reference
	//
	// FLAT preserves both sides. Repeated BUY/SELL ticks are intentionally
	// idempotent so stale persisted continuation state self-heals after restart.
	if aiResult.Raw == Buy {
		t.clearProducerContinuationSide(
			SideSell,
		)
	}
	if aiResult.Raw == Sell {
		t.clearProducerContinuationSide(
			SideBuy,
		)
	}

	// Producers consume an immutable snapshot for this decision pass. Any
	// committed entry later in the tick advances Trader-owned continuation state
	// only after the fill has become local position state.
	continuationRefs :=
		t.producerContinuationReferencesSnapshot()

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
		resourceSnapshot,
		continuationRefs,
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

	// Evaluate every ordinary producer independently. Resource priority is not
	// applied during signal evaluation; it is applied only by the downstream
	// ProducerResourceCoordinator after per-producer admission and sizing.
	decisions := t.collectEntryProducerDecisions(
		aiResult,
		macdResult,
		emaResult,
		pyramidResult,
		equityResult,
		price,
		pendingCounts,
		continuationRefs,
	)

	log.Printf(
		"[TRACE] hotpath.after_decision elapsed_ms=%d producer_candidates=%d",
		time.Since(hotStart).Milliseconds(),
		len(decisions),
	)

	/*
		Gate Analysis telemetry is shared market telemetry, not a selected-
		producer artifact. MACD/EPS are common materials for every producer.
	*/
	t.recordGateAnalysisPointLocked(
		wallNow,
		price,
		macdResult.EPS,
		macdResult.Turn,
		pyramidResult.Buy.EffectiveGatePrice,
		pyramidResult.Sell.EffectiveGatePrice,
	)

	// Preserve Pyramid rebase transactions once per directional side represented
	// in this producer batch. Multiple same-side producers must not cause the
	// same shared Pyramid transition to be applied/logged repeatedly.
	hasBuyDecision := false
	hasSellDecision := false
	for _, decision := range decisions {
		switch decision.Signal {
		case Buy:
			hasBuyDecision = true
		case Sell:
			hasSellDecision = true
		}
	}
	if hasBuyDecision {
		t.applyPyramidRebaseTransactions(pyramidResult, Buy)
	}
	if hasSellDecision {
		t.applyPyramidRebaseTransactions(pyramidResult, Sell)
	}

	// The parallel-entry processor owns admission, request construction,
	// allocation lifecycle, historical refund-shortfall restoration, legacy
	// Equity-stage timing during request preparation, spare compatibility mirrors,
	// and execution of every approved/partial allocation. step() supplies the
	// single frozen ResourceSnapshot and does not re-run legacy inline sizing.
	return t.processParallelProducerEntriesLocked(
		ctx,
		decisions,
		resourceSnapshot,
		resourceSnapshotOK,
		equityResult,
		execHistory,
		price,
		minNotional,
		now,
		wallNow,
		hotStart,
		aiResult.Raw,
	)
}

// -----------------------------------------------------------------------------
// Case 3B - Latest Threshold-Stop-Loss Exit Lookup
//
// Returns the most recent losing threshold-stop-loss exit for the requested
// side within the supplied time window.
//
// Other exits that occurred afterward do not invalidate the protection.
// -----------------------------------------------------------------------------
func latestThresholdStopLossExitWithin(
	exits []ExitRecord,
	side OrderSide,
	now time.Time,
	window time.Duration,
) (*ExitRecord, bool) {
	for i := len(exits) - 1; i >= 0; i-- {
		exit := &exits[i]

		if exit.Time.IsZero() {
			continue
		}

		age := now.Sub(exit.Time)

		// Ignore invalid/future timestamps.
		if age < 0 {
			continue
		}

		// lastExits is chronological, so anything earlier is also expired.
		if age > window {
			break
		}

		if exit.Side != side {
			continue
		}

		if !strings.HasPrefix(
			exit.Reason,
			"threshold_stop_loss",
		) {
			continue
		}

		if exit.PNLUSD >= 0 {
			continue
		}

		return exit, true
	}

	return nil, false
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
