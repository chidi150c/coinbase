// producer_parallel_entry.go — parallel producer admission, allocation, refund servicing, and execution.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// recordAllocationEventLocked persists one authoritative allocation lifecycle
// event under the existing DecisionID. The caller must hold t.mu.
func (t *Trader) recordAllocationEventLocked(
	allocation ProducerResourceAllocation,
	stage ProducerStage,
) {
	req := allocation.Request
	if req.Intent == nil || req.Attempt == nil {
		return
	}
	if req.Attempt.Events == nil {
		req.Attempt.Events = make(map[ProducerStage]ProducerEvent)
	}

	req.Attempt.Events[stage] = ProducerEvent{
		Time:       time.Now().UTC(),
		CreatedAt:  req.Intent.CreatedAt,
		Producer:   req.Producer,
		Side:       fmt.Sprint(req.Side),
		Stage:      stage,
		DecisionID: req.Intent.DecisionID,
		Reason:     req.Intent.ProducerReason,

		ProducerPriority: int(req.Priority),
		AllocationStatus: string(allocation.Status),
		AllocationReason: string(allocation.Reason),
		AllocationMethod: allocation.AllocationMethod,

		RequestedQuote: req.RequestedQuote,
		RequestedBase:  req.RequestedBase,
		AllocatedQuote: allocation.AllocatedQuote,
		AllocatedBase:  allocation.AllocatedBase,

		AvailableQuote: allocation.Request.AttemptEventAvailableQuote(allocation),
		AvailableBase:  allocation.Request.AttemptEventAvailableBase(allocation),

		AllocationFraction:       allocation.AllocationFraction,
		PriorityGroupRequested:   allocation.PriorityGroupRequested,
		PriorityGroupAvailable:   allocation.PriorityGroupAvailable,
		PriorityGroupMemberCount: allocation.PriorityGroupMembers,
	}
}

// These tiny helpers keep the event builder independent from a second copy of
// ResourceSnapshot on each request. PriorityGroupAvailable is the authoritative
// contested-resource figure; the opposite resource remains visible as zero.
func (r ProducerResourceRequest) AttemptEventAvailableQuote(a ProducerResourceAllocation) float64 {
	if r.ResourceKind == ResourceKindQuote {
		return a.PriorityGroupAvailable
	}
	return 0
}

func (r ProducerResourceRequest) AttemptEventAvailableBase(a ProducerResourceAllocation) float64 {
	if r.ResourceKind == ResourceKindBase {
		return a.PriorityGroupAvailable
	}
	return 0
}

func allocationFinalStage(status AllocationStatus) ProducerStage {
	switch status {
	case AllocationApproved:
		return ProducerStageAllocationApproved
	case AllocationPartial:
		return ProducerStageAllocationPartial
	default:
		return ProducerStageAllocationRejected
	}
}

func (t *Trader) persistProducerAttemptLocked(attempt *ProducerAttempt) {
	if attempt == nil {
		return
	}
	t.recordProducerAttemptLocked(attempt)
	if err := t.saveProducerHistoryNoLock(); err != nil {
		// Existing history persistence is best-effort at this boundary. The
		// trading/exchange lifecycle retains its own explicit persist failures.
		return
	}
}

func producerAllocationRefundCandidateUSD(
	allocation ProducerResourceAllocation,
	price float64,
) float64 {
	req := allocation.Request
	if req.RefundRequestedUSD <= 0 {
		return 0
	}

	var extraUSD float64
	switch req.Side {
	case SideBuy:
		extraUSD = allocation.AllocatedQuote - req.CoreQuote
	case SideSell:
		extraBase := allocation.AllocatedBase - req.CoreBase
		if extraBase > 0 {
			extraUSD = extraBase * price
		}
	}
	if extraUSD < 0 {
		extraUSD = 0
	}
	if extraUSD > req.RefundRequestedUSD {
		extraUSD = req.RefundRequestedUSD
	}
	return extraUSD
}

