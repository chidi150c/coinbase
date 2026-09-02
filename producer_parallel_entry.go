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

// rememberInsufficientFundingLocked restores the historical refund-service
// side effect: when an entry cannot be funded, remember the unexecuted USD so
// the opposite-side trade can service it later.
//
// A failed BUY is serviced by a later SELL through refundBuyUSD.
// A failed SELL is serviced by a later BUY through refundSellUSD.
// The caller must hold t.mu.
func (t *Trader) rememberInsufficientFundingLocked(side OrderSide, failedUSD float64) {
	if failedUSD <= 0 {
		return
	}

	switch side {
	case SideBuy:
		t.refundBuyUSD += failedUSD
	case SideSell:
		t.refundSellUSD += failedUSD
	}

	_ = t.saveStateNoLock()
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
				t.consumeRefundReservationLocked(req.Side, refundUSD)
				_ = t.saveStateNoLock()
			}
			t.persistProducerAttemptLocked(attempt)
			t.mu.Unlock()

			if err != nil {
				return fmt.Sprintf("HOLD producer=%s", req.Producer), err
			}
			if entry != nil {
				return fmt.Sprintf("OPEN-PENDING producer=%s side=%s order_id=%s", req.Producer, req.Side, entry.OrderID), nil
			}
			// Maker size became non-viable inside the wrapper only if an invariant
			// changed. Fall through to direct market rather than increasing size.
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

	originalMarketQuote := marketQuote
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
		if isBinanceInsufficientBalance(err) {
			failedUSD := originalMarketQuote
			if failedUSD <= 0 {
				failedUSD = marketQuote
			}
			t.rememberInsufficientFundingLocked(req.Side, failedUSD)
		}
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

	t.consumeRefundReservationLocked(req.Side, refundUSD)
	if recheckNow {
		switch req.Side {
		case SideBuy:
			t.pendingRecheckBuy = false
		case SideSell:
			t.pendingRecheckSell = false
		}
	}
	_ = t.saveStateNoLock()
	t.persistProducerAttemptLocked(attempt)
	t.mu.Unlock()

	// A market fill changed the account synchronously. Force the balance cache
	// to refresh before the next allocation cycle rather than reusing the stale
	// pre-plan snapshot.
	t.invalidateBalanceSnapshot()

	return fmt.Sprintf("OPEN producer=%s side=%s order_id=%s", req.Producer, req.Side, placed.ID), nil
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
	totalLots :=
		len(t.book(SideBuy).Lots) +
			len(t.book(SideSell).Lots)
	for i := range decisions {
		d := decisions[i]

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
	for _, allocation := range plan.Allocations {
		t.recordAllocationEventLocked(allocation, allocationFinalStage(allocation.Status))
		t.persistProducerAttemptLocked(allocation.Request.Attempt)

		if allocation.Status == AllocationApproved || allocation.Status == AllocationPartial {
			// Allocation is only a resource reservation. Equity stage advancement
			// already occurred at the historical sizing/request-preparation point.
			approved = append(approved, allocation)
		}
	}

	// AllocationPlan is now authoritative. These values are diagnostics/spare
	// pointers used by the existing commit path; existing pending reservations
	// were already removed exactly once when snapshot was created.
	t.SpareBuyUSD = math.Max(0, snapshot.SpareQuote)
	t.SpareSellUSD = math.Max(0, snapshot.SpareBase*price)

	// The full plan has been established atomically while t.mu is held. Network
	// I/O now proceeds without t.mu. producerAllocationMu (owned by step) keeps a
	// second entry-allocation cycle from using this batch's frozen resources.
	t.mu.Unlock()

	if len(approved) == 0 {
		return StepResult{Msg: "HOLD allocation rejected", Raw: aiRaw, Signal: Flat}, nil
	}

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
