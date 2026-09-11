package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type orderingBroker struct {
	mu            sync.Mutex
	nextOrderID   string
	statePath     string
	pollObserved  chan bool
	availableBase float64
	events        []string
	marketStarted chan OrderSide
	marketRelease <-chan struct{}
	nextMarketID  int
}

func (b *orderingBroker) Name() string                                         { return "ordering" }
func (b *orderingBroker) GetNowPrice(context.Context, string) (float64, error) { return 100, nil }
func (b *orderingBroker) PlaceMarketQuote(ctx context.Context, _ string, side OrderSide, quote float64) (*PlacedOrder, error) {
	b.mu.Lock()
	b.nextMarketID++
	id := b.nextMarketID
	b.events = append(b.events, "market:"+string(side))
	b.mu.Unlock()
	if b.marketStarted != nil {
		b.marketStarted <- side
	}
	if b.marketRelease != nil {
		select {
		case <-b.marketRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &PlacedOrder{ID: fmt.Sprintf("market-%d", id), Side: side, Price: 100, BaseSize: quote / 100, QuoteSpent: quote, Status: "FILLED"}, nil
}
func (b *orderingBroker) GetRecentCandles(context.Context, string, string, int) ([]Candle, error) {
	return nil, nil
}
func (b *orderingBroker) GetAvailableBase(context.Context, string) (string, float64, float64, error) {
	return "BTC", b.availableBase, 0.00001, nil
}
func (b *orderingBroker) GetAvailableQuote(context.Context, string) (string, float64, float64, error) {
	return "USDT", 1000, 0.01, nil
}
func (b *orderingBroker) PlaceLimitPostOnly(_ context.Context, _ string, side OrderSide, _ float64, _ float64) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, "limit:"+string(side))
	return b.nextOrderID, nil
}
func (b *orderingBroker) GetOrder(ctx context.Context, _ string, orderID string) (*PlacedOrder, error) {
	data, err := os.ReadFile(b.statePath)
	persisted := err == nil && bytes.Contains(data, []byte(orderID))
	select {
	case b.pollObserved <- persisted:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *orderingBroker) eventSnapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.events...)
}
func (b *orderingBroker) CancelOrder(context.Context, string, string) error { return nil }
func (b *orderingBroker) GetExchangeFilters(context.Context, string) (ExFilters, error) {
	return ExFilters{PriceTick: 0.01, BaseStep: 0.00001, MinNotional: 1}, nil
}
func (b *orderingBroker) GetBBO(context.Context, string) (float64, float64, error) {
	return 99.99, 100.01, nil
}

func newOrderingTrader(t *testing.T, broker *orderingBroker) *Trader {
	t.Helper()
	return &Trader{
		cfg:                            Config{ProductID: "BTCUSDT", StateFile: broker.statePath, PersistState: true, LimitTimeoutSec: 60, FeeRatePct: 0.1, RequireBaseForShort: true},
		broker:                         broker,
		stateFile:                      broker.statePath,
		dailyStart:                     time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		books:                          map[OrderSide]*SideBook{SideBuy: {RunnerIDs: []int{}, Lots: []*Position{}}, SideSell: {RunnerIDs: []int{}, Lots: []*Position{}}},
		pendingEntries:                 make(map[string]*PendingEntry),
		pendingExits:                   make(map[string]*PendingExit),
		RefundObligations:              make(map[string]*RefundObligation),
		Case3AObligations:              make(map[string]*Case3AObligation),
		PendingReplacementRetries:      make(map[string]PendingReplacementRetry),
		producerContinuationReferences: make(ProducerContinuationReferences),
		resourceManager:                NewResourceManager(ResourceLedgerState{}),
	}
}

func TestVersion201ExitFanoutSubmissionsOverlap(t *testing.T) {
	release := make(chan struct{})
	broker := &orderingBroker{availableBase: 10, marketStarted: make(chan OrderSide, 2), marketRelease: release}
	tr := newOrderingTrader(t, broker)
	tr.cfg.PersistState = false
	tr.stateFile = ""
	tr.cfg.LimitTimeoutSec = 0
	tr.cfg.MinNotional = 1
	tr.cfg.BaseStep = 0.001
	tr.books[SideBuy].Lots = []*Position{{OpenPrice: 100, Side: SideBuy, SizeBase: 0.1, OpenTime: time.Now().UTC(), EntryOrderID: "buy-entry", Producer: EntryProducerNormalLegacy}}
	tr.books[SideSell].Lots = []*Position{{OpenPrice: 100, Side: SideSell, SizeBase: 0.1, OpenTime: time.Now().UTC(), EntryOrderID: "sell-entry", Producer: EntryProducerNormalLegacy}}
	done := make(chan []exitFanoutResult, 1)
	go func() {
		done <- tr.fanOutExits(context.Background(), 100, []exitCandidate{{side: SideBuy, entryOrderID: "buy-entry", reason: "characterization"}, {side: SideSell, entryOrderID: "sell-entry", reason: "characterization"}}, time.Now())
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-broker.marketStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("exit submissions did not overlap")
		}
	}
	close(release)
	select {
	case results := <-done:
		if len(results) != 2 {
			t.Fatalf("fanout results=%d", len(results))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fanout did not complete")
	}
}