// capAllocationToCurrentRefundLocked prevents multiple producers in the same
// batch from consuming the same refund-service budget. If a higher-priority
// producer already consumed that budget, the lower producer is reduced back
// toward its core sizing; the released remainder is intentionally NOT
// redistributed in V1.
func (t *Trader) capAllocationToCurrentRefundLocked(
	allocation *ProducerResourceAllocation,
	price float64,
) float64 {
	if allocation == nil || allocation.Request.RefundRequestedUSD <= 0 {
		return 0
	}

	req := allocation.Request
	candidate := producerAllocationRefundCandidateUSD(*allocation, price)
	if candidate <= 0 {
		return 0
	}

	available := 0.0
	switch req.Side {
	case SideSell:
		available = t.refundBuyUSD
	case SideBuy:
		available = t.refundSellUSD
	}
	if available < 0 {
		available = 0
	}
	refundUSD := math.Min(candidate, available)

	// Any requested refund portion no longer backed by the refund budget is
	// released from this allocation. It does not become ordinary risk sizing.
	if refundUSD+1e-9 < candidate {
		switch req.Side {
		case SideBuy:
			maxQuote := req.CoreQuote + refundUSD
			if allocation.AllocatedQuote > maxQuote {
				allocation.AllocatedQuote = maxQuote
				allocation.AllocatedBase = maxQuote / price
			}
		case SideSell:
			maxBase := req.CoreBase + refundUSD/price
			if allocation.AllocatedBase > maxBase {
				allocation.AllocatedBase = maxBase
				allocation.AllocatedQuote = maxBase * price
			}
		}
	}

	return refundUSD
}

func (t *Trader) consumeRefundReservationLocked(side OrderSide, refundUSD float64) {
	if refundUSD <= 0 {
		return
	}
	switch side {
	case SideSell:
		t.refundBuyUSD -= refundUSD
		if t.refundBuyUSD < 0 {
			t.refundBuyUSD = 0
		}
	case SideBuy:
		t.refundSellUSD -= refundUSD
		if t.refundSellUSD < 0 {
			t.refundSellUSD = 0
		}
	}
}

func (t *Trader) prepareIntentFromAllocation(
	allocation ProducerResourceAllocation,
	refundUSD float64,
	limitPx float64,
	baseAtLimit float64,
) {
	req := allocation.Request
	intent := req.Intent
	if intent == nil {
		return
	}

	intent.Side = req.Side
	intent.ProductID = t.cfg.ProductID
	intent.LimitPx = limitPx
	intent.BaseAtLimit = baseAtLimit
	intent.Quote = baseAtLimit * limitPx
	intent.Take = req.Take
	intent.EntryMethod = req.EntryMethod
	intent.RefundPortionUSD = refundUSD
	intent.ConfidenceMult = req.ConfidenceMult
	intent.ProfitGateUSD = req.ProfitGateUSD
	intent.PendingCancelPolicy = req.Decision.PendingCancelPolicy
	intent.AssignRunner = req.Decision.AssignRunner
	intent.ProducerReason = req.Decision.ProducerReason
	if intent.History == nil {
		intent.History = make([]string, 0, 5)
	}
}

func marketEntryErrorCode(err error) EntryProduceErrorCode {
	if err == nil {
		return ""
	}
	code := EntryProduceErrSubmitNetworkFailed
	var binanceErr *BinanceBridgeError
	if errors.As(err, &binanceErr) {
		msg := strings.TrimSpace(binanceErr.BinanceMsg)
		switch {
		case (binanceErr.BinanceCode == -2010 || binanceErr.BinanceCode == -1010) &&
			msg == "Account has insufficient balance for requested action.":
			code = EntryProduceErrInsufficientBalance
		case binanceErr.BinanceCode == -1007:
			code = EntryProduceErrSubmitTimeout
		case binanceErr.BinanceCode == -1003:
			code = EntryProduceErrRateLimited
		default:
			code = EntryProduceErrExchangeRejected
		}
	}
	return code
}

