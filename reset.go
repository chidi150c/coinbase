package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type ResetController struct {
	mu        sync.Mutex
	requested bool
	requestID string
}

func (r *ResetController) Request() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requested {
		return r.requestID, false
	}
	r.requested = true
	r.requestID = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	return r.requestID, true
}

func (r *ResetController) ResetRequested() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requested
}

func (r *ResetController) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requestID
}

func (r *ResetController) complete() {
	r.mu.Lock()
	r.requested = false
	r.requestID = ""
	r.mu.Unlock()
}

func (t *Trader) requestFullSystemReset() (string, bool) {
	return t.resetController.Request()
}

func (t *Trader) cancelAndReconcileAllOrders(ctx context.Context) error {
	lister, ok := t.broker.(OpenOrderLister)
	if !ok {
		return errors.New("broker cannot discover exchange open orders")
	}

	ids := make(map[string]struct{})
	for _, entry := range t.pendingEntriesSnapshot() {
		if entry != nil && strings.TrimSpace(entry.OrderID) != "" {
			ids[strings.TrimSpace(entry.OrderID)] = struct{}{}
		}
	}
	for _, exit := range t.pendingExitsSnapshot() {
		if exit != nil && strings.TrimSpace(exit.OrderID) != "" {
			ids[strings.TrimSpace(exit.OrderID)] = struct{}{}
		}
	}
	exchangeIDs, err := lister.ListOpenOrderIDs(ctx, t.cfg.ProductID)
	if err != nil {
		return fmt.Errorf("discover open orders: %w", err)
	}
	for _, id := range exchangeIDs {
		if strings.TrimSpace(id) != "" {
			ids[strings.TrimSpace(id)] = struct{}{}
		}
	}

	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if err := t.broker.CancelOrder(ctx, t.cfg.ProductID, id); err != nil {
			// A locally known order may already be terminal. GetOrder is the
			// existing reconciliation path and determines whether cancellation
			// failure is harmless.
			ord, getErr := t.broker.GetOrder(ctx, t.cfg.ProductID, id)
			if getErr != nil || ord == nil || !strings.EqualFold(ord.Status, "done") {
				return fmt.Errorf("cancel order %s: %w", id, err)
			}
		}
		ord, err := t.broker.GetOrder(ctx, t.cfg.ProductID, id)
		if err != nil {
			return fmt.Errorf("reconcile order %s: %w", id, err)
		}
		if ord == nil || !strings.EqualFold(ord.Status, "done") {
			return fmt.Errorf("reconcile order %s: order remains non-terminal", id)
		}
	}

	remaining, err := lister.ListOpenOrderIDs(ctx, t.cfg.ProductID)
	if err != nil {
		return fmt.Errorf("verify open orders: %w", err)
	}
	if len(remaining) != 0 {
		return fmt.Errorf("open orders remain after cancellation: %v", remaining)
	}
	return nil
}

func (t *Trader) rebalanceBalancesFiftyFifty(ctx context.Context, price float64, requestID string) (balanceSnapshot, error) {
	if price <= 0 {
		return balanceSnapshot{}, fmt.Errorf("invalid rebalance price %.8f", price)
	}
	if err := t.refreshBalanceSnapshot(ctx); err != nil {
		return balanceSnapshot{}, err
	}
	before, ok := t.getBalanceSnapshot(0)
	if !ok {
		return balanceSnapshot{}, errors.New("fresh balance snapshot is invalid")
	}
	total := before.AvailQuote + before.AvailBase*price
	if total <= 0 {
		return balanceSnapshot{}, errors.New("account equity is zero")
	}
	filters, err := t.broker.GetExchangeFilters(ctx, t.cfg.ProductID)
	if err != nil {
		return balanceSnapshot{}, fmt.Errorf("exchange filters: %w", err)
	}
	minNotional := math.Max(t.cfg.OrderMinUSD, filters.MinNotional)
	target := total / 2
	delta := before.AvailQuote - target
	amount := math.Floor((math.Abs(delta)/before.QuoteStep)+1e-12) * before.QuoteStep
	if amount >= minNotional {
		side := SideBuy
		if delta < 0 {
			side = SideSell
		}
		broker, ok := t.broker.(IdempotentBroker)
		if !ok {
			return balanceSnapshot{}, errors.New("broker does not support idempotent reset order submission")
		}
		ownerID := "full-reset:" + strings.TrimSpace(requestID)
		placed, err := broker.PlaceMarketQuoteWithClientID(ctx, t.cfg.ProductID, side, amount, stableClientOrderID(ownerID))
		if err != nil {
			return balanceSnapshot{}, fmt.Errorf("50/50 %s %.8f: %w", side, amount, err)
		}
		if placed == nil || strings.TrimSpace(placed.ID) == "" {
			return balanceSnapshot{}, errors.New("50/50 order returned no exchange order ID")
		}
	}

	t.invalidateBalanceSnapshot()
	if err := t.refreshBalanceSnapshot(ctx); err != nil {
		return balanceSnapshot{}, fmt.Errorf("refresh balances after rebalance: %w", err)
	}
	after, ok := t.getBalanceSnapshot(0)
	if !ok {
		return balanceSnapshot{}, errors.New("post-rebalance balance snapshot is invalid")
	}
	imbalance := math.Abs(after.AvailQuote - after.AvailBase*price)
	tolerance := math.Max(minNotional, math.Max(after.QuoteStep, after.BaseStep*price))
	if imbalance > tolerance+1e-9 {
		return balanceSnapshot{}, fmt.Errorf(
			"50/50 verification failed: quote=%.8f base_value=%.8f imbalance=%.8f tolerance=%.8f",
			after.AvailQuote, after.AvailBase*price, imbalance, tolerance,
		)
	}
	return after, nil
}