func TestVersion201RecoveryModeAReplacementPrecedesExit(t *testing.T) {
	broker := &orderingBroker{nextOrderID: "replacement", availableBase: 10, pollObserved: make(chan bool, 1)}
	tr := newOrderingTrader(t, broker)
	tr.cfg.PersistState = false
	tr.stateFile = ""
	tr.cfg.MinNotional = 1
	tr.cfg.BaseStep = 0.00001
	tr.cfg.ProfitGateUSD = 1
	tr.MarketRegime = RegimeNormal
	tr.books[SideSell].Lots = []*Position{{OpenPrice: 100, Side: SideSell, SizeBase: 0.1, OpenTime: time.Now().UTC(), EntryOrderID: "source", Producer: EntryProducerCase11APeakReversal}}
	_, acted, err := tr.closeLotByEntryID(context.Background(), 110, SideSell, "source", "threshold_stop_loss", "L1_THRESHOLD_WARNING", time.Now())
	if err != nil || !acted {
		t.Fatalf("Mode A close failed acted=%t err=%v", acted, err)
	}
	events := broker.eventSnapshot()
	if len(events) < 2 || events[0] != "limit:SELL" || events[1] != "market:BUY" {
		t.Fatalf("Mode A ordering changed: %v", events)
	}
	for _, entry := range tr.pendingEntries {
		if entry.Cancel != nil {
			entry.Cancel()
		}
	}
}

func TestVersion201RecoveryModeBExitPrecedesSameCallReplacement(t *testing.T) {
	broker := &orderingBroker{nextOrderID: "replacement", availableBase: 0, pollObserved: make(chan bool, 1)}
	tr := newOrderingTrader(t, broker)
	tr.cfg.PersistState = false
	tr.stateFile = ""
	tr.cfg.MinNotional = 1
	tr.cfg.BaseStep = 0.00001
	tr.cfg.ProfitGateUSD = 1
	tr.MarketRegime = RegimeDown
	tr.books[SideSell].Lots = []*Position{{OpenPrice: 100, Side: SideSell, SizeBase: 0.1, OpenTime: time.Now().UTC(), EntryOrderID: "source", Producer: EntryProducerCase11APeakReversal}}
	_, acted, err := tr.closeLotByEntryID(context.Background(), 110, SideSell, "source", "threshold_stop_loss", "L1_THRESHOLD_WARNING", time.Now())
	if err != nil || !acted {
		t.Fatalf("Mode B close failed acted=%t err=%v", acted, err)
	}
	events := broker.eventSnapshot()
	if len(events) < 2 || events[0] != "market:BUY" || events[1] != "limit:SELL" {
		t.Fatalf("Mode B ordering changed: %v", events)
	}
	for _, entry := range tr.pendingEntries {
		if entry.Cancel != nil {
			entry.Cancel()
		}
	}
}

func waitForPersistedPoll(t *testing.T, observed <-chan bool) {
	t.Helper()
	select {
	case persisted := <-observed:
		if !persisted {
			t.Fatal("poller reached broker before pending state was persisted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not reach broker")
	}
}

func TestVersion201EntryRegistersAndPersistsBeforePoller(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "entry-state.json")
	broker := &orderingBroker{nextOrderID: "entry-order", statePath: statePath, pollObserved: make(chan bool, 1)}
	tr := newOrderingTrader(t, broker)
	now := time.Now().UTC()
	intent := &PendingIntent{Enabled: true, Producer: EntryProducerNormalLegacy, HotStart: now, Side: SideBuy, LimitPx: 100, BaseAtLimit: 0.1, Quote: 10, Take: 101, ProducerReason: "characterization", ProductID: "BTCUSDT", DecisionID: "decision-entry", CreatedAt: now, PendingCancelPolicy: PendingSignalCancelOnFlatOrOpposite}
	attempt := &ProducerAttempt{DecisionID: intent.DecisionID, CreatedAt: now, HotStart: now, Producer: intent.Producer, Side: string(intent.Side), Events: make(map[ProducerStage]ProducerEvent)}
	entry, err := tr.produceEntry(context.Background(), intent, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || tr.pendingEntries[entry.OrderID] != entry {
		t.Fatal("accepted entry was not registered")
	}
	if intent.SubmissionStartedAt.IsZero() || intent.ExchangeRespondedAt.IsZero() || intent.PendingRegisteredAt.IsZero() || intent.StatePersistedAt.IsZero() {
		t.Fatalf("transport checkpoints missing: %+v", intent)
	}
	if !(intent.SubmissionStartedAt.Before(intent.ExchangeRespondedAt) || intent.SubmissionStartedAt.Equal(intent.ExchangeRespondedAt)) || intent.ExchangeRespondedAt.After(intent.PendingRegisteredAt) || intent.PendingRegisteredAt.After(intent.StatePersistedAt) {
		t.Fatalf("transport checkpoint order changed: %+v", intent)
	}
	waitForPersistedPoll(t, broker.pollObserved)
	entry.Cancel()
}

func TestVersion201ExitRegistersAndPersistsBeforePoller(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "exit-state.json")
	broker := &orderingBroker{nextOrderID: "exit-order", statePath: statePath, pollObserved: make(chan bool, 1)}
	tr := newOrderingTrader(t, broker)
	tr.books[SideSell].Lots = []*Position{{OpenPrice: 100, Side: SideSell, SizeBase: 0.1, Take: 99, OpenTime: time.Now().UTC(), EntryOrderID: "source-entry", Producer: EntryProducerCase11APeakReversal}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tr.startPendingMakerExit(ctx, SideSell, "source-entry", SideBuy, "threshold_stop_loss", "characterization", 100, 0.1); err != nil {
		t.Fatal(err)
	}
	p := tr.pendingExits["exit-order"]
	if p == nil || tr.books[SideSell].Lots[0].FixedTPOrderID != "exit-order" {
		t.Fatal("accepted exit was not registered")
	}
	waitForPersistedPoll(t, broker.pollObserved)
	cancel()
}