// executeProducerAllocation realizes one already-authoritative allocation.
// It never resizes upward and never consults the old ResourceSnapshot for more
// funding. The AllocationPlan, not the snapshot, governs execution.
func (t *Trader) executeProducerAllocation(
	ctx context.Context,
	allocation ProducerResourceAllocation,
	price float64,
	minNotional float64,
	now time.Time,
	wallNow time.Time,
	hotStart time.Time,
) (string, error) {
	req := allocation.Request
	intent := req.Intent
	attempt := req.Attempt
	if intent == nil || attempt == nil {
		return "", fmt.Errorf("allocation missing producer lifecycle")
	}

	// The batch installed a transient resource reservation before any broker
	// submission. Keep it authoritative during execution, then always release it:
	// on pending registration the PendingEntry registry becomes authoritative;
	// on market success the committed position becomes authoritative; and on any
	// failure no exchange-backed reservation remains.
	defer func() {
		t.mu.Lock()
		t.releaseProducerResourcesLocked(intent.DecisionID)
		t.mu.Unlock()
	}()

	quote := allocation.AllocatedQuote
	base := allocation.AllocatedBase
	if quote < minNotional || base <= 0 {
		return "", fmt.Errorf("allocated order became invalid quote=%.8f base=%.8f", quote, base)
	}

	// Resolve the optional refund-service slice against the current budget under
	// t.mu. The batch is serialized by producerAllocationMu, so no second entry
	// allocation cycle can consume the same refund budget concurrently.
	t.mu.Lock()
	refundUSD := t.capAllocationToCurrentRefundLocked(&allocation, price)
	quote = allocation.AllocatedQuote
	base = allocation.AllocatedBase

	// Preserve the old refund timing: once this producer has actually taken a
	// refund-service slice into its order sizing, consume that stored refund
	// before submission. A later broker failure does not recreate it.
	if refundUSD > 0 {
		t.consumeRefundReservationLocked(req.Side, refundUSD)
		remaining := 0.0
		switch req.Side {
		case SideSell:
			remaining = t.refundBuyUSD
			if remaining == 0 {
				req.Decision.ProducerReason = strings.TrimSpace(req.Decision.ProducerReason + "|refund=buy-full")
			} else {
				req.Decision.ProducerReason = strings.TrimSpace(req.Decision.ProducerReason + "|refund=buy-partial")
			}
		case SideBuy:
			remaining = t.refundSellUSD
			if remaining == 0 {
				req.Decision.ProducerReason = strings.TrimSpace(req.Decision.ProducerReason + "|refund=sell-full")
			} else {
				req.Decision.ProducerReason = strings.TrimSpace(req.Decision.ProducerReason + "|refund=sell-partial")
			}
		}
		allocation.Request.Decision.ProducerReason = req.Decision.ProducerReason
		if intent != nil {
			intent.ProducerReason = req.Decision.ProducerReason
		}
	}
	t.mu.Unlock()

	if quote < minNotional || base <= 0 {
		return "", fmt.Errorf("allocation below exchange minimum after refund reconciliation")
	}

	offsetBps := t.cfg.LimitPriceOffsetBps
	limitWait := t.cfg.LimitTimeoutSec
	wantLimit := strings.EqualFold(strings.TrimSpace(t.cfg.OrderType), "limit") &&
		offsetBps > 0 && limitWait > 0

	// Preserve the existing one-shot per-side market preference after a maker
	// timeout. This is execution policy, not duplicate control.
	recheckNow := false
	t.mu.Lock()
	switch req.Side {
	case SideBuy:
		recheckNow = t.pendingRecheckBuy
	case SideSell:
		recheckNow = t.pendingRecheckSell
	}
	t.mu.Unlock()
	if recheckNow {
		wantLimit = false
	}

	if wantLimit {
		limitPx := price
		if req.Side == SideBuy {
			limitPx = price * (1 - offsetBps/10000)
		} else {
			limitPx = price * (1 + offsetBps/10000)
		}
		if tick := t.cfg.PriceTick; tick > 0 {
			if req.Side == SideBuy {
				limitPx = math.Floor(limitPx/tick) * tick
			} else {
				limitPx = math.Ceil(limitPx/tick) * tick
			}
		}

		baseAtLimit := quote / limitPx
		if t.cfg.BaseStep > 0 {
			baseAtLimit = math.Floor(baseAtLimit/t.cfg.BaseStep) * t.cfg.BaseStep
		}

		if limitPx <= 0 {
			log.Printf(
				"[DEBUG] postonly.invalid_limit "+
					"side=%s limit=%.8f live=%.8f",
				req.Side, limitPx, price,
			)
		}

		if limitPx > 0 && baseAtLimit > 0 && baseAtLimit*limitPx >= minNotional {
			t.prepareIntentFromAllocation(allocation, refundUSD, limitPx, baseAtLimit)

			var (
				entry *PendingEntry
				err   error
			)
			switch req.Side {
			case SideBuy:
				entry, err = t.startProducerBuyEntry(ctx, intent, attempt)
			case SideSell:
				entry, err = t.startProducerSellEntry(ctx, intent, attempt)
			default:
				err = fmt.Errorf("unsupported entry side %s", req.Side)
			}

			t.mu.Lock()
			if err == nil && entry != nil {
			}
			t.persistProducerAttemptLocked(attempt)
			t.mu.Unlock()

			if err != nil {
				log.Printf(
					"[DEBUG] postonly.error "+
						"hold_for_recheck side=%s err=%v",
					req.Side, err,
				)
				return fmt.Sprintf("HOLD producer=%s", req.Producer), err
			}
			if entry != nil {
				return fmt.Sprintf("OPEN-PENDING producer=%s side=%s order_id=%s", req.Producer, req.Side, entry.OrderID), nil
			}
			// Maker size became non-viable inside the wrapper only if an invariant
			// changed. Fall through to direct market rather than increasing size.
		} else if limitPx > 0 {
			log.Printf(
				"[DEBUG] postonly.skip "+
					"reason=snapped_size_below_min "+
					"side=%s base=%.8f limit=%.8f notional=%.8f min_notional=%.8f",
				req.Side, baseAtLimit, limitPx, baseAtLimit*limitPx, minNotional,
			)
		}
	}

	// Direct market path. The requested quote is exactly the coordinator grant;
	// the legacy insufficient-balance retry may only reduce it to minNotional.
	marketQuote := quote
	marketBase := base
	intent.Side = req.Side
	intent.ProductID = t.cfg.ProductID
	intent.LimitPx = price
	intent.BaseAtLimit = marketBase
	intent.Quote = marketQuote
	intent.Take = req.Take
	intent.EntryMethod = req.EntryMethod
	intent.RefundPortionUSD = refundUSD
	intent.ConfidenceMult = req.ConfidenceMult
	intent.ProfitGateUSD = req.ProfitGateUSD
	intent.PendingCancelPolicy = req.Decision.PendingCancelPolicy
	intent.AssignRunner = req.Decision.AssignRunner
	intent.ProducerReason = req.Decision.ProducerReason

	if attempt.Events == nil {
		attempt.Events = make(map[ProducerStage]ProducerEvent)
	}
	attempt.Events[ProducerStageProduced] = ProducerEvent{
		Time:       time.Now().UTC(),
		CreatedAt:  intent.CreatedAt,
		Producer:   intent.Producer,
		Side:       fmt.Sprint(req.Side),
		Stage:      ProducerStageProduced,
		DecisionID: intent.DecisionID,
		Reason:     intent.ProducerReason,
		Price:      price,
		BaseSize:   marketBase,
		QuoteValue: marketQuote,
	}

	placed, err := t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, req.Side, marketQuote)
	if err != nil && marketQuote > minNotional && isBinanceInsufficientBalance(err) {
		marketQuote = minNotional
		marketBase = marketQuote / price
		if event, ok := attempt.Events[ProducerStageProduced]; ok {
			event.BaseSize = marketBase
			event.QuoteValue = marketQuote
			attempt.Events[ProducerStageProduced] = event
		}
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, req.Side, marketQuote)
	}
	if err != nil {
		t.mu.Lock()
		t.addDecisionProducerEvent(
			intent,
			attempt,
			ProducerStageEntryFailed,
			marketEntryErrorCode(err),
			fmt.Errorf("direct market entry submission failed: side=%s quote=%.8f: %w", req.Side, marketQuote, err),
			false,
			true,
		)
		t.mu.Unlock()
		return fmt.Sprintf("HOLD producer=%s", req.Producer), err
	}
	if placed == nil {
		err = errors.New("market broker returned nil PlacedOrder")
		t.mu.Lock()
		t.addDecisionProducerEvent(intent, attempt, ProducerStageEntryFailed, EntryProduceErrBuildOrder, err, false, true)
		t.mu.Unlock()
		return fmt.Sprintf("HOLD producer=%s", req.Producer), err
	}

	attempt.Events[ProducerStageFilled] = ProducerEvent{
		Time:       time.Now().UTC(),
		CreatedAt:  intent.CreatedAt,
		Producer:   intent.Producer,
		Side:       fmt.Sprint(req.Side),
		Stage:      ProducerStageFilled,
		DecisionID: intent.DecisionID,
		OrderID:    placed.ID,
		Reason:     intent.ProducerReason,
		Price:      placed.Price,
		BaseSize:   placed.BaseSize,
		QuoteValue: placed.QuoteSpent,
	}

	// Reuse the authoritative generic commit logic used by asynchronous maker
	// fills instead of maintaining a second position-commit implementation.
	t.mu.Lock()

	// Preserve the historical direct-market actual-fill fee delta. The old
	// market path adjusted equity only for the difference between requested
	// quote and broker-reported actual quote; maker commits did not do this.
	actualQuoteForDelta := marketQuote
	if placed.QuoteSpent > 0 {
		actualQuoteForDelta = placed.QuoteSpent
	}
	delta := (actualQuoteForDelta - marketQuote) * (t.cfg.FeeRatePct / 100.0)
	t.equityUSD -= delta

	entry, buildErr := t.buildPendingEntry(intent, placed.ID)
	if buildErr != nil {
		t.addDecisionProducerEvent(intent, attempt, ProducerStageEntryFailed, buildErr.Code, buildErr.Err, buildErr.CleanupRequired, true)
		t.mu.Unlock()
		return fmt.Sprintf("HOLD producer=%s", req.Producer), buildErr
	}

	res := OpenResult{
		Filled:         true,
		Placed:         placed,
		OrderID:        placed.ID,
		ProducerEvents: attempt.Events,
	}

	commitErr := t.commitEntryFill(entry, &res, now, wallNow)
	if res.ProducerEvents != nil {
		for stage, event := range res.ProducerEvents {
			attempt.Events[stage] = event
		}
	}
	if commitErr != nil {
		var produceErr *EntryProduceError
		if errors.As(commitErr, &produceErr) {
			attempt.Events[ProducerStageCommitFailed] = ProducerEvent{
				Time:            time.Now().UTC(),
				CreatedAt:       intent.CreatedAt,
				Producer:        intent.Producer,
				Side:            fmt.Sprint(req.Side),
				Stage:           ProducerStageCommitFailed,
				DecisionID:      intent.DecisionID,
				OrderID:         placed.ID,
				Reason:          intent.ProducerReason,
				ErrorCode:       produceErr.Code,
				CleanupRequired: produceErr.CleanupRequired,
			}
			if produceErr.Err != nil {
				event := attempt.Events[ProducerStageCommitFailed]
				event.Error = produceErr.Err.Error()
				attempt.Events[ProducerStageCommitFailed] = event
			}
		}
		t.persistProducerAttemptLocked(attempt)
		t.mu.Unlock()
		return fmt.Sprintf("HOLD producer=%s", req.Producer), commitErr
	}

	// Preserve the one-shot recheck lifecycle: consume the side flag only
	// after a real market order has successfully reached the commit path.
	if recheckNow {
		switch req.Side {
		case SideBuy:
			t.pendingRecheckBuy = false
		case SideSell:
			t.pendingRecheckSell = false
		}
	}

	if _, refundConsumed := attempt.Events[ProducerStageRefundConsumed]; !refundConsumed {
		attempt.Events[ProducerStageCommitted] = ProducerEvent{
			Time:       time.Now().UTC(),
			CreatedAt:  intent.CreatedAt,
			Producer:   intent.Producer,
			Side:       fmt.Sprint(req.Side),
			Stage:      ProducerStageCommitted,
			DecisionID: intent.DecisionID,
			OrderID:    placed.ID,
			Reason:     intent.ProducerReason,
			Price:      placed.Price,
			BaseSize:   placed.BaseSize,
			QuoteValue: placed.QuoteSpent,
		}
	}

	t.persistProducerAttemptLocked(attempt)
	t.mu.Unlock()

	// A market fill changed the account synchronously. Force the balance cache
	// to refresh before the next allocation cycle rather than reusing the stale
	// pre-plan snapshot.
	t.invalidateBalanceSnapshot()

	return fmt.Sprintf("OPEN producer=%s side=%s order_id=%s", req.Producer, req.Side, placed.ID), nil
}