func (t *Trader) installCleanResetState(snapshot balanceSnapshot, price float64) error {
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pendingBuyCancel != nil {
		t.pendingBuyCancel()
	}
	if t.pendingSellCancel != nil {
		t.pendingSellCancel()
	}

	t.equityUSD = snapshot.AvailQuote + snapshot.AvailBase*price
	t.dailyStart = midnightUTC(now)
	t.dailyPnL = 0
	t.lastFit = time.Time{}
	if t.model != nil {
		*t.model = *NewLogisticModel(t.model.FeatDim)
	}
	t.pos = nil
	t.didConsolidateStartup = false
	t.books = map[OrderSide]*SideBook{SideBuy: {RunnerIDs: []int{}}, SideSell: {RunnerIDs: []int{}}}
	t.previousAIRaw = Flat
	t.aiTransitionInitialized = false
	t.producerContinuationReferences = make(ProducerContinuationReferences)
	t.producerFundingSuppressions = make(map[string]ProducerFundingSuppression)
	t.lastAddBuy, t.lastAddSell = time.Time{}, time.Time{}
	t.winLowBuy, t.winHighSell = 0, 0
	t.latchedGateBuy, t.latchedGateSell = 0, 0
	t.RecentHigh, t.RecentLow = 0, 0
	t.PreviousRecentHigh, t.PreviousRecentLow = 0, 0
	t.RecentHighAt, t.RecentLowAt = time.Time{}, time.Time{}
	t.SellGateTouchedAt, t.BuyGateTouchedAt = time.Time{}, time.Time{}
	t.lastAddEquity = 0
	t.equityStageBuy, t.equityStageSell = 0, 0
	t.lastExits = nil
	t.pendingRecheckBuy, t.pendingRecheckSell = false, false
	t.pendingBuyCh, t.pendingSellCh = nil, nil
	t.pendingBuyCtx, t.pendingSellCtx = nil, nil
	t.pendingBuyCancel, t.pendingSellCancel = nil, nil
	t.nearestTakeBuy, t.nearestNetBuy, t.nearestTakeSell, t.nearestNetSell = 0, 0, 0, 0
	t.nearestIdxBuy, t.nearestIdxSell = 0, 0
	t.refundBuyUSD, t.refundSellUSD = 0, 0
	t.RefundObligations = make(map[string]*RefundObligation)
	t.SpareBuyUSD, t.SpareSellUSD = snapshot.AvailQuote, snapshot.AvailBase*price
	t.MarketRegime, t.RegimeUntil = RegimeNormal, time.Time{}
	t.FreshLowAt, t.FreshHighAt = time.Time{}, time.Time{}
	t.RegimeMultiplier, t.RecoveryDebtUSD = 1, 0
	t.dustBuyLots, t.dustSellLots = nil, nil
	t.Case3AObligations = make(map[string]*Case3AObligation)
	t.PendingReplacementRetries = make(map[string]PendingReplacementRetry)
	t.Case3BObligations = make(map[string]*Case3AObligation)
	t.PendingCase3BRetries = make(map[string]PendingReplacementRetry)
	t.pendingEntries = make(map[string]*PendingEntry)
	t.pendingExits = make(map[string]*PendingExit)
	t.resourceManager.Restore(ResourceLedgerState{})
	t.producerHistory = make(map[EntryProducer]*ProducerHistory)
	t.producerEconomics = make(map[EntryProducer]*ProducerEconomics)
	t.gateAnalysisLastSampleUnix = 0

	if err := t.saveStateNoLock(); err != nil {
		return err
	}
	return t.saveProducerHistoryNoLock()
}

func (t *Trader) executeFullSystemReset(ctx context.Context, price float64) error {
	if t == nil || t.resetController == nil {
		return errors.New("reset controller unavailable")
	}
	requestID := t.resetController.ID()
	if requestID == "" {
		return errors.New("reset request ID unavailable")
	}

	// A reset and a producer allocation must never overlap.
	t.producerAllocationMu.Lock()
	defer t.producerAllocationMu.Unlock()

	log.Printf("[RESET] started request_id=%s", requestID)
	if err := t.cancelAndReconcileAllOrders(ctx); err != nil {
		return err
	}
	snapshot, err := t.rebalanceBalancesFiftyFifty(ctx, price, requestID)
	if err != nil {
		return err
	}
	if err := t.installCleanResetState(snapshot, price); err != nil {
		return fmt.Errorf("persist clean reset state: %w", err)
	}
	t.resetController.complete()
	log.Printf("[RESET] completed successfully request_id=%s equity=%.8f quote=%.8f base=%.8f", requestID, t.equityUSD, snapshot.AvailQuote, snapshot.AvailBase)
	return nil
}