// preserveFundingFailureDebugLocked carries forward the old inline
// insufficient-funding DEBUG behavior. Refund state is mutated once per side
// after same-tick shortfalls have been aggregated. Caller holds t.mu.
func (t *Trader) preserveFundingFailureDebugLocked(
	allocation ProducerResourceAllocation,
	snapshot ResourceSnapshot,
	price float64,
) {
	if allocation.FundingShortfallUSD <= 0 {
		return
	}

	req := allocation.Request
	switch req.Side {
	case SideBuy:
		usable := snapDownResource(snapshot.SpareQuote, snapshot.QuoteStep)
		if usable < snapshot.MinNotional {
			if snapshot.SpareQuote+1e-9 < req.RequestedQuote {
				log.Printf("[DEBUG] GATE BUY: degrade failed; HOLD")
			} else {
				log.Printf(
					"[DEBUG] GATE BUY: need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
					req.RequestedQuote,
					snapshot.SpareQuote,
					snapshot.AvailQuote,
					snapshot.ReservedQuote,
					snapshot.QuoteStep,
				)
			}
		}

	case SideSell:
		log.Printf(
			"[DEBUG] GATE SELL: need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, baseStep=%.8f)",
			req.MinimumResource,
			snapshot.SpareBase,
			snapshot.AvailBase,
			snapshot.ReservedBase,
			snapshot.BaseStep,
		)
	}
}

// processParallelProducerEntriesLocked owns the complete ordinary producer
// batch after all shared evaluator materials have been computed. The caller
// must hold t.mu. The function returns with t.mu UNLOCKED.
func (t *Trader) processParallelProducerEntriesLocked(
	ctx context.Context,
	decisions []EntryDecision,
	snapshot ResourceSnapshot,
	balanceAvailable bool,
	equity EquityResult,
	execHistory []Candle,
	price float64,
	minNotional float64,
	now time.Time,
	wallNow time.Time,
	hotStart time.Time,
	aiRaw Signal,
) (StepResult, error) {
	if len(decisions) == 0 {
		t.mu.Unlock()
		return StepResult{Msg: "FLAT", Raw: aiRaw, Signal: Flat}, nil
	}

	// Preserve startup consolidation behavior before lot-slot capacity is
	// frozen into the allocation snapshot.
	if t.cfg.MaxConcurrentLots > 0 &&
		len(t.book(SideBuy).Lots)+len(t.book(SideSell).Lots) >= t.cfg.MaxConcurrentLots &&
		!t.didConsolidateStartup {
		t.consolidateRunners(t.book(SideBuy), price)
		t.consolidateRunners(t.book(SideSell), price)
		t.consolidateDust(t.book(SideBuy), price, minNotional)
		t.consolidateDust(t.book(SideSell), price, minNotional)
		t.archiveOrphanDust(t.book(SideBuy), price, minNotional)
		t.archiveOrphanDust(t.book(SideSell), price, minNotional)
		t.didConsolidateStartup = true
		_ = t.saveStateNoLock()
	}

	snapshot.CurrentLots = len(t.book(SideBuy).Lots) + len(t.book(SideSell).Lots)
	if t.cfg.MaxConcurrentLots > 0 {
		snapshot.AvailableLotSlots = t.cfg.MaxConcurrentLots - snapshot.CurrentLots
		if snapshot.AvailableLotSlots < 0 {
			snapshot.AvailableLotSlots = 0
		}
	} else {
		snapshot.AvailableLotSlots = -1
	}

	requests := make([]ProducerResourceRequest, 0, len(decisions))

	for i := range decisions {
		d := decisions[i]
		intent, attempt := newProducerDecisionLifecycle(&d)
		if intent == nil || attempt == nil {
			continue
		}

		t.addDecisionProducerEvent(intent, attempt, ProducerStageDecision, "", nil, false, false)
		if event, ok := attempt.Events[ProducerStageDecision]; ok {
			event.Price = price
			attempt.Events[ProducerStageDecision] = event
		}

		admission := t.evaluateProducerAdmissionLocked(d, price, wallNow)
		if !admission.Allowed {
			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageDecisionBlocked,
				admission.ErrorCode,
				admission.Err,
				false,
				true,
			)
			continue
		}

		// A missing/stale balance snapshot is itself an allocation outcome, not
		// a reason to erase an otherwise valid producer decision. Preserve the
		// DecisionID and explicit requested/rejected lifecycle even though exact
		// sizing cannot be authoritative without the common snapshot.
		if !balanceAvailable {
			side, _ := d.SignalToSide()
			priority := d.ProducerPriority
			if priority <= 0 {
				priority = producerPriorityFor(d.Producer)
			}
			req := ProducerResourceRequest{
				Decision: d, Intent: intent, Attempt: attempt,
				Producer: d.Producer, Side: side, Priority: priority,
			}
			if side == SideBuy {
				req.ResourceKind = ResourceKindQuote
			} else if side == SideSell && t.cfg.RequireBaseForShort {
				req.ResourceKind = ResourceKindBase
			}
			requested := ProducerResourceAllocation{
				Request:          req,
				AllocationMethod: "priority",
			}
			t.recordAllocationEventLocked(requested, ProducerStageAllocationRequested)
			rejected := requested
			rejected.Status = AllocationRejected
			rejected.Reason = AllocationReasonBalanceUnavailable
			t.recordAllocationEventLocked(rejected, ProducerStageAllocationRejected)
			t.persistProducerAttemptLocked(attempt)
			continue
		}

		if d.Confidence <= 0 {
			log.Printf(
				"[TRADE_GATE] confidence=%.2f lastAddBuy=%s lastAddSell=%s "+
					"winLowBuy=%.2f winHighSell=%.2f "+
					"latchedBuy=%.2f latchedSell=%.2f "+
					"nearestBuy{take=%.2f net=%.2f idx=%d} "+
					"nearestSell{take=%.2f net=%.2f idx=%d} ",
				d.Confidence,
				t.lastAddBuy.Format(time.RFC3339),
				t.lastAddSell.Format(time.RFC3339),
				t.winLowBuy, t.winHighSell,
				t.latchedGateBuy, t.latchedGateSell,
				t.nearestTakeBuy, t.nearestNetBuy, t.nearestIdxBuy,
				t.nearestTakeSell, t.nearestNetSell, t.nearestIdxSell,
			)
			t.addDecisionProducerEvent(
				intent, attempt, ProducerStageDecisionFailed,
				EntryProduceErrDecisionInvalidConfidence,
				fmt.Errorf("decision confidence must be > 0: %.8f", d.Confidence),
				false, true,
			)
			continue
		}

		if d.Producer != EntryProducerCase3AReplacement && d.ProfitGateMultiplier <= 0 {
			t.addDecisionProducerEvent(
				intent, attempt, ProducerStageDecisionFailed,
				EntryProduceErrInvalidProfitGate,
				fmt.Errorf(
					"ordinary producer missing standardized ProfitGateMultiplier: producer=%s tier=%s continuation=%t",
					d.Producer, d.ProducerTier, d.IsContinuation,
				),
				false, true,
			)
			continue
		}

		req, err := t.buildProducerResourceRequestLocked(
			d, intent, attempt, snapshot, equity, execHistory, price, minNotional,
		)
		if err != nil {
			t.addDecisionProducerEvent(
				intent,
				attempt,
				ProducerStageDecisionFailed,
				EntryProduceErrInvalidQuantity,
				err,
				false,
				true,
			)
			continue
		}

		requestAvailable := req.RequestedResource
		switch req.ResourceKind {
		case ResourceKindQuote:
			requestAvailable = snapshot.SpareQuote
		case ResourceKindBase:
			requestAvailable = snapshot.SpareBase
		}

		requested := ProducerResourceAllocation{
			Request:                req,
			Status:                 "",
			Reason:                 "",
			AllocationMethod:       "priority",
			PriorityGroupRequested: req.RequestedResource,
			PriorityGroupAvailable: requestAvailable,
			PriorityGroupMembers:   1,
		}
		t.recordAllocationEventLocked(requested, ProducerStageAllocationRequested)
		requests = append(requests, req)
	}

	if len(requests) == 0 {
		t.mu.Unlock()
		return StepResult{Msg: "HOLD no admitted producer requests", Raw: aiRaw, Signal: Flat}, nil
	}

	coordinator := ProducerResourceCoordinator{}
	plan := coordinator.Allocate(snapshot, requests, balanceAvailable)

	approved := make([]ProducerResourceAllocation, 0, len(plan.Allocations))

	// Refund creation keeps historical replacement semantics. Parallel allocation
	// can produce multiple same-side funding shortfalls in one coordinator tick,
	// so aggregate those shortfalls first and replace each persisted side bucket
	// exactly once. Never += into persisted refund state and never let allocation
	// iteration order decide which same-tick shortfall survives.
	var shortBuyUSD, shortSellUSD float64
	for _, allocation := range plan.Allocations {
		if allocation.FundingShortfallUSD <= 0 {
			continue
		}
		switch allocation.Request.Side {
		case SideBuy:
			shortBuyUSD += allocation.FundingShortfallUSD
		case SideSell:
			shortSellUSD += allocation.FundingShortfallUSD
		}
	}
	// Do not publish this tick's newly-created refund debt yet. Approved
	// opposite-side producers were sized against the refund state that existed
	// at the start of the coordinator tick. Preserve the old ordering: existing
	// refund is serviced first; this tick's shortfall replaces the persisted
	// bucket only after execution (never +=).

	for _, allocation := range plan.Allocations {
		t.recordAllocationEventLocked(allocation, allocationFinalStage(allocation.Status))

		if allocation.Status == AllocationRejected {
			if allocation.Reason == AllocationReasonLotCapacity {
				log.Printf("[DEBUG] GATE1 lot cap reached (%d); HOLD", t.cfg.MaxConcurrentLots)
			}
		}

		// Funding shortfall is meaningful for both rejected and partial grants.
		// Preserve historical DEBUG behavior independently from refund mutation.
		t.preserveFundingFailureDebugLocked(allocation, snapshot, price)

		t.persistProducerAttemptLocked(allocation.Request.Attempt)

		if allocation.Status == AllocationApproved || allocation.Status == AllocationPartial {
			if allocation.Status == AllocationPartial {
				t.addDecisionProducerEvent(
					allocation.Request.Intent,
					allocation.Request.Attempt,
					ProducerStageSizingReduced,
					"", nil, false, false,
				)
			}
			approved = append(approved, allocation)
		}
	}

	// Preserve the historical side-local spare synchronization. The old path
	// refreshed only the side actually being attempted; do not rewrite the
	// opposite side merely because a common snapshot now exists.
	for _, allocation := range approved {
		switch allocation.Request.Side {
		case SideBuy:
			t.SpareBuyUSD = math.Max(0, snapshot.SpareQuote)
		case SideSell:
			t.SpareSellUSD = math.Max(0, snapshot.SpareBase*price)
		}
	}

	// Atomically reserve every approved coordinator grant BEFORE broker
	// submissions begin. Existing pending reservations remain paramount and were
	// already reflected in the frozen snapshot. These transient reservations
	// protect the gap between plan finalization and PendingEntry/commit ownership.
	reservedDecisionIDs := make([]string, 0, len(approved))
	for _, allocation := range approved {
		if err := t.reserveProducerAllocationLocked(allocation); err != nil {
			for _, decisionID := range reservedDecisionIDs {
				t.releaseProducerResourcesLocked(decisionID)
			}
			t.mu.Unlock()
			return StepResult{
					Msg:    "HOLD resource reservation failed",
					Raw:    aiRaw,
					Signal: Flat,
				}, fmt.Errorf(
					"producer resource reservation failed producer=%s decision_id=%s: %w",
					allocation.Request.Producer,
					allocation.Request.Intent.DecisionID,
					err,
				)
		}

		if allocation.Request.Intent != nil &&
			strings.TrimSpace(allocation.Request.Intent.DecisionID) != "" &&
			(allocation.Request.Side == SideBuy ||
				(allocation.Request.Side == SideSell && t.cfg.RequireBaseForShort)) {
			reservedDecisionIDs = append(
				reservedDecisionIDs,
				allocation.Request.Intent.DecisionID,
			)
		}
	}

	publishTickRefundShortfallsLocked := func() {
		if shortBuyUSD > 0 {
			t.refundBuyUSD = shortBuyUSD
		}
		if shortSellUSD > 0 {
			t.refundSellUSD = shortSellUSD
		}
	}

	// If every allocation was rejected there is no execution phase, but funding
	// shortfalls are still real outcomes of this coordinator tick and must not be
	// lost behind an early return.
	if len(approved) == 0 {
		publishTickRefundShortfallsLocked()
		_ = t.saveStateNoLock()
		t.mu.Unlock()
		return StepResult{Msg: "HOLD allocation rejected", Raw: aiRaw, Signal: Flat}, nil
	}

	// The full plan and all transient reservations have been established while
	// t.mu is held. Network I/O now proceeds without t.mu. producerAllocationMu
	// (owned by step) prevents another complete allocation cycle from racing this
	// batch's frozen plan.
	t.mu.Unlock()

	messages := make([]string, 0, len(approved))
	errs := make([]error, 0)
	var lastSignal Signal = Flat
	for _, allocation := range approved {
		msg, err := t.executeProducerAllocation(
			ctx,
			allocation,
			price,
			minNotional,
			now,
			wallNow,
			hotStart,
		)
		if msg != "" {
			messages = append(messages, msg)
		}
		lastSignal = allocation.Request.Decision.Signal
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", allocation.Request.Producer, err))
		}
	}

	// Publish funding shortfalls created by THIS coordinator tick only after all
	// approved allocations had their historical opportunity to consume the
	// pre-tick refund state.
	if shortBuyUSD > 0 || shortSellUSD > 0 {
		t.mu.Lock()
		publishTickRefundShortfallsLocked()
		_ = t.saveStateNoLock()
		t.mu.Unlock()
	}

	result := StepResult{
		Msg:    strings.Join(messages, " | "),
		Raw:    aiRaw,
		Signal: lastSignal,
	}
	if result.Msg == "" {
		result.Msg = "HOLD"
	}

	return result, errors.Join(errs...)
}
