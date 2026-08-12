// FILE: trader.go
// Package main – Position/risk management and the synchronized trading loop.
//
// What’s here:
//   • Position state (open price/side/size/stop/take)
//   • Trader: holds config, broker, model, equity/PnL, and mutex
//
// Concurrency design:
//   - We take the trader mutex to read/update in-memory state,
//     but RELEASE the lock around any network I/O (placing orders,
//     fetching prices via the broker). That prevents stalls/blocking.
//   - On EXIT, we actually place a closing market order (unless DryRun).
//
// Safety:
//   - Daily circuit breaker: MaxDailyLossPct
//   - Long-only guard (Config.LongOnly): prevents new SELL entries on spot
//   - OrderMinUSD floor and proportional risk per trade

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- Position & Trader ----

type ExitMode string

const (
	ExitModeRunnerTrailing ExitMode = "RunnerTrailing"
	ExitModeScalpFixedTP   ExitMode = "ScalpFixedTP"
	ExitModeDustBasket     ExitMode = "DustBasket"
)

type Position struct {
	OpenPrice float64
	Side      OrderSide
	SizeBase  float64
	Take      float64
	OpenTime  time.Time
	// --- NEW: record entry fee for later P/L adjustment ---
	EntryFee        float64
	OpenNotionalUSD float64
	// --- NEW (runner-only trailing fields; used only when this lot is the runner) ---
	TrailActive bool    // becomes true after TRAIL_ACTIVATE_PCT threshold (legacy flag; now also used by USD-activate)
	TrailPeak   float64 // best favorable price since activation (peak for long; trough for short)
	TrailStop   float64 // current trailing stop level derived from TrailPeak and TRAIL_DISTANCE_PCT

	// --- NEW: human-readable gates/why string captured at entry time ---
	ProducerReason string `json:"reason,omitempty"`

	// --- NEW (profit-gate data model) ---
	EstExitFeeUSD    float64  `json:"est_exit_fee_usd,omitempty"` // recomputed each tick from mark
	UnrealizedPnLUSD float64  `json:"unrealized_pnl_usd"`         // NET = gross - entry - estExit
	ExitMode         ExitMode `json:"exit_mode,omitempty"`        // RunnerTrailing | ScalpFixedTP
	Version          int      `json:"version"`
	FixedTPWorking   bool     `json:"-"` // internal flag: emulate a posted TP (re-post each tick while gate holds)
	ConfidenceMult   float64  `json:"confidence_mult,omitempty"`
	ProfitGateUSD    float64  `json:"profit_gate_usd,omitempty"`
	EntryMethod      string   `json:"entry_method,omitempty"`

	TrailActivateGateUSD float64 `json:"activate_gate_usd"` // from TRAIL_ACTIVATE_USD (runner/scalp)
	TrailDistancePct     float64 `json:"distance_pct"`      // from TRAIL_DISTANCE_PCT (runner/scalp)

	// --- NEW: track maker-first TP exit order id (post-only limit attempt) ---
	FixedTPOrderID   string  `json:"-"`
	RefundPortionUSD float64 `json:"refund_portion_usd"`

	// --- NEW: stable lot identifier & entry order id (persisted) ---
	EntryOrderID             string  `json:"entry_order_id,omitempty"`
	ProfitTrailActive        bool    `json:"profit_trail_active,omitempty"`
	ProfitPeakUSD            float64 `json:"profit_peak_usd,omitempty"`
	Case3AReplacementStarted bool    `json:"case3_b_replacement_started"`

	Case3AReplacementOrderID string        `json:"case3_b_replacement_order_id"`
	Producer                 EntryProducer `json:"entry_producer,omitempty"`
}

// --- NEW: per-side book (authoritative store) ---
type SideBook struct {
	RunnerIDs []int       `json:"runner_ids,omitempty"` // NEW: multiple runner indices (authoritative for multi-runner mode)
	Lots      []*Position `json:"lots"`
}

// BotState is the persistent snapshot of trader state.
// NOTE: Persist ONLY the SideBook-based schema now.
type BotState struct {
	EquityUSD      float64
	DailyStart     time.Time
	DailyPnL       float64
	Model          *LogisticModel
	WalkForwardMin int
	LastFit        time.Time

	// --- Persisted per-side books (authoritative) ---
	BookBuy  SideBook
	BookSell SideBook

	// --- NEW: side-aware pyramiding state (persisted) ---
	LastAddBuy      time.Time
	LastAddSell     time.Time
	WinLowBuy       float64
	WinHighSell     float64
	LatchedGateBuy  float64
	LatchedGateSell float64

	// --- NEW: equity-at-last-add snapshots (SELL persisted; legacy fallback supported) ---
	LastAddEquity float64

	// --- NEW: persist equity trigger staging indices per side ---
	EquityStageBuy  int
	EquityStageSell int
	Exits           []ExitRecord

	// --- NEW (persist pending maker-first opens & recheck flags) ---
	PendingRecheckBuy       bool
	PendingRecheckSell      bool
	RefundBuyUSD            float64
	RefundSellUSD           float64
	SpareBuyUSD             float64
	SpareSellUSD            float64
	PreviousAIRaw           Signal
	PendingExits            map[string]*PendingExit
	PendingEntries          map[string]*PendingEntry
	MarketRegime            MarketRegime `json:"market_regime,omitempty"`
	RegimeUntil             time.Time    `json:"regime_until,omitempty"`
	RecentLowBreakAt        time.Time    `json:"recent_low_break_at,omitempty"`
	RecentHighBreakAt       time.Time    `json:"recent_high_break_at,omitempty"`
	RegimeMultiplier        float64
	RecoveryDebtUSD         float64
	DustBuyLots             []*Position
	DustSellLots            []*Position
	PendingReplacementRetry PendingReplacementRetry
}

type OpenResult struct {
	Filled  bool
	Placed  *PlacedOrder
	OrderID string

	ProducerEvents map[ProducerStage]ProducerEvent
}

type PendingExit struct {
	Side          OrderSide       `json:"side"`
	ProductID     string          `json:"product_id"`
	OrderID       string          `json:"order_id"`
	EntryOrderID  string          `json:"entry_order_id"`
	ExitReason    string          `json:"exit_reason"`
	ExitDecision  string          `json:"exit_decision"`
	LimitPx       float64         `json:"limit_px"`
	BaseRequested float64         `json:"base_requested"`
	Deadline      time.Time       `json:"deadline"`
	ResultC       chan ExitResult `json:"-"`
}

type ExitResult struct {
	Filled  bool
	Placed  *PlacedOrder
	OrderID string
	Pending *PendingExit
}

type Trader struct {
	cfg                   Config
	broker                Broker
	model                 *LogisticModel
	didConsolidateStartup bool
	pos                   *Position // kept for backward compatibility with earlier logic (represents last lot in aggregate)
	// lots      []*Position // legacy aggregate view (derived from books; do not mutate directly)
	mu            sync.RWMutex
	equityUSD     float64
	previousAIRaw Signal

	// NEW: path to persisted state file
	stateFile string

	// NEW: track last model fit time for walk-forward
	lastFit time.Time

	// NEW: per-side books (authoritative)
	books map[OrderSide]*SideBook

	// NEW: index of the designated runner lot in legacy aggregate (-1 if none). Derived from books.
	// runnerIdx int

	// --- NEW: side-aware pyramiding state (kept in-memory; copied to legacy fields for logs) ---
	lastAddBuy         time.Time
	lastAddSell        time.Time
	winLowBuy          float64
	winHighSell        float64
	latchedGateBuy     float64
	latchedGateSell    float64
	RecentHigh         float64
	RecentLow          float64
	PreviousRecentHigh float64
	PreviousRecentLow  float64
	SellGateTouchedAt  time.Time
	BuyGateTouchedAt   time.Time

	// --- NEW: equity-at-last-add snapshots for equity strategy trading ---
	lastAddEquity float64
	// --- NEW: equity trigger staging indices per side (0..3 for 25/50/75/100) ---
	equityStageBuy  int
	equityStageSell int

	// daily
	dailyStart time.Time
	dailyPnL   float64
	lastExits  []ExitRecord

	// --- NEW (Phase 4): async maker-first open state per-side ---
	pendingBuyCh      chan OpenResult
	pendingSellCh     chan OpenResult
	pendingBuyCtx     context.Context
	pendingSellCtx    context.Context
	pendingBuyCancel  context.CancelFunc
	pendingSellCancel context.CancelFunc

	// --- NEW (Phase 2): recheck flags for market fallback gating ---
	pendingRecheckBuy  bool
	pendingRecheckSell bool

	// --- NEW: centralized state manager channel ---
	stateApplyCh chan func(*Trader)
	// Persist snapshots for Gate2 use (under lock; we are holding t.mu in step())
	nearestTakeBuy  float64
	nearestNetBuy   float64
	nearestIdxBuy   int
	nearestTakeSell float64
	nearestNetSell  float64
	nearestIdxSell  int

	refundBuyUSD  float64
	refundSellUSD float64
	SpareBuyUSD   float64
	SpareSellUSD  float64

	MarketRegime            MarketRegime
	RegimeUntil             time.Time
	RecentLowBreakAt        time.Time
	RecentHighBreakAt       time.Time
	RegimeMultiplier        float64
	RecoveryDebtUSD         float64
	dustBuyLots             []*Position
	dustSellLots            []*Position
	PendingReplacementRetry PendingReplacementRetry
	balanceMu               sync.RWMutex

	balanceSnapshot balanceSnapshot

	balanceRefreshOnce sync.Once
	balanceRefreshStop chan struct{}
	// Unified asynchronous entry registry; key = exchange OrderID.
	pendingEntries map[string]*PendingEntry
	pendingExits   map[string]*PendingExit

	producerHistory map[EntryProducer]*ProducerHistory

	// Durable bounded producer-performance aggregate.
	//
	// Unlike producerHistory, this map does not grow with the number of
	// decisions or trades. It contains one aggregate record per producer.
	producerEconomics map[EntryProducer]*ProducerEconomics

	producerHistoryFile string
}

func NewTrader(cfg Config, broker Broker) *Trader {
	producerHistoryFile := cfg.ProducerHistoryFile
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,

		books: map[OrderSide]*SideBook{
			SideBuy:  {RunnerIDs: []int{}, Lots: nil},
			SideSell: {RunnerIDs: []int{}, Lots: nil},
		},

		pendingEntries: make(map[string]*PendingEntry),
		pendingExits:   make(map[string]*PendingExit),

		stateApplyCh:        make(chan func(*Trader), 128),
		MarketRegime:        RegimeNormal,
		producerHistoryFile: producerHistoryFile,
		producerHistory: make(
			map[EntryProducer]*ProducerHistory,
		),
	}

	// Start centralized state manager goroutine
	go func() {
		for fn := range t.stateApplyCh {
			t.mu.Lock()
			fn(t)
			t.mu.Unlock()
		}
	}()

	// Persistence guard: backtests set PERSIST_STATE=false
	persist := t.cfg.PersistState
	if !persist {
		// Disable persistence hard by clearing the path.
		t.stateFile = ""
		log.Printf("[INFO] persistence disabled (PERSIST_STATE=false); starting fresh state")
	} else {
		// Try to load state if enabled
		if err := t.loadState(); err == nil {
			log.Printf("[INFO] trader state restored from %s", t.stateFile)
		} else {
			log.Printf("[INFO] no prior state restored: %v", err)
			// >>> FAIL-FAST: if live (not DryRun) and persistence is expected,
			// and the state path isn't a mounted/writable volume, abort with a clear message.
			if shouldFatalNoStateMount(t.stateFile) {
				log.Fatalf("[FATAL] persistence required but state path is not a mounted volume or not writable: STATE_FILE=%s ; "+
					"mount /opt/coinbase/state into the container and ensure it's writable. "+
					"Example docker-compose:\n  volumes:\n    - /opt/coinbase/state:/opt/coinbase/state",
					t.stateFile)
			}
		}
		if t.producerHistoryFile != "" {
			if err := t.loadProducerHistory(); err != nil {
				log.Printf(
					"[WARN] producer history state not restored "+
						"file=%s err=%v",
					t.producerHistoryFile,
					err,
				)
			} else {
				log.Printf(
					"[INFO] producer history restored from %s",
					t.producerHistoryFile,
				)
			}
		}
	}

	// Initialize legacy aggregate view for logs/compat.
	// t.refreshAggregateFromBooks()
	return t
}

// ExitRecord captures a compact snapshot for an exited lot.
type ExitRecord struct {
	Time             time.Time `json:"time"`
	Side             OrderSide `json:"side"`
	OpenPrice        float64   `json:"open_price"`
	ClosePrice       float64   `json:"close_price"`
	SizeBase         float64   `json:"size_base"`
	OpenNotionalUSD  float64   `json:"open_notional_usd"`
	EntryFeeUSD      float64   `json:"entry_fee_usd"`
	ExitFeeUSD       float64   `json:"exit_fee_usd"`
	PNLUSD           float64   `json:"pnl_usd"`
	Reason           string    `json:"reason"`
	ExitMode         ExitMode  `json:"exit_mode,omitempty"`
	WasRunner        bool      `json:"was_runner"`
	RefundPortionUSD float64   `json:"refund_portion_usd"`
	// --- NEW: identifiers for traceability ---
	EntryOrderID string `json:"entry_order_id,omitempty"`
	ExitOrderID  string `json:"exit_order_id,omitempty"`
	Version      int    `json:"version"`
}

func (t *Trader) EquityUSD() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.equityUSD
}

// SetEquityUSD safely updates trader equity and the equity metric.
func (t *Trader) SetEquityUSD(v float64) {
	t.mu.Lock()
	t.equityUSD = v
	t.mu.Unlock()

	// persist new state (no-op if disabled) — executed outside lock via RLock snapshot
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		// TODO: remove TRACE
		// log.Printf("[TRACE] state.save error=%v", err)
	}
}

// NEW: safe unlock with panic-protection for deferred paths
func (t *Trader) unlockSafe() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC] unlock: %v", r)
		}
	}()
	t.mu.Unlock()
}

// NEW: centralized state apply helper
func (t *Trader) apply(fn func(*Trader)) {
	select {
	case t.stateApplyCh <- fn:
	default:
		// fallback (channel saturated): apply inline
		t.mu.Lock()
		fn(t)
		t.mu.Unlock()
	}
}

func midnightUTC(ts time.Time) time.Time {
	y, m, d := ts.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (t *Trader) updateDaily(date time.Time) {
	if midnightUTC(date) != t.dailyStart {
		t.dailyStart = midnightUTC(date)
		t.dailyPnL = 0
		// persist outside write lock path (no locking here)
		if err := t.saveStateNoLock(); err != nil {
			log.Printf("[WARN] saveState: %v", err)
			// TODO: remove TRACE
			// log.Printf("[TRACE] state.save error=%v", err)
		}
	}
}

func clamp(x, lo, hi float64) float64 {
	if hi > 0 && x > hi {
		return hi
	}
	if x < lo {
		return lo
	}
	return x
}

// --- NEW: equity trigger staging helpers ---
func equityStagesSell() []float64 { return []float64{0.25, 0.50, 0.75, 1.00} }
func equityStagesBuy() []float64  { return []float64{0.25, 0.50, 0.75, 1.00} }

func snapToStep(x, step float64) float64 {
	if step <= 0 {
		return x
	}
	n := math.Floor(x / step)
	if n <= 0 {
		return 0
	}
	return n * step
}
func clampStage(idx, n int) int {
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// --- NEW: side-aware latestEntry helper (does not alter existing latestEntry name/signature) ---
func (t *Trader) latestEntryBySide(side OrderSide) float64 {
	book := t.books[side]
	if book == nil || len(book.Lots) == 0 {
		return 0
	}
	return book.Lots[len(book.Lots)-1].OpenPrice
}

// applyRunnerTargets adjusts stop/take for the designated runner lot.
func (t *Trader) applyRunnerTargets(p *Position) {
	if p == nil {
		return
	}
	actUSD := t.cfg.TrailActivateUSDRunner
	if actUSD == 0 {
		stage := t.equityStageBuy
		if p.Side == SideSell {
			stage = t.equityStageSell
		}

		runnerMult := 1.0 + float64(stage)
		if runnerMult > 6.0 {
			runnerMult = 6.0
		}

		actUSD = runnerMult * t.cfg.ProfitGateUSD
	}
	if actUSD <= 0 {
		actUSD = t.cfg.ProfitGateUSD
	}
	p.TrailActivateGateUSD = actUSD
	// NEW: runner Take = fee-aware USD trailing activation price
	p.Take = activationPrice(p, p.TrailActivateGateUSD, t.cfg.FeeRatePct)
}

// --- NEW: USD-based trailing updater for runner/scalp trailing.
// Uses lot.UnrealizedPnLUSD populated earlier this tick.
// Returns (shouldExit, newTrailStopIfAny).
func (t *Trader) updateRunnerTrail(lot *Position, price float64) (bool, float64) {
	if lot == nil {
		return false, 0
	}
	// Profit gate: do nothing until net ≥ gate
	if lot.UnrealizedPnLUSD < t.cfg.ProfitGateUSD {
		lot.TrailActive = false
		lot.TrailPeak = 0
		lot.TrailStop = 0
		return false, 0
	}

	// Determine trailing parameters by ExitMode
	actUSD := t.cfg.TrailActivateUSDRunner

	if actUSD == 0 {
		stage := t.equityStageBuy
		if lot.Side == SideSell {
			stage = t.equityStageSell
		}

		runnerMult := 1.0 + float64(stage)
		if runnerMult > 6.0 {
			runnerMult = 6.0
		}

		actUSD = runnerMult * t.cfg.ProfitGateUSD
	}

	distPct := t.cfg.TrailDistancePctRunner
	switch lot.ExitMode {
	case ExitModeRunnerTrailing:
		// default as set
	default:
		// Non-trailing modes should not be routed here
		return false, 0
	}

	// Activation when Net PnL ≥ TRAIL_ACTIVATE_USD
	if !lot.TrailActive && lot.UnrealizedPnLUSD >= actUSD {
		lot.TrailActive = true
		lot.TrailDistancePct = distPct
		lot.TrailActivateGateUSD = actUSD
		if lot.Side == SideBuy {
			lot.TrailPeak = price
			lot.TrailStop = price * (1.0 - distPct/100.0)
		} else {
			lot.TrailPeak = price // trough for short
			lot.TrailStop = price * (1.0 + distPct/100.0)
		}
		// --- breadcrumb ---
		// log.Printf("[TRACE] trail.activate side=%s activate_usd=%.2f net=%.2f price=%.8f peak=%.8f stop=%.8f",
		// lot.Side, actUSD, lot.UnrealizedPnLUSD, price, lot.TrailPeak, lot.TrailStop)
	}

	// Maintain peak/stop while activated
	if lot.TrailActive {
		if lot.Side == SideBuy {
			if price > lot.TrailPeak {
				lot.TrailPeak = price
				ts := lot.TrailPeak * (1.0 - distPct/100.0)
				if ts > lot.TrailStop {
					lot.TrailStop = ts
					// --- breadcrumb ---
					// log.Printf("[TRACE] trail.raise lotSide=BUY peak=%.8f stop=%.8f", lot.TrailPeak, lot.TrailStop)
				}
			}
			if price <= lot.TrailStop && lot.TrailStop > 0 {
				// --- breadcrumb ---
				// log.Printf("[TRACE] trail.trigger lotSide=BUY price=%.8f stop=%.8f", price, lot.TrailStop)
				return true, lot.TrailStop
			}
		} else { // SELL
			if price < lot.TrailPeak {
				lot.TrailPeak = price
				lot.TrailStop = lot.TrailPeak * (1.0 + distPct/100.0)
				// --- breadcrumb ---
				// log.Printf("[TRACE] trail.raise lotSide=SELL trough=%.8f stop=%.8f", lot.TrailPeak, lot.TrailStop)
			}
			if price >= lot.TrailStop && lot.TrailStop > 0 {
				// --- breadcrumb ---
				// log.Printf("[TRACE] trail.trigger lotSide=SELL price=%.8f stop=%.8f", price, lot.TrailStop)
				return true, lot.TrailStop
			}
		}
	}

	return false, lot.TrailStop
}

// --- NEW: helper to get book by side (always non-nil) ---
func (t *Trader) book(side OrderSide) *SideBook {
	b := t.books[side]
	if b == nil {
		b = &SideBook{RunnerIDs: []int{}}
		t.books[side] = b
	}
	return b
}

// mergeLots merges lot at fromIdx into lot at toIdx inside the given book.
// toIdx is the survivor.
func mergeLots(book *SideBook, fromIdx, toIdx int, px float64) {
	if book == nil {
		return
	}
	if fromIdx < 0 || fromIdx >= len(book.Lots) {
		return
	}
	if toIdx < 0 || toIdx >= len(book.Lots) {
		return
	}
	if fromIdx == toIdx {
		return
	}

	// --- helper: ensure runner id exists ---
	ensureRunner := func(idx int) {
		for _, r := range book.RunnerIDs {
			if r == idx {
				return
			}
		}
		book.RunnerIDs = append(book.RunnerIDs, idx)
	}

	// --- helper: shift runner ids after removal ---
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

	a := book.Lots[toIdx]   // survivor
	b := book.Lots[fromIdx] // absorbed

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
	// keep USD persistence based on entry price
	a.OpenNotionalUSD = a.SizeBase * a.OpenPrice

	// tag ProducerReason with the absorbed lot's original EntryOrderID
	a.ProducerReason = strings.TrimSpace(a.ProducerReason + "|merge:" + b.EntryOrderID)

	// drop fromIdx
	book.Lots = append(book.Lots[:fromIdx], book.Lots[fromIdx+1:]...)
	shiftAfterRemoval(fromIdx)

	// re-assert runner if any of the two was runner
	if wereRunner {
		ensureRunner(toIdx)
	}
}

// consolidateRunners collapses multiple small runner lots on a side.
// Rules (per user spec):
// 1) use t.cfg.RiskPerTradeUSD as the threshold
// 2) keep full VWAP merge logic
// 3) merge those NOT meeting the threshold into the OLDEST among those small ones
// 4) keep the NEWEST OpenTime among merged lots
// 5) do NOT touch trader-level equity baselines (lastAddEquity*)
func (t *Trader) consolidateRunners(book *SideBook, px float64) {
	if book == nil {
		return
	}
	if px <= 0 {
		return
	}
	riskUSD := t.cfg.RiskPerTradeUSD
	if riskUSD <= 0 {
		return
	}
	if len(book.RunnerIDs) <= 1 {
		// nothing to consolidate
		return
	}

	// helper: ensure this lot index is recorded as runner
	ensureRunner := func(idx int) {
		for _, r := range book.RunnerIDs {
			if r == idx {
				return
			}
		}
		book.RunnerIDs = append(book.RunnerIDs, idx)
	}

	// shift runner ids after removal to keep them aligned with book.Lots
	shiftAfterRemoval := func(removedIdx int) {
		if len(book.RunnerIDs) == 0 {
			return
		}
		out := book.RunnerIDs[:0]
		for _, r := range book.RunnerIDs {
			if r == removedIdx {
				// drop it
				continue
			}
			if r > removedIdx {
				r--
			}
			out = append(out, r)
		}
		// reassign to a clean slice
		book.RunnerIDs = append([]int(nil), out...)
	}

	// STEP 1: detect which runners are "small" (below threshold)
	var smallRunnerIdxs []int
	for _, rid := range book.RunnerIDs {
		if rid < 0 || rid >= len(book.Lots) {
			continue
		}
		notional := book.Lots[rid].SizeBase * px
		if notional < riskUSD {
			smallRunnerIdxs = append(smallRunnerIdxs, rid)
		}
	}

	// if 0 or 1 small runner → nothing to consolidate
	if len(smallRunnerIdxs) <= 1 {
		return
	}

	// STEP 2: find the OLDEST among those small ones → this is the sink
	sink := smallRunnerIdxs[0]
	for _, idx := range smallRunnerIdxs[1:] {
		if idx < sink {
			sink = idx
		}
	}

	// merge fromIdx -> toIdx (toIdx survives)
	mergeInto := func(fromIdx, toIdx int) {
		// take copies
		survivor := book.Lots[toIdx]
		source := book.Lots[fromIdx]

		// VWAP over size
		totalBase := survivor.SizeBase + source.SizeBase
		if totalBase > 0 {
			totalQuote := survivor.OpenPrice*survivor.SizeBase + source.OpenPrice*source.SizeBase
			survivor.OpenPrice = totalQuote / totalBase
		}

		// sum size & fee
		survivor.SizeBase += source.SizeBase
		survivor.EntryFee += source.EntryFee

		// recompute notional
		survivor.OpenNotionalUSD = survivor.SizeBase * survivor.OpenPrice

		// keep NEWEST OpenTime
		if !survivor.OpenTime.IsZero() && !source.OpenTime.IsZero() {
			if source.OpenTime.After(survivor.OpenTime) {
				survivor.OpenTime = source.OpenTime
			}
		} else if survivor.OpenTime.IsZero() && !source.OpenTime.IsZero() {
			survivor.OpenTime = source.OpenTime
		}

		// tag ProducerReason
		survivor.ProducerReason = strings.TrimSpace(survivor.ProducerReason + "|mergedRunner:" + source.EntryOrderID)

		// write back survivor before we change the slice
		book.Lots[toIdx] = survivor

		// remove source
		book.Lots = append(book.Lots[:fromIdx], book.Lots[fromIdx+1:]...)
		shiftAfterRemoval(fromIdx)

		// after removal, if source was left of survivor, survivor shifts left by 1
		actualSurvivorIdx := toIdx
		if fromIdx < toIdx {
			actualSurvivorIdx = toIdx - 1
		}

		// re-assert runner on survivor
		ensureRunner(actualSurvivorIdx)
	}

	// STEP 3: merge all other small runners into the sink
	// do it in descending order so removals don't break later indices
	for i := len(smallRunnerIdxs) - 1; i >= 0; i-- {
		src := smallRunnerIdxs[i]
		if src == sink {
			continue
		}
		// since we go descending and sink is the smallest among small ones,
		// we can safely merge straight into current sink
		mergeInto(src, sink)
	}

	// NOTE:
	// - we did NOT touch t.lastAddEquity / t.equityStage*
	// - only per-lot fields on this side's book were rewritten
}

func floorToStep(x, step float64) float64 {
	if step <= 0 {
		return x
	}
	return math.Floor((x/step)+1e-12) * step
}

func archiveAndPruneExits(path string, exits *[]ExitRecord, keep int) {

	if exits == nil {
		return
	}
	if keep <= 0 {
		keep = 8
	}
	if len(*exits) <= keep {
		return
	}

	cut := len(*exits) - keep
	old := (*exits)[:cut]

	if err := appendExitsCSV(path, old); err != nil {
		log.Printf("[ERROR] exit archive failed path=%s; keeping unpruned exits to avoid data loss: %v", path, err)
		return
	}

	*exits = (*exits)[cut:]
	log.Printf("[INFO] exit archive ok path=%s archived=%d kept=%d", path, len(old), len(*exits))
}

func (t *Trader) exitsArchivePath() string {
	if strings.TrimSpace(t.stateFile) != "" {
		return filepath.Join(filepath.Dir(t.stateFile), "exits.csv")
	}

	return "exits.csv"
}

func appendExitsCSV(path string, exits []ExitRecord) error {
	if len(exits) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	_, statErr := os.Stat(path)
	writeHeader := os.IsNotExist(statErr)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if writeHeader {
		if err := w.Write(exitCSVHeader()); err != nil {
			return err
		}
	}

	for _, e := range exits {
		if err := w.Write(exitCSVRow(e)); err != nil {
			return err
		}
	}

	return w.Error()
}

func exitCSVHeader() []string {
	return []string{
		"time",
		"side",
		"open_price",
		"close_price",
		"size_base",
		"open_notional_usd",
		"entry_fee_usd",
		"exit_fee_usd",
		"pnl_usd",
		"exit_reason",
		"exit_class",
		"exit_mode",
		"was_runner",
		"refund_portion_usd",
		"entry_order_id",
		"exit_order_id",

		"entry_pup",
		"entry_confidence",
		"entry_ai_raw",
		"entry_logic_opinion",
		"entry_final_signal",
		"entry_buy_threshold",
		"entry_sell_threshold",
		"entry_logic_eps",

		"entry_gate_price",
		"entry_latched_price",
		"entry_elapsed_hr",
		"entry_latch_target_hr",
		"entry_effective_pct",
		"entry_base_pct",
		"entry_target_net_usd",

		"exit_pup",
		"exit_confidence",
		"exit_ai_raw",
		"exit_logic_opinion",
		"exit_final_signal",
		"exit_buy_threshold",
		"exit_sell_threshold",
		"exit_logic_eps",
		"exit_previous_ai_raw",
		"exit_net_pnl_usd",
		"exit_stop_loss_limit_usd",

		"entry_logic_macd_line",
		"entry_logic_macd_turn",
		"entry_logic_macd_hist",
		"entry_logic_macd_dhist",
		"entry_logic_macd_dsmooth",
		"entry_logic_macd_strong_positive",
		"entry_logic_macd_strong_negative",
		"entry_logic_macd_momentum_down",
		"entry_logic_macd_momentum_up",
		"entry_logic_ema_spread",
		"entry_logic_ema2050",
		"entry_logic_pattern_high_peak",
		"entry_logic_pattern_low_bottom",
		"entry_logic_pattern_price_down_up",
		"entry_logic_pattern_price_up_down",
		"entry_logic_pattern_buy",
		"entry_logic_pattern_sell",

		"exit_logic_macd_line",
		"exit_logic_macd_turn",
		"exit_logic_macd_hist",
		"exit_logic_macd_dhist",
		"exit_logic_macd_dsmooth",
		"exit_logic_macd_strong_positive",
		"exit_logic_macd_strong_negative",
		"exit_logic_macd_momentum_down",
		"exit_logic_macd_momentum_up",
		"exit_logic_ema_spread",
		"exit_logic_ema2050",
		"exit_logic_pattern_high_peak",
		"exit_logic_pattern_low_bottom",
		"exit_logic_pattern_price_down_up",
		"exit_logic_pattern_price_up_down",
		"exit_logic_pattern_buy",
		"exit_logic_pattern_sell",
	}
}

func exitCSVRow(e ExitRecord) []string {
	exitPart := extractExitPart(e.Reason)
	entryPart := extractEntryPart(e.Reason)

	return []string{
		e.Time.Format(time.RFC3339),
		string(e.Side),
		ff(e.OpenPrice),
		ff(e.ClosePrice),
		ff(e.SizeBase),
		ff(e.OpenNotionalUSD),
		ff(e.EntryFeeUSD),
		ff(e.ExitFeeUSD),
		ff(e.PNLUSD),
		exitReasonType(e.Reason),
		kv(exitPart, "exitClass"),
		fmt.Sprintf("%v", e.ExitMode),
		fmt.Sprintf("%v", e.WasRunner),
		ff(e.RefundPortionUSD),
		e.EntryOrderID,
		e.ExitOrderID,

		kv(entryPart, "pUp"),
		kv(entryPart, "confidence"),
		kv(entryPart, "aiRaw"),
		kv(entryPart, "logicOpinion"),
		kv(entryPart, "final"),
		kv(entryPart, "buyTh"),
		kv(entryPart, "sellTh"),
		kv(entryPart, "logicEPS"),

		kv(entryPart, "gatePrice"),
		kv(entryPart, "latched"),
		kv(entryPart, "elapsedHr"),
		kv(entryPart, "latchTargetHr"),
		kv(entryPart, "effPct"),
		kv(entryPart, "basePct"),
		kv(entryPart, "targetNetUSD"),

		kv(exitPart, "pUp"),
		kv(exitPart, "confidence"),
		kv(exitPart, "aiRaw"),
		kv(exitPart, "logicOpinion"),
		kv(exitPart, "final"),
		kv(exitPart, "buyTh"),
		kv(exitPart, "sellTh"),
		kv(exitPart, "logicEPS"),
		kv(exitPart, "previousAIRaw"),
		kv(exitPart, "exitNetPNL"),
		kv(exitPart, "stopLossLimit"),

		kv(entryPart, "logic_macd_line"),
		kv(entryPart, "logic_macd_turn"),
		kv(entryPart, "logic_macd_hist"),
		kv(entryPart, "logic_macd_dhist"),
		kv(entryPart, "logic_macd_dsmooth"),
		kv(entryPart, "logic_macd_strong_positive"),
		kv(entryPart, "logic_macd_strong_negative"),
		kv(entryPart, "logic_macd_momentum_down"),
		kv(entryPart, "logic_macd_momentum_up"),
		kv(entryPart, "logic_ema_spread"),
		kv(entryPart, "logic_ema2050"),
		kv(entryPart, "logic_pattern_high_peak"),
		kv(entryPart, "logic_pattern_low_bottom"),
		kv(entryPart, "logic_pattern_price_down_up"),
		kv(entryPart, "logic_pattern_price_up_down"),
		kv(entryPart, "logic_pattern_buy"),
		kv(entryPart, "logic_pattern_sell"),

		kv(exitPart, "logic_macd_line"),
		kv(exitPart, "logic_macd_turn"),
		kv(exitPart, "logic_macd_hist"),
		kv(exitPart, "logic_macd_dhist"),
		kv(exitPart, "logic_macd_dsmooth"),
		kv(exitPart, "logic_macd_strong_positive"),
		kv(exitPart, "logic_macd_strong_negative"),
		kv(exitPart, "logic_macd_momentum_down"),
		kv(exitPart, "logic_macd_momentum_up"),
		kv(exitPart, "logic_ema_spread"),
		kv(exitPart, "logic_ema2050"),
		kv(exitPart, "logic_pattern_high_peak"),
		kv(exitPart, "logic_pattern_low_bottom"),
		kv(exitPart, "logic_pattern_price_down_up"),
		kv(exitPart, "logic_pattern_price_up_down"),
		kv(exitPart, "logic_pattern_buy"),
		kv(exitPart, "logic_pattern_sell"),
	}
}

func decisionEntryReason(d EntryDecision) string {
	parts := make([]string, 0, 100)

	// Append an evaluator's pipe-delimited key=value materials as
	// first-class canonical fields with a side/evaluator prefix.
	//
	// Example:
	//   side=BUY|price=62987.36|gatePass=false
	//
	// Becomes:
	//   pyr_buy_side=BUY|pyr_buy_price=62987.36|pyr_buy_gatePass=false
	appendPrefixedFields := func(prefix, reason string) {
		for _, token := range strings.Split(reason, "|") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}

			key, value, found := strings.Cut(token, "=")
			if !found {
				// Preserve an unexpected non-key/value diagnostic instead
				// of silently discarding it.
				parts = append(
					parts,
					fmt.Sprintf(
						"%s_diagnostic=%s",
						prefix,
						token,
					),
				)
				continue
			}

			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)

			if key == "" {
				continue
			}

			parts = append(
				parts,
				fmt.Sprintf(
					"%s_%s=%s",
					prefix,
					key,
					value,
				),
			)
		}
	}

	// -----------------------------------------------------------------
	// AI / model context.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		fmt.Sprintf("pUp=%.5f", d.PUp),
		fmt.Sprintf("buyTh=%.5f", d.BuyThreshold),
		fmt.Sprintf("sellTh=%.5f", d.SellThreshold),
		fmt.Sprintf("confidence=%.2f", d.Confidence),
	)

	// -----------------------------------------------------------------
	// Market / interpretation context.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		fmt.Sprintf("regime=%s", d.MarketRegime),
		fmt.Sprintf("regimeMult=%.2f", d.RegimeMult),
		fmt.Sprintf("logicEPS=%.5f", d.LogicEPS),
		fmt.Sprintf("logicBaseEPS=%.5f", d.LogicBaseEPS),
		fmt.Sprintf("logicRegimeEPS=%.5f", d.LogicRegimeEPS),
	)

	// -----------------------------------------------------------------
	// MACD raw materials and interpretation.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		fmt.Sprintf("logic_macd_line=%.5f", d.LogicMACDLine),
		fmt.Sprintf("logic_macd_line_prev6=%.5f", d.LogicMACDLinePrev6),
		fmt.Sprintf("logic_macd_turn=%.5f", d.LogicMACDTurn),
		fmt.Sprintf("logic_macd_hist=%.5f", d.LogicMACDHist),
		fmt.Sprintf("logic_macd_dhist=%.5f", d.LogicMACDDHist),
		fmt.Sprintf("logic_macd_dsmooth=%.5f", d.LogicMACDDSmooth),
		fmt.Sprintf("logic_macd_strong_positive=%t", d.LogicMACDStrongPositive),
		fmt.Sprintf("logic_macd_strong_negative=%t", d.LogicMACDStrongNegative),
		fmt.Sprintf("logic_macd_momentum_down=%t", d.LogicMACDMomentumDown),
		fmt.Sprintf("logic_macd_momentum_up=%t", d.LogicMACDMomentumUp),
	)

	// -----------------------------------------------------------------
	// EMA raw materials and pattern interpretation.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		fmt.Sprintf("logic_ema_spread=%.6f", d.LogicEMASpread),
		fmt.Sprintf("logic_ema2050=%.6f", d.LogicEMA2050),
		fmt.Sprintf("logic_pattern_high_peak=%t", d.LogicPatternHighPeak),
		fmt.Sprintf("logic_pattern_low_bottom=%t", d.LogicPatternLowBottom),
		fmt.Sprintf("logic_pattern_price_down_up=%t", d.LogicPatternPriceDownUp),
		fmt.Sprintf("logic_pattern_price_up_down=%t", d.LogicPatternPriceUpDown),
		fmt.Sprintf("logic_pattern_buy=%t", d.LogicPatternBuy),
		fmt.Sprintf("logic_pattern_sell=%t", d.LogicPatternSell),
	)

	// -----------------------------------------------------------------
	// Case xx Entry Producers.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		// Case 11 — Peak-Reversal SELL producer.
		fmt.Sprintf("macd_pre_peak_zone=%t", d.MACDPrePeakZone),
		fmt.Sprintf("peak_reversal_sell=%t", d.PeakReversalSell),
		fmt.Sprintf("macd_pre_bottom_zone=%t", d.MACDPreBottomZone),
		fmt.Sprintf("bottom_reversal_buy=%t", d.BottomReversalBuy),
		// Case 13 — Capitulation-Bottom BUY evidence.
		fmt.Sprintf("case13B_near_low_pct=%.6f", d.NearRecentLowPct),
		fmt.Sprintf("case13B_price_near_low=%t", d.PriceNearRecentLow),
		fmt.Sprintf("case13A_near_high_pct=%.6f", d.NearRecentHighPct),
		fmt.Sprintf("case13A_price_near_high=%t", d.PriceNearRecentHigh),
	)

	// -----------------------------------------------------------------
	// BUY Pyramid raw materials.
	//
	// Pyramid.Buy.Reason already contains the full evaluation evidence:
	// side, price, anchor, gate price, latch, percentages, elapsed time,
	// tFloor, latch mode, spacing result, gate result, and related fields.
	// Each item is promoted to a prefixed canonical key=value field.
	// -----------------------------------------------------------------

	appendPrefixedFields(
		"pyr_buy",
		d.Pyramid.Buy.Reason,
	)

	// AdversePass is carried directly by PyramidResult and may not be
	// present inside the evaluator's reason string.
	parts = append(
		parts,
		fmt.Sprintf(
			"pyr_buy_adversePass=%t",
			d.Pyramid.Buy.AdversePass,
		),
	)

	// -----------------------------------------------------------------
	// SELL Pyramid raw materials.
	// -----------------------------------------------------------------

	appendPrefixedFields(
		"pyr_sell",
		d.Pyramid.Sell.Reason,
	)

	parts = append(
		parts,
		fmt.Sprintf(
			"pyr_sell_adversePass=%t",
			d.Pyramid.Sell.AdversePass,
		),
	)

	// -----------------------------------------------------------------
	// Equity raw materials and derived results.
	//
	// Equity.Reason contains equity, baseline, multipliers, trigger
	// thresholds, distances, spare balances, proposed sizes, pass states,
	// and final trigger states. Promote every item to a canonical field.
	// -----------------------------------------------------------------

	appendPrefixedFields(
		"equity",
		d.Equity.Reason,
	)

	// -----------------------------------------------------------------
	// Selected-side summaries.
	//
	// These explain the final selected side when a directional producer
	// exists. For FLAT decisions, the pass values remain false and the
	// reasons may be empty because no side was selected.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		fmt.Sprintf(
			"selected_pyramid_pass=%t",
			d.PyramidPass,
		),
		fmt.Sprintf(
			"selected_equity_pass=%t",
			d.EquityPass,
		),
	)

	appendPrefixedFields(
		"selected_pyramid",
		d.PyramidReason,
	)

	appendPrefixedFields(
		"selected_equity",
		d.EquityReason,
	)

	// -----------------------------------------------------------------
	// Decision flow.
	//
	// Raw and final are already printed in the canonical log prefix as
	// Raw= and Decision=, so they are not duplicated here.
	// -----------------------------------------------------------------

	parts = append(
		parts,
		fmt.Sprintf("legacySignal=%s", d.LegacySignal),
		fmt.Sprintf("logicOpinion=%s", d.LogicOpinion),
		fmt.Sprintf("Producer=%s", d.Producer),
		// Backward-compatible aliases.
		// Raw and Decision remain authoritative in the fixed log prefix.
		fmt.Sprintf("aiRaw=%s", d.Raw),
		fmt.Sprintf("final=%s", d.Signal),
	)

	return strings.Join(parts, "|")
}

func ff(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func extractExitPart(reason string) string {
	start := strings.Index(reason, "exitReason{")
	if start < 0 {
		return reason
	}
	start += len("exitReason{")

	end := strings.Index(reason[start:], "}  ||  openReason{")
	if end >= 0 {
		return reason[start : start+end]
	}

	return reason[start:]
}

func extractEntryPart(reason string) string {
	start := strings.Index(reason, "openReason{")
	if start < 0 {
		return ""
	}
	start += len("openReason{")
	entry := reason[start:]
	entry = strings.TrimSuffix(entry, "}")
	return entry
}

func exitReasonType(reason string) string {
	if reason == "" {
		return ""
	}
	parts := strings.Split(reason, " | ")
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return strings.TrimSpace(reason)
}

func kv(s, key string) string {
	if s == "" || key == "" {
		return ""
	}

	re := regexp.MustCompile(regexp.QuoteMeta(key) + `=([A-Za-z0-9_.+-]+)`)
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func placedOrderID(p *PlacedOrder) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.ID)
}

// activationPrice returns the mark price that achieves a given NET USD gain (usdGate)
// after subtracting the already-paid entry fee and estimating exit fee at feeRatePct.
func activationPrice(lot *Position, usdGate float64, feeRatePct float64) float64 {
	B := lot.SizeBase
	if B <= 0 {
		return 0
	}
	fr := feeRatePct / 100.0
	op := lot.OpenPrice

	if lot.Side == SideBuy {
		den := 1.0 - fr
		if den <= 0 {
			den = 1e-9
		}
		// Net = B*((1-fr)*P - op) - EntryFee = usdGate
		return (op + (usdGate+lot.EntryFee)/B) / den
	}

	// SELL: Net = B*(op - (1+fr)*P) - EntryFee = usdGate
	den := 1.0 + fr
	if den <= 0 {
		return 1e-9
	}
	return (op - (usdGate+lot.EntryFee)/B) / den
}

// ---- labels ----

func signalLabel(s Signal) string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	default:
		return "flat"
	}
}

// ---- Persistence helpers ----

// saveState builds a snapshot under a read lock, then writes it without holding any locks.
func (t *Trader) saveState() error {
	if t.stateFile == "" || !t.cfg.PersistState {
		return nil
	}
	t.mu.RLock()
	st := t.snapshotStateLocked()
	t.mu.RUnlock()
	return t.saveStateFrom(st)
}

// saveStateNoLock writes out the current in-memory state assuming the caller holds the write lock
// or otherwise guarantees stability; it does not take any locks.
func (t *Trader) saveStateNoLock() error {
	if t.stateFile == "" || !t.cfg.PersistState {
		return nil
	}
	st := t.snapshotStateLocked()
	return t.saveStateFrom(st)
}

// snapshotStateLocked builds the BotState assuming the caller already holds t.mu (write or read if immutable reads).
func (t *Trader) snapshotStateLocked() BotState {
	return BotState{
		EquityUSD:      t.equityUSD,
		DailyStart:     t.dailyStart,
		DailyPnL:       t.dailyPnL,
		Model:          t.model,
		WalkForwardMin: t.cfg.WalkForwardMin,
		LastFit:        t.lastFit,

		BookBuy:  *t.book(SideBuy),
		BookSell: *t.book(SideSell),

		LastAddBuy:      t.lastAddBuy,
		LastAddSell:     t.lastAddSell,
		WinLowBuy:       t.winLowBuy,
		WinHighSell:     t.winHighSell,
		LatchedGateBuy:  t.latchedGateBuy,
		PreviousAIRaw:   t.previousAIRaw,
		LatchedGateSell: t.latchedGateSell,

		LastAddEquity: t.lastAddEquity,

		// Persist equity stages
		EquityStageBuy:  t.equityStageBuy,
		EquityStageSell: t.equityStageSell,
		Exits:           t.lastExits,

		PendingRecheckBuy:       t.pendingRecheckBuy,
		PendingRecheckSell:      t.pendingRecheckSell,
		RefundBuyUSD:            t.refundBuyUSD,
		RefundSellUSD:           t.refundSellUSD,
		SpareBuyUSD:             t.SpareBuyUSD,
		SpareSellUSD:            t.SpareSellUSD,
		PendingExits:            t.pendingExits,
		PendingEntries:          t.pendingEntries,
		MarketRegime:            t.MarketRegime,
		RegimeUntil:             t.RegimeUntil,
		RecentLowBreakAt:        t.RecentLowBreakAt,
		RecentHighBreakAt:       t.RecentHighBreakAt,
		RegimeMultiplier:        t.RegimeMultiplier,
		RecoveryDebtUSD:         t.RecoveryDebtUSD,
		DustBuyLots:             append([]*Position(nil), t.dustBuyLots...),
		DustSellLots:            append([]*Position(nil), t.dustSellLots...),
		PendingReplacementRetry: t.PendingReplacementRetry,
	}
}

// saveStateFrom writes the provided snapshot to disk.
func (t *Trader) saveStateFrom(st BotState) error {
	if t.stateFile == "" || !t.cfg.PersistState {
		return nil
	}
	bs, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp := t.stateFile + ".tmp"
	if err := os.WriteFile(tmp, bs, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, t.stateFile)
}

func (t *Trader) loadState() error {
	if t.stateFile == "" || !t.cfg.PersistState {
		return fmt.Errorf("no state file configured")
	}
	bs, err := os.ReadFile(t.stateFile)
	if err != nil {
		return err
	}
	var st BotState
	if err := json.Unmarshal(bs, &st); err != nil {
		return err
	}

	// Equity restore policy
	if !t.cfg.UseLiveEquity() {
		t.equityUSD = st.EquityUSD
	}
	t.dailyStart = st.DailyStart
	t.dailyPnL = st.DailyPnL

	if st.Model != nil {
		t.model = st.Model
	}
	if !st.LastFit.IsZero() {
		t.lastFit = st.LastFit
	}
	t.MarketRegime = st.MarketRegime
	if t.MarketRegime == "" {
		t.MarketRegime = RegimeNormal
	}
	t.RegimeMultiplier = st.RegimeMultiplier
	if t.RegimeMultiplier <= 0 {
		t.RegimeMultiplier = 1.0
	}
	t.RecoveryDebtUSD = st.RecoveryDebtUSD
	if t.RecoveryDebtUSD < 0 {
		t.RecoveryDebtUSD = 0
	}
	t.RegimeUntil = st.RegimeUntil
	t.RecentLowBreakAt = st.RecentLowBreakAt
	t.RecentHighBreakAt = st.RecentHighBreakAt

	// Restore per-side books (no migration; assume st.Book*.RunnerIDs reflects persisted state)
	t.books[SideBuy] = &SideBook{
		RunnerIDs: st.BookBuy.RunnerIDs,
		Lots:      st.BookBuy.Lots,
	}
	t.books[SideSell] = &SideBook{
		RunnerIDs: st.BookSell.RunnerIDs,
		Lots:      st.BookSell.Lots,
	}

	for _, side := range []OrderSide{SideBuy, SideSell} {
		book := t.book(side)
		for i, lot := range book.Lots {
			if containsIdx(book.RunnerIDs, i) {
				// Runner → trailing (runner params)
				if lot.TrailDistancePct == 0 {
					lot.TrailDistancePct = t.cfg.TrailDistancePctRunner
				}
				if lot.TrailActivateGateUSD == 0 {
					lot.TrailActivateGateUSD = t.cfg.TrailActivateUSDRunner
				}
				if lot.ExitMode == "" {
					lot.ExitMode = ExitModeRunnerTrailing
				}
			} else {
				if lot.ExitMode == "" {
					lot.ExitMode = ExitModeScalpFixedTP
				}
			}
		}
	}

	// Side-aware pyramiding persisted state
	t.lastAddBuy = st.LastAddBuy
	t.lastAddSell = st.LastAddSell
	t.winLowBuy = st.WinLowBuy
	t.winHighSell = st.WinHighSell
	t.latchedGateBuy = st.LatchedGateBuy
	t.previousAIRaw = st.PreviousAIRaw
	t.latchedGateSell = st.LatchedGateSell
	t.lastAddEquity = st.LastAddEquity
	t.lastExits = st.Exits

	t.dustBuyLots = append([]*Position(nil), st.DustBuyLots...)
	t.dustSellLots = append([]*Position(nil), st.DustSellLots...)

	t.PendingReplacementRetry = st.PendingReplacementRetry

	// Restore equity stages
	t.equityStageBuy = st.EquityStageBuy
	t.equityStageSell = st.EquityStageSell

	t.pendingRecheckBuy = st.PendingRecheckBuy
	t.pendingRecheckSell = st.PendingRecheckSell

	t.pendingExits = st.PendingExits
	if t.pendingExits == nil {
		t.pendingExits = make(map[string]*PendingExit)
	}
	t.pendingEntries = st.PendingEntries
	if t.pendingEntries == nil {
		t.pendingEntries = make(map[string]*PendingEntry)
	}

	t.refundBuyUSD = st.RefundBuyUSD
	t.refundSellUSD = st.RefundSellUSD
	t.SpareBuyUSD = st.SpareBuyUSD
	t.SpareSellUSD = st.SpareSellUSD

	// Initialize trailing baseline for any current runners (no migration; just honor existing RunnerIDs)
	for _, side := range []OrderSide{SideBuy, SideSell} {
		book := t.book(side)
		for _, rid := range book.RunnerIDs {
			if rid >= 0 && rid < len(book.Lots) {
				r := book.Lots[rid]
				if r.TrailPeak == 0 {
					r.TrailPeak = r.OpenPrice
				}
			}
		}
	}

	// Restart warmup for pyramiding decay/adverse tracking
	now := time.Now().UTC()
	if len(t.book(SideBuy).Lots) > 0 && t.lastAddBuy.IsZero() {
		t.lastAddBuy = now
		t.winLowBuy = 0
		t.latchedGateBuy = 0
	}
	if len(t.book(SideSell).Lots) > 0 && t.lastAddSell.IsZero() {
		t.lastAddSell = now
		t.winHighSell = 0
		t.latchedGateSell = 0
	}

	// t.refreshAggregateFromBooks() // legacy aggregate (intentionally left disabled)
	return nil
}
func containsIdx(ids []int, idx int) bool {
	for _, id := range ids {
		if id == idx {
			return true
		}
	}
	return false
}

func (t *Trader) LastExits() []ExitRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	// return a copy to avoid external mutation
	out := make([]ExitRecord, len(t.lastExits))
	copy(out, t.lastExits)
	return out
}

// ---- Phase-7 helpers ----

// postSlack sends a best-effort Slack webhook message if SLACK_WEBHOOK is set.
// No impact on baseline behavior or logging; errors are ignored.
func postSlack(msg string) {
	hook := getEnv("SLACK_WEBHOOK", "")
	if hook == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body := map[string]string{"text": msg}
	bs, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", hook, bytes.NewReader(bs))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	_, _ = http.DefaultClient.Do(req)
}

// volRiskFactor derives a multiplicative factor from recent relative volatility.
// Returns ~0.6–0.8 in high vol, ~1.0 normal, up to ~1.2 in very low vol.
func volRiskFactor(c []Candle) float64 {
	if len(c) < 40 {
		return 1.0
	}
	cl := make([]float64, len(c))
	for i := range c {
		cl[i] = c[i].Close
	}
	std20 := RollingStd(cl, 20)
	i := len(std20) - 1
	relVol := std20[i] / (cl[i] + 1e-12)
	switch {
	case relVol > 0.02:
		return 0.6
	case relVol > 0.01:
		return 0.8
	case relVol < 0.004:
		return 1.2
	default:
		return 1.0
	}
}

// ---- Refit guard (minimal, internal) ----

// shouldRefit returns true only when we allow a model (re)fit:
// 1) len(history) >= cfg.MaxHistoryCandles, and
// 2) optional walk-forward cadence satisfied (cfg.WalkForwardMin).
// This is a guard only; it performs no fitting and emits no logs/metrics.
func (t *Trader) shouldRefit(historyLen int) bool {
	if !t.cfg.EnableLiveRetraining {
		return false
	}
	if historyLen < t.cfg.MaxHistoryCandles {
		return false
	}
	min := t.cfg.WalkForwardMin
	if min <= 0 {
		return true
	}
	if t.lastFit.IsZero() {
		return true
	}
	return time.Since(t.lastFit) >= time.Duration(min)*time.Minute
}

// ---- Rehydrate pending maker-first opens (minimal) ----

type RehydrateMode int

const (
	RehydrateModeResume RehydrateMode = iota
)

// RehydratePending resumes persisted asynchronous entries.
//
// Persisted PendingEntry/PendingIntent data remains authoritative.
// Rehydration only restores runtime-only state:
//   - ResultC
//   - Cancel / poller context
//   - Book/runtime ownership wiring
//   - CommitEligible
//   - clearOwner
//
// It NEVER submits a new exchange order.
func (t *Trader) RehydratePending(
	ctx context.Context,
	mode RehydrateMode,
) {
	if t == nil || mode != RehydrateModeResume {
		return
	}

	// Snapshot pointers so we never hold t.mu across broker I/O
	// or while starting pollers.
	entries := t.pendingEntriesSnapshot()

	for _, persisted := range entries {
		if persisted == nil ||
			persisted.Completed ||
			persisted.Intent == nil {
			continue
		}

		intent := persisted.Intent

		orderID := strings.TrimSpace(intent.OrderID)
		if orderID == "" {
			orderID = strings.TrimSpace(persisted.OrderID)
		}
		if orderID == "" {
			continue
		}

		// Keep the persisted OrderID invariant synchronized.
		intent.OrderID = orderID
		persisted.OrderID = orderID

		// ------------------------------------------------------------
		// Deadline already expired while the bot was down.
		// ------------------------------------------------------------
		if !intent.Deadline.IsZero() &&
			time.Now().After(intent.Deadline) {

			// Preserve the old one-shot market-fallback behavior
			// ONLY for normal entries.
			//
			// Case3A recovery entries must not create a normal
			// BUY/SELL market-preference flag.
			if persisted.Producer != EntryProducerCase3AReplacement {
				t.mu.Lock()

				switch persisted.Side {
				case SideBuy:
					t.pendingRecheckBuy = true

				case SideSell:
					t.pendingRecheckSell = true
				}

				t.mu.Unlock()
			}

			// Best effort: remove any exchange order that may
			// still be resting.
			_ = t.broker.CancelOrder(
				ctx,
				intent.ProductID,
				orderID,
			)

			// The pending lifecycle is finished.
			if persisted.clearOwner != nil {
				persisted.clearOwner()
			} else {
				t.mu.Lock()

				current, ok := t.pendingEntries[orderID]
				if ok && current == persisted {
					delete(t.pendingEntries, orderID)
				}

				t.mu.Unlock()
			}

			t.mu.Lock()
			if err := t.saveStateNoLock(); err != nil {
				// log.Printf(
				// "[TRACE] entry.rehydrate.expired_state_save_failed side=%s order_id=%s err=%v",
				// persisted.Side,
				// orderID,
				// err,
				// )
			}
			t.mu.Unlock()

			continue
		}

		// ------------------------------------------------------------
		// Check the existing exchange order.
		// ------------------------------------------------------------
		ord, err := t.broker.GetOrder(
			ctx,
			intent.ProductID,
			orderID,
		)

		if err != nil {
			// log.Printf(
			// "[TRACE] entry.rehydrate.get_order_failed side=%s source=%s order_id=%s err=%v",
			// persisted.Side,
			// persisted.Producer,
			// orderID,
			// err,
			// )

			continue
		}

		if ord == nil {
			t.mu.Lock()

			// Only normal entries create the one-shot market preference.
			if persisted.Producer != EntryProducerCase3AReplacement {
				switch persisted.Side {
				case SideBuy:
					t.pendingRecheckBuy = true

				case SideSell:
					t.pendingRecheckSell = true
				}
			}

			// Missing exchange order means this pending lifecycle is over,
			// regardless of entry source.
			current, ok := t.pendingEntries[orderID]
			if ok && current == persisted {
				delete(t.pendingEntries, orderID)
			}

			if err := t.saveStateNoLock(); err != nil {
				// log.Printf(
				// "[TRACE] entry.rehydrate.missing_state_save_failed side=%s order_id=%s err=%v",
				// persisted.Side,
				// orderID,
				// err,
				// )
			}

			t.mu.Unlock()

			continue
		}

		// ------------------------------------------------------------
		// Restore runtime-only PendingEntry state.
		// ------------------------------------------------------------

		if persisted.ResultC == nil {
			persisted.ResultC = make(chan OpenResult, 1)
		}

		persisted.Completed = false

		switch persisted.Side {
		case SideBuy:
			persisted.Book = t.book(SideBuy)
			persisted.LastAdd = &t.lastAddBuy
			persisted.WinExtreme = &t.winLowBuy
			persisted.LatchedGate = &t.latchedGateBuy
			persisted.EquityTriggered = intent.EquityBuy

		case SideSell:
			persisted.Book = t.book(SideSell)
			persisted.LastAdd = &t.lastAddSell
			persisted.WinExtreme = &t.winHighSell
			persisted.LatchedGate = &t.latchedGateSell
			persisted.EquityTriggered = intent.EquitySell

		default:
			// log.Printf(
			// "[TRACE] entry.rehydrate.invalid_side order_id=%s side=%v",
			// orderID,
			// persisted.Side,
			// )
			continue
		}

		if persisted.Producer == EntryProducerCase3AReplacement {
			persisted.CommitEligible =
				t.Case3ACommitEligible
		} else {
			persisted.CommitEligible = nil
		}

		// Restore registry cleanup ownership.
		persisted.clearOwner = func(entry *PendingEntry) func() {
			return func() {
				t.mu.Lock()
				defer t.mu.Unlock()

				current, ok :=
					t.pendingEntries[entry.OrderID]

				if ok && current == entry {
					delete(
						t.pendingEntries,
						entry.OrderID,
					)
				}
			}
		}(persisted)

		// ------------------------------------------------------------
		// It may have filled while the bot was offline.
		//
		// Feed that through ResultC exactly like the normal poller.
		// drainPendingEntry() remains the only commit path.
		// ------------------------------------------------------------
		if ord.BaseSize > 0 ||
			ord.QuoteSpent > 0 {

			select {
			case persisted.ResultC <- OpenResult{
				Filled:  true,
				Placed:  ord,
				OrderID: orderID,
			}:
			default:
			}

			// log.Printf(
			// "[TRACE] entry.rehydrate.filled side=%s source=%s order_id=%s",
			// persisted.Side,
			// persisted.Producer,
			// orderID,
			// )

			continue
		}

		// ------------------------------------------------------------
		// Existing exchange order is still live.
		//
		// Resume using the SAME generic poll/reprice implementation
		// used for newly produced entries.
		// ------------------------------------------------------------
		t.startEntryPoller(
			ctx,
			persisted,
		)

		// log.Printf(
		// "[TRACE] entry.rehydrate.resumed side=%s source=%s order_id=%s deadline=%s",
		// persisted.Side,
		// persisted.Producer,
		// orderID,
		// intent.Deadline.Format(time.RFC3339),
		// )
	}

	// ------------------------------------------------------------
	// Rehydrate persisted asynchronous exits.
	//
	// PendingExit data is persisted, but ResultC and the lot's
	// FixedTPOrderID are runtime-only. Restore those and resume
	// the existing watcher. Never submit a new exit order here.
	// ------------------------------------------------------------
	exits := t.pendingExitsSnapshot()

	for _, persisted := range exits {
		if persisted == nil {
			continue
		}

		orderID := strings.TrimSpace(
			persisted.OrderID,
		)
		if orderID == "" {
			continue
		}

		if strings.TrimSpace(persisted.ProductID) == "" {
			persisted.ProductID = t.cfg.ProductID
		}

		if persisted.ResultC == nil {
			persisted.ResultC =
				make(chan ExitResult, 1)
		}

		// FixedTPOrderID is json:"-", so restore the runtime
		// ownership link between the lot and its pending exit.
		t.mu.Lock()

		book := t.book(persisted.Side)

		for _, lot := range book.Lots {
			if lot == nil {
				continue
			}

			if strings.TrimSpace(lot.EntryOrderID) ==
				strings.TrimSpace(persisted.EntryOrderID) {

				lot.FixedTPOrderID = orderID
				break
			}
		}

		t.mu.Unlock()

		// Resume monitoring the already-existing exchange order.
		// watchPendingExit() will poll its saved OrderID, process
		// any fill that occurred while offline, and handle timeout.
		go t.watchPendingExit(
			ctx,
			persisted,
		)

		// log.Printf(
		// "[TRACE] pending_exit.rehydrate.resumed "+
		// "side=%s exit_id=%s entry_id=%s deadline=%s",
		// persisted.Side,
		// orderID,
		// persisted.EntryOrderID,
		// persisted.Deadline.Format(time.RFC3339),
		// )
	}
}

// ---- Fail-fast helpers (startup state mount check) ----

// shouldFatalNoStateMount returns true when we expect persistence but the state file's
// parent directory is not a mounted volume or not writable. This prevents accidental
// flat-boot trading after CI/CD restarts when the host volume isn't mounted.
func shouldFatalNoStateMount(stateFile string) bool {
	stateFile = strings.TrimSpace(stateFile)
	if stateFile == "" {
		return false
	}
	dir := filepath.Dir(stateFile)

	// If the file already exists, don't fatal — persistence is working.
	if _, err := os.Stat(stateFile); err == nil {
		return false
	}

	// Ensure parent directory exists and is a directory.
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return true
	}

	// Ensure directory is writable.
	if f, err := os.CreateTemp(dir, "wtest-*.tmp"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
	} else {
		return true
	}

	// Ensure it's actually a mount point (host volume), not a container tmp dir.
	isMount, err := isMounted(dir)
	if err == nil && !isMount {
		return true
	}
	return false
}

// isMounted checks /proc/self/mountinfo to see if dir is a mount point.
func isMounted(dir string) (bool, error) {
	bs, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	dir = filepath.Clean(dir)
	for _, ln := range strings.Split(string(bs), "\n") {
		parts := strings.Split(ln, " ")
		if len(parts) < 5 {
			continue
		}
		mp := filepath.Clean(parts[4]) // mount point field
		if mp == dir {
			return true, nil
		}
	}
	return false, nil
}

type RecoveryMethod int

const (
	RecoveryByProfitTarget RecoveryMethod = iota
	RecoveryByPositionSize
)

func (m RecoveryMethod) String() string {
	switch m {
	case RecoveryByProfitTarget:
		return "RecoveryByProfitTarget"
	case RecoveryByPositionSize:
		return "RecoveryByPositionSize"
	default:
		return "RecoveryUnknown"
	}
}

type PendingReplacementRetry struct {
	Enabled            bool
	Replacement        PendingIntent
	WaitForExitOrderID string
	Reason             string
	CreatedAt          time.Time
}

func (t *Trader) markCase3AReplacementRetryLocked(repl PendingIntent, waitForExitOrderID string, reason string) {
	if !repl.Enabled {
		return
	}

	t.PendingReplacementRetry = PendingReplacementRetry{
		Enabled:            true,
		Replacement:        repl,
		WaitForExitOrderID: waitForExitOrderID,
		Reason:             reason,
		CreatedAt:          time.Now().UTC(),
	}

	// log.Printf(
	// "[TRACE] Case3A.retry.marked side=%s price=%.8f base=%.8f method=%s wait_exit_id=%s reason=%s",
	// repl.Side,
	// repl.LimitPx,
	// repl.BaseAtLimit,
	// repl.RecoveryMethod.String(),
	// waitForExitOrderID,
	// reason,
	// )

	_ = t.saveStateNoLock()
}

type exitFanoutResult struct {
	Side         OrderSide
	EntryOrderID string
	Reason       string
	Msg          string
	Err          error
}

func (t *Trader) fanOutExits(
	ctx context.Context,
	livePrice float64,
	cands []exitCandidate,
) []exitFanoutResult {
	if len(cands) == 0 {
		return nil
	}

	resultsCh := make(chan exitFanoutResult, len(cands))

	var wg sync.WaitGroup
	wg.Add(len(cands))

	for _, cand := range cands {
		cand := cand

		go func() {
			defer wg.Done()

			// log.Printf(
			// "[TRACE] exit.fanout.start side=%s idx_snapshot=%d entry_id=%s reason=%s net=%.6f",
			// cand.side,
			// cand.idx,
			// cand.entryOrderID,
			// cand.reason,
			// cand.net,
			// )

			msg, err := t.closeLotByEntryID(
				ctx,
				livePrice,
				cand.side,
				cand.entryOrderID,
				cand.reason,
				cand.decision,
			)

			resultsCh <- exitFanoutResult{
				Side:         cand.side,
				EntryOrderID: cand.entryOrderID,
				Reason:       cand.reason,
				Msg:          msg,
				Err:          err,
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	results := make([]exitFanoutResult, 0, len(cands))

	for res := range resultsCh {
		results = append(results, res)
	}

	return results
}

func (t *Trader) closeLotByEntryID(
	ctx context.Context,
	livePrice float64,
	side OrderSide,
	entryOrderID string,
	exitReason string,
	exitDecision string,
) (string, error) {
	entryOrderID = strings.TrimSpace(entryOrderID)
	if entryOrderID == "" {
		return "", fmt.Errorf(
			"close by entry id: empty entry id side=%s reason=%s",
			side,
			exitReason,
		)
	}

	t.mu.Lock()

	idx := t.findLotIndexByEntryIDLocked(side, entryOrderID)
	if idx < 0 {
		t.mu.Unlock()

		return "", fmt.Errorf(
			"close by entry id: lot not found side=%s entry_id=%s reason=%s",
			side,
			entryOrderID,
			exitReason,
		)
	}

	// closeLot requires t.mu to be held and returns with it held.
	msg, err := t.closeLot(
		ctx,
		livePrice,
		side,
		idx,
		exitReason,
		exitDecision,
	)

	t.mu.Unlock()
	return msg, err
}

// --- NEW: side-aware lot closing (no global index) ---
func (t *Trader) closeLot(
	ctx context.Context,
	livePrice float64,
	side OrderSide,
	localIdx int,
	exitReason string,
	exitDecision string,
) (string, error) {

	//Prepare and validate the lot for closing

	// 	Locate the correct position book
	// 		* Retrieve the BUY or SELL book corresponding to the side being closed.
	book := t.book(side)

	// 	Validate the lot index
	// 		* Ensure the requested lot index exists.
	// 		* Fail immediately if the index is out of range.
	if localIdx < 0 || localIdx >= len(book.Lots) {
		return "", fmt.Errorf(
			"close lot invalid index side=%s idx=%d lots=%d",
			side,
			localIdx,
			len(book.Lots),
		)
	}

	// 	Validate the lot itself
	// 		* Ensure the lot pointer is not nil.
	// 		* Ensure the lot has a valid EntryOrderID.
	// 		* Fail immediately if either check fails.
	lot := book.Lots[localIdx]
	if lot == nil {
		return "", fmt.Errorf(
			"close lot nil position side=%s idx=%d",
			side,
			localIdx,
		)
	}

	entryOrderID := strings.TrimSpace(lot.EntryOrderID)
	if entryOrderID == "" {
		return "", fmt.Errorf(
			"close lot empty entry id side=%s idx=%d",
			side,
			localIdx,
		)
	}

	// 	Determine the closing direction
	// 		* BUY positions close with a SELL.
	// 		* SELL positions close with a BUY.
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}

	//Record the current UTC time for the exit.
	exitTime := time.Now().UTC()

	// 	Determine the amount to close:
	// Read the lot size.
	baseRequestedRaw := lot.SizeBase
	// Round it down to the exchange's allowed base step.
	baseRequested := floorToStep(baseRequestedRaw, t.cfg.BaseStep)
	// Skip the exit if nothing remains after rounding.
	if baseRequested <= 0 {
		log.Printf("[CLOSE-SKIP] lotSide=%s closeSide=%s baseRaw=%.8f baseRounded=%.8f step=%.8f reason=%s", lot.Side, closeSide, baseRequestedRaw, baseRequested, t.cfg.BaseStep, exitReason)
		return "", nil
	}

	// 	Verify minimum exchange notional:
	// Compute the USD value of the exit.
	quote := baseRequested * livePrice

	// If the order would be below the exchange minimum, defer the exit instead of submitting an invalid order.
	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}
	if quote < minNotional {
		log.Printf("[CLOSE-SKIP] lotSide=%s closeSide=%s base=%.8f livePrice=%.2f notional=%.2f < min %.2f; deferring", lot.Side, closeSide, baseRequested, livePrice, quote, minNotional)
		return fmt.Sprintf("EXIT-SKIP %s side=%s→%s notional=%.2f < min=%.2f reason=%s", exitTime.Format(time.RFC3339), lot.Side, closeSide, quote, minNotional, exitReason), nil
	}

	// Determine whether this is a deep-loss stop
	// Check whether the exit decision classified it as L2_DEEP_LOSS.
	// Check whether the exit reason is threshold_stop_loss.
	isL2DeepLoss := strings.HasPrefix(exitReason, "threshold_stop_loss") &&
		strings.Contains(exitDecision, "L2_DEEP_LOSS")

	//----------------------------------------------------------------
	// 	Select the exit mechanism
	//----------------------------------------------------------------
	// Use the normal maker-first pending exit only if:
	// the lot is in ScalpFixedTP mode,
	// the exit is not an L2 deep-loss emergency,
	// and maker exits are enabled (LimitTimeoutSec > 0).
	// Otherwise, the lot will use the immediate (taker) exit path.
	usePendingMakerExit := lot.ExitMode == ExitModeScalpFixedTP &&
		!isL2DeepLoss &&
		t.cfg.LimitTimeoutSec > 0

	// =============================================================================
	// CASE 3 - SELL LOSS RECOVERY & PROTECTION
	//
	// Case 3 introduces two complementary strategies:
	//   • Case 3A - Recover intelligently in a continuing DOWN regime
	//   • Case 3B - Prevent repeating weak SELLs in an UP regime.
	//
	// Sufficient spare base
	// 		→ Case 3A Mode A in any regime
	// 		→ replacement SELL should start before closing the losing SELL
	// Insufficient spare + regime DOWN
	// 		→ Case 3A Mode B
	// Insufficient spare + regime UP/NORMAL
	// 		→ Case3A.modeA.blocked
	// 		→ no replacement
	// =============================================================================

	Case3ALossUSD := 0.0
	// Prepare an empty PendingIntent in case Case 3A recovery becomes necessary.
	var repl PendingIntent
	// Estimate the exit fee
	estExitFee := quote * (t.cfg.FeeRatePct / 100.0)
	// Estimate the commission that would be paid if the position were closed at the current price.
	// Calculate the gross trading P&L (before fees)
	gross := (livePrice - lot.OpenPrice) * baseRequested
	if lot.Side == SideSell {
		gross = (lot.OpenPrice - livePrice) * baseRequested
	}

	// Calculate the estimated net P&L
	net := gross - lot.EntryFee - estExitFee

	if lot.Side == SideSell &&
		strings.HasPrefix(exitReason, "threshold_stop_loss") {

		// =============================================================================
		// Case 3A - SELL Stop-Loss Recovery
		// -----------------------------------
		// Trigger:
		//     SELL threshold_stop_loss with loss
		//             ↓
		//     Recovery Mode A:
		//         Any regime with sufficient spare base.
		//     Recovery Mode B:
		//         DOWN regime when Mode A is not possible.
		// Philosophy:
		//    The SELL thesis may still be correct. Instead of abandoning the position,
		//    immediately attempt a structured recovery.
		// Two recovery methods exist.
		//	  *  Recovery Mode A (RecoveryByPositionSize)
		//	  *  Recovery Mode B (RecoveryByProfitTarget)
		//===============================================================================

		if net < 0 {
			Case3ALossUSD = -net

			// log.Printf(
			// "[TRACE] Case3A.detect side=%s closeSide=%s regime=%s entry_price=%.8f stop_price=%.8f base=%.8f net_loss=%.6f",
			// lot.Side,
			// closeSide,
			// t.MarketRegime,
			// lot.OpenPrice,
			// livePrice,
			// baseRequested,
			// Case3ALossUSD,
			// )

			recoveryNetUSD := Case3ALossUSD

			if recoveryNetUSD > 0 {
				beforeDebt := t.RecoveryDebtUSD
				afterDebt := beforeDebt + recoveryNetUSD

				log.Printf(
					"[TRACE] Case3A.recovery_preview side=%s loss=%.6f recovery=%.6f debt_before=%.6f debt_after=%.6f",
					lot.Side,
					Case3ALossUSD,
					recoveryNetUSD,
					beforeDebt,
					afterDebt,
				)

				replacementEntryPrice := livePrice
				recoveryExitPrice := lot.OpenPrice
				priceMove := replacementEntryPrice - recoveryExitPrice

				if priceMove > 0 {

					// Mode A sizing:
					// The extra SELL position must recover the realized stop-loss when price
					// falls from the replacement entry price back to the original SELL entry price.
					recoveryPerBase := priceMove

					if recoveryPerBase <= 0 {
						// log.Printf(
						// "[TRACE] Case3A.recovery_per_base.skip side=%s regime=%s recovery_net=%.6f price_move=%.8f recovery_per_base=%.8f reason=non_positive_recovery_move",
						// lot.Side,
						// t.MarketRegime,
						// recoveryNetUSD,
						// priceMove,
						// recoveryPerBase,
						// )
					} else {
						extraBase := recoveryNetUSD / recoveryPerBase
						if t.cfg.BaseStep > 0 {
							extraBase = math.Ceil(extraBase/t.cfg.BaseStep) * t.cfg.BaseStep
						}

						normalBase := baseRequested

						freshSpareBase, freshBaseStep, err := t.currentSpareBaseLocked(ctx)
						if err != nil {
							// log.Printf("[TRACE] Case3A.spare_base.failed err=%v", err)
							freshSpareBase = 0
						}

						if freshBaseStep > 0 {
							normalBase = math.Floor(normalBase/freshBaseStep) * freshBaseStep
							extraBase = math.Ceil(extraBase/freshBaseStep) * freshBaseStep
						}

						modeARequiredBase := normalBase + extraBase

						switch {
						case freshSpareBase >= modeARequiredBase:
							// Mode A is permitted in every regime, including UP.
							repl = PendingIntent{
								Enabled:             true,
								Side:                SideSell,
								LimitPx:             replacementEntryPrice,
								BaseAtLimit:         modeARequiredBase,
								RecoveryNetUSD:      recoveryNetUSD,
								RecoveryMethod:      RecoveryByPositionSize,
								ProfitGateUSD:       t.cfg.ProfitGateUSD,
								SourceEntryOrderID:  lot.EntryOrderID,
								Producer:            EntryProducerCase3AReplacement,
								PendingCancelPolicy: PendingSignalCancelDisabled,
								ProducerReason: fmt.Sprintf(
									"case3A_replacement|"+
										"method=%s|"+
										"recovery_usd=%.6f|"+
										"regime=%s|"+
										"source_order_id=%s",
									RecoveryByPositionSize.String(),
									recoveryNetUSD,
									t.MarketRegime,
									lot.EntryOrderID,
								),
							}
						case t.MarketRegime == RegimeDown:
							// Mode B remains the insufficient-spare fallback only in DOWN regime.
							repl = PendingIntent{
								Enabled:             true,
								Side:                SideSell,
								LimitPx:             replacementEntryPrice,
								BaseAtLimit:         normalBase,
								RecoveryNetUSD:      recoveryNetUSD,
								RecoveryMethod:      RecoveryByProfitTarget,
								ProfitGateUSD:       t.cfg.ProfitGateUSD + recoveryNetUSD,
								SourceEntryOrderID:  lot.EntryOrderID,
								Producer:            EntryProducerCase3AReplacement,
								PendingCancelPolicy: PendingSignalCancelDisabled,
								ProducerReason: fmt.Sprintf(
									"case3A_replacement|"+
										"method=%s|"+
										"recovery_usd=%.6f|"+
										"regime=%s|"+
										"source_order_id=%s",
									RecoveryByProfitTarget.String(),
									recoveryNetUSD,
									t.MarketRegime,
									lot.EntryOrderID,
								),
							}

						default:
							// UP/NORMAL with insufficient spare cannot safely run Mode A.
							// Do not fabricate an oversized replacement request.
							// log.Printf(
							// "[TRACE] Case3A.modeA.blocked side=%s regime=%s spare_base=%.8f required_base=%.8f normal_base=%.8f extra_base=%.8f recovery=%.6f",
							// lot.Side,
							// t.MarketRegime,
							// freshSpareBase,
							// modeARequiredBase,
							// normalBase,
							// extraBase,
							// recoveryNetUSD,
							// )
						}

						if repl.Enabled {
							// log.Printf(
							// "[TRACE] Case3A.recovery_mode side=%s spare_base=%.8f normal_base=%.8f extra_base=%.8f method=%s replacement_base=%.8f replacement_notional=%.2f profit_gate=%.6f reason=%s",
							// lot.Side,
							// freshSpareBase,
							// normalBase,
							// extraBase,
							// repl.RecoveryMethod.String(),
							// repl.BaseAtLimit,
							// repl.Quote,
							// repl.ProfitGateUSD,
							// repl.ProducerReason,
							// )
						}
					}

				} else {
					// log.Printf(
					// "[TRACE] Case3A.extra_base.skip side=%s recovery_net=%.6f stop_entry=%.8f recovery_exit=%.8f reason=no_positive_sell_recovery_move",
					// lot.Side,
					// recoveryNetUSD,
					// replacementEntryPrice,
					// recoveryExitPrice,
					// )
				}
			}
		}
	}

	// ============================================================================
	// Case 3A Mode A - replacement must start before the losing SELL is closed.
	// ============================================================================
	//
	// Mode A is only valid if the replacement entry is successfully started first.
	// The Case3A source wrapper:
	//   - receives the existing ProducerAttempt;
	//   - enriches it with produced/pending/failure/cleanup events;
	//   - returns replacement order ID and error.
	//
	// closeLot() is the higher-level Case3A owner, so it records the returned
	// ProducerAttempt into producerHistory after the wrapper has completed its
	// immediate cleanup handling.
	//
	if repl.Enabled &&
		repl.RecoveryMethod == RecoveryByPositionSize {

		if lot.Case3AReplacementStarted {
			// Replacement already exists for this source lot.
			// Do not create another Case3A producer attempt.

		} else {
			attempt := newProducerIntentLifecycle(
				&repl,
			)
			if attempt == nil {
				return "", errors.New(
					"Case3A modeA: failed to create producer lifecycle",
				)
			}

			/*
				startCase3AReplacement() enters produceEntry(), whose registration
				path acquires t.mu. closeLot() currently owns t.mu here, so release
				it before entering the replacement pipeline to avoid a self-deadlock.

				After the wrapper returns, reacquire t.mu before touching local lot
				state or producerHistory.
			*/
			t.mu.Unlock()

			oid, err := t.startCase3AReplacement(
				ctx,
				&repl,
				attempt,
			)

			t.mu.Lock()

			/*
				The mutex was released while the exchange-facing replacement path
				ran. Refresh the source lot by its stable EntryOrderID before
				mutating it; never trust the old lot pointer across that unlock.
			*/
			book = t.book(side)
			currentIdx := t.findLotIndexByEntryIDLocked(
				side,
				entryOrderID,
			)
			if currentIdx < 0 {
				return "", fmt.Errorf(
					"Case3A modeA replacement returned but source lot disappeared "+
						"side=%s entry_id=%s replacement_order_id=%s",
					side,
					entryOrderID,
					oid,
				)
			}

			localIdx = currentIdx
			lot = book.Lots[localIdx]

			/*
				Record the Case3A attempt regardless of success or failure.

				The attempt already contains:
				  - stage=produced from startCase3AReplacement();
				  - stage=pending or stage=entry_failed from produceEntry();
				  - cleanup_cancelled / cleanup_cancel_failed when applicable.

				closeLot() is the first higher-level owner above the Case3A wrapper.
				t.mu is held again here, as required by recordProducerAttemptLocked().
			*/
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

			if err != nil {
				/*
					Mode A requires the replacement to start successfully before
					the losing SELL may close. The failed attempt has already been
					recorded, so abort the loss exit and propagate the error.
				*/
				return "", fmt.Errorf(
					"Case3A modeA replacement failed; "+
						"loss exit aborted entry_id=%s: %w",
					entryOrderID,
					err,
				)
			}

			// Replacement successfully started; mark the refreshed source lot.
			lot.Case3AReplacementStarted = true
			lot.Case3AReplacementOrderID = oid

			if err := t.saveStateNoLock(); err != nil {
				log.Printf(
					"[WARN] saveState Case3A source flag: %v",
					err,
				)
			}
		}
	}

	if usePendingMakerExit && strings.TrimSpace(lot.FixedTPOrderID) != "" {
		return fmt.Sprintf(
			"PENDING_EXIT_EXISTS %s side=%s entry_id=%s exit_id=%s reason=%s",
			exitTime.Format(time.RFC3339),
			lot.Side,
			lot.EntryOrderID,
			lot.FixedTPOrderID,
			exitReason,
		), nil
	}

	t.mu.Unlock()

	var placed *PlacedOrder

	if usePendingMakerExit {

		limitPx := lot.Take

		if limitPx <= 0 {
			limitPx = livePrice

			offBps := t.cfg.TPMakerOffsetBps
			if closeSide == SideSell && offBps > 0 {
				limitPx = livePrice * (1.0 + offBps/10000.0)
			}
			if closeSide == SideBuy && offBps > 0 {
				limitPx = livePrice * (1.0 - offBps/10000.0)
			}

			// log.Printf(
			// "[TRACE] pending_exit.maker_px side=%s entry_id=%s take=%.8f live=%.8f maker_px=%.8f",
			// lot.Side,
			// lot.EntryOrderID,
			// lot.Take,
			// livePrice,
			// limitPx,
			// )

		}

		if t.cfg.PriceTick > 0 {
			if closeSide == SideSell {
				limitPx = math.Ceil(limitPx/t.cfg.PriceTick) * t.cfg.PriceTick
			} else {
				limitPx = math.Floor(limitPx/t.cfg.PriceTick) * t.cfg.PriceTick
			}
		}

		err := t.startPendingMakerExit(ctx, lot.Side, lot.EntryOrderID, side, exitReason, exitDecision, limitPx, baseRequested)
		t.mu.Lock()

		waitID := ""

		book = t.book(side)

		currentIdx := t.findLotIndexByEntryIDLocked(side, entryOrderID)
		if currentIdx >= 0 {
			waitID = book.Lots[currentIdx].FixedTPOrderID
		}

		if err != nil {
			// log.Printf("[TRACE] pending_exit.start_failed side=%s entry_id=%s err=%v", lot.Side, lot.EntryOrderID, err)
			return "", nil
		}

		/*
			Case 3A Mode B - Recovery by Profit Target

			Mode B applies when:
			  - the losing position is a SELL;
			  - the SELL is being stopped out at a loss;
			  - Mode A cannot be used because there is not enough spare base;
			  - the market regime is DOWN.

			Strategy:
			  - close the losing SELL;
			  - open a normal-sized replacement SELL;
			  - increase the replacement's profit target by the realized recovery amount;
			  - allow the replacement to recover the prior loss through future profit.

			If the replacement cannot be started immediately:
			  - retry only failures explicitly classified as retryable;
			  - do not blindly retry cleanup-uncertain, invalid, or unknown failures.

			When the source SELL is using a pending maker exit, the replacement may be
			prepared while that exit is pending, but any deferred Mode B retry must wait
			until the originating losing position has actually committed its exit.
		*/
		attempt := newProducerIntentLifecycle(
			&repl,
		)
		if attempt == nil {
			return "", errors.New(
				"Case3A modeB pending-exit: failed to create producer lifecycle",
			)
		}

		t.mu.Unlock()

		_, replErr := t.startCase3AReplacement(
			ctx,
			&repl,
			attempt,
		)

		t.mu.Lock()

		// t.mu is held again here; record the same enriched attempt mechanically.
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

		if replErr != nil {
			/*
				The wrapper has already performed immediate cleanup when required.
				This higher-level handler decides retry / non-retry /
				cleanup-uncertain policy for the returned EntryProduceError.
			*/
			t.handleCase3AReplacementError(
				repl,
				waitID,
				replErr,
			)
		}

		return fmt.Sprintf(
			"PENDING_EXIT %s side=%s entry_id=%s limit=%.2f base=%.8f reason=%s",
			exitTime.Format(time.RFC3339),
			side,
			entryOrderID,
			limitPx,
			baseRequested,
			exitReason,
		), nil
	}

	// log.Printf(
	// "[TRACE] order.close.request lotSide=%s closeSide=%s idx=%d entry_id=%s reason=%s decision=%s net=%.6f gate=%.6f baseReq=%.8f quoteEst=%.2f livePrice=%.8f mode=%s",
	// lot.Side,
	// closeSide,
	// localIdx,
	// lot.EntryOrderID,
	// exitReason,
	// exitDecision,
	// net,
	// t.lotProfitGateUSD(lot),
	// baseRequested,
	// quote,
	// livePrice,
	// lot.ExitMode,
	// )

	var err error
	placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote)

	// log.Printf("[KPI] taker.exit.done side=%s base=%.8f quote_est=%.2f reason=%s", closeSide, baseRequested, quote, exitReason)

	if err != nil {
		if t.cfg.UseDirectSlack {
			postSlack(fmt.Sprintf("ERR step: %v", err))
		}
		t.mu.Lock()
		return "", fmt.Errorf("close order failed: %w", err)
	}

	if placed != nil {
		// log.Printf("[TRACE] order.close placed price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f", placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
	}

	// after market close succeeds and logs order.close placed

	t.mu.Lock()

	book = t.book(side)

	currentIdx := t.findLotIndexByEntryIDLocked(side, entryOrderID)
	if currentIdx < 0 {
		return "", fmt.Errorf(
			"market exit filled but local lot disappeared side=%s entry_id=%s exit_id=%s",
			side,
			entryOrderID,
			placedOrderID(placed),
		)
	}

	localIdx = currentIdx
	lot = book.Lots[localIdx]

	wasNewest := localIdx == len(book.Lots)-1
	priceExec := livePrice
	baseFilled := baseRequested
	commissionUSD := 0.0

	if placed != nil {
		if placed.Price > 0 {
			priceExec = placed.Price
		}
		if placed.BaseSize > 0 {
			baseFilled = placed.BaseSize
		}
		if placed.CommissionUSD > 0 {
			commissionUSD = placed.CommissionUSD
		}
	}

	// ============================================================================
	// Case 3A Mode B - replacement after the losing SELL exit was accepted.
	// ============================================================================
	//
	// At this point the exchange has already accepted the source loss exit.
	// The Case3A replacement is therefore attempted immediately before the
	// heavier local exit bookkeeping.
	//
	// Unlike Mode A, a replacement failure does not roll back the already
	// accepted source exit. Instead, Case3A policy decides whether the
	// replacement is retryable.
	//
	if repl.Enabled &&
		repl.RecoveryMethod == RecoveryByProfitTarget {

		/*
			The source loss exit has already been accepted by the exchange.
			startCase3AReplacement() may acquire t.mu inside produceEntry(), so
			release closeLot's lock while the replacement pipeline runs.
		*/
		attempt := newProducerIntentLifecycle(
			&repl,
		)
		if attempt == nil {
			return "", errors.New(
				"Case3A modeB market-exit: failed to create producer lifecycle",
			)
		}

		t.mu.Unlock()

		_, replErr := t.startCase3AReplacement(
			ctx,
			&repl,
			attempt,
		)

		t.mu.Lock()

		/*
			Record the same ProducerAttempt that was created before the
			Case3A wrapper and enriched by produced / pending / failure /
			cleanup lifecycle handling.
		*/
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

		if replErr != nil {
			/*
				Case3A policy is deliberately applied above the source wrapper.

				Known retryable:
				  - post_only_submit_failed
				  - persist_state_failed

				Cleanup uncertainty:
				  - cleanup_cancel_failed

				Known validation/build/register failures:
				  - non-retryable

				Unknown codes:
				  - non-retryable by default and logged for later classification.
			*/
			t.handleCase3AReplacementError(
				repl,
				placedOrderID(placed),
				replErr,
			)
		}

		/*
			The mutex was released during replacement submission. Refresh the
			source lot before applying the already-filled exit to local state.
		*/
		book = t.book(side)
		currentIdx = t.findLotIndexByEntryIDLocked(
			side,
			entryOrderID,
		)
		if currentIdx < 0 {
			return "", fmt.Errorf(
				"Case3A modeB replacement returned but source lot disappeared "+
					"side=%s entry_id=%s exit_id=%s",
				side,
				entryOrderID,
				placedOrderID(placed),
			)
		}

		localIdx = currentIdx
		lot = book.Lots[localIdx]
		wasNewest = localIdx == len(book.Lots)-1
	}

	msg, err := t.applyFilledExitLocked(
		livePrice,
		priceExec,
		baseRequested,
		baseFilled,
		side,
		localIdx,
		exitReason,
		exitDecision,
		exitTime,
		placedOrderID(placed),
		commissionUSD,
		minNotional,
		wasNewest,
	)

	return msg, err

}

// findLotIndexByEntryIDLocked returns the current lot index.
//
// Caller must hold t.mu.
//
// EntryOrderID is authoritative because slice indexes can change when
// concurrent exit workers remove earlier lots.
func (t *Trader) findLotIndexByEntryIDLocked(
	side OrderSide,
	entryOrderID string,
) int {
	entryOrderID = strings.TrimSpace(entryOrderID)
	if entryOrderID == "" {
		return -1
	}

	book := t.book(side)
	if book == nil {
		return -1
	}

	for i, lot := range book.Lots {
		if lot == nil {
			continue
		}

		if strings.TrimSpace(lot.EntryOrderID) == entryOrderID {
			return i
		}
	}

	return -1
}

func (t *Trader) currentSpareBaseLocked(ctx context.Context) (float64, float64, error) {
	var reservedLongBase float64

	if bb := t.book(SideBuy); bb != nil {
		for _, lot := range bb.Lots {
			reservedLongBase += lot.SizeBase
		}
	}

	if t.cfg.RequireBaseForShort {
		for _, entry := range t.pendingEntries {
			if entry == nil ||
				entry.Completed ||
				entry.Intent == nil ||
				entry.Side != SideSell {
				continue
			}

			reservedLongBase += entry.Intent.BaseAtLimit
		}
	}

	t.mu.Unlock()
	_, availBase, baseStep, err := t.broker.GetAvailableBase(ctx, t.cfg.ProductID)
	t.mu.Lock()

	if err != nil {
		return 0, 0, err
	}
	if baseStep <= 0 {
		return 0, 0, fmt.Errorf("invalid baseStep %.8f", baseStep)
	}

	spareBase := availBase - reservedLongBase
	if spareBase < 0 {
		spareBase = 0
	}

	return spareBase, baseStep, nil
}

func (t *Trader) applyFilledExitLocked(livePrice float64, priceExec float64, baseRequested float64, baseFilled float64, side OrderSide, localIdx int, exitReason string, exitDecision string, exitTime time.Time, exitOrderID string, commissionUSD float64, minNotional float64, wasNewest bool) (string, error) {
	_ = livePrice

	book := t.book(side)
	if localIdx < 0 || localIdx >= len(book.Lots) {
		return "", fmt.Errorf("applyFilledExitLocked: invalid localIdx=%d side=%s", localIdx, side)
	}

	lot := book.Lots[localIdx]

	entryPortion := 0.0
	if baseRequested > 0 {
		entryPortion = lot.EntryFee * (baseFilled / baseRequested)
	}

	pl := (priceExec - lot.OpenPrice) * baseFilled
	if lot.Side == SideSell {
		pl = (lot.OpenPrice - priceExec) * baseFilled
	}

	quoteExec := baseFilled * priceExec
	exitFee := quoteExec * (t.cfg.FeeRatePct / 100.0)
	if commissionUSD > 0 {
		exitFee = commissionUSD
	}

	pl -= entryPortion
	pl -= exitFee

	rawPL := func() float64 {
		if lot.Side == SideBuy {
			return (priceExec - lot.OpenPrice) * baseFilled
		}
		return (lot.OpenPrice - priceExec) * baseFilled
	}()

	removedWasRunner := false
	kind := "scalp"
	for _, rid := range book.RunnerIDs {
		if rid == localIdx {
			removedWasRunner = true
			kind = "runner"
			break
		}
	}

	log.Printf("[TRACE] exit.classify side=%s kind=%s reason=%s open=%.8f exec=%.8f baseFilled=%.8f rawPL=%.6f entryFee=%.6f exitFee=%.6f finalPL=%.6f", lot.Side, kind, exitReason, lot.OpenPrice, priceExec, baseFilled, rawPL, entryPortion, exitFee, pl)

	t.dailyPnL += pl
	t.equityUSD += pl

	if lot.Side == SideBuy {
		t.SpareBuyUSD += quoteExec
		if t.SpareBuyUSD < 0 {
			t.SpareBuyUSD = 0
		}
	} else if lot.Side == SideSell {
		t.SpareSellUSD += quoteExec
		if t.SpareSellUSD < 0 {
			t.SpareSellUSD = 0
		}
	}

	rec := ExitRecord{
		Time:             exitTime,
		Side:             lot.Side,
		OpenPrice:        lot.OpenPrice,
		ClosePrice:       priceExec,
		SizeBase:         baseFilled,
		OpenNotionalUSD:  lot.OpenNotionalUSD,
		EntryFeeUSD:      entryPortion,
		ExitFeeUSD:       exitFee,
		PNLUSD:           pl,
		Reason:           exitReason + " | exitReason{" + exitDecision + "}  ||  openReason{" + lot.ProducerReason + "}",
		ExitMode:         lot.ExitMode,
		WasRunner:        removedWasRunner,
		RefundPortionUSD: lot.RefundPortionUSD,
		EntryOrderID:     lot.EntryOrderID,
		ExitOrderID:      exitOrderID,
		Version:          Version,
	}

	t.applyRecoveryDebtFromExit(rec.PNLUSD)

	// log.Printf(
	// "[TRACE] recovery.exit pnl=%.4f debt_after=%.4f",
	// rec.PNLUSD,
	// t.RecoveryDebtUSD,
	// )

	t.lastExits = append(t.lastExits, rec)

	/*
	   Producer economics must capture every authoritative realized exit
	   contribution before any partial-exit residual is resized,
	   consolidated, archived as dust, or otherwise transformed.

	   This records realized NET PnL onto the SAME ProducerAttempt.

	   It does NOT mark ProducerStageExited. A partial exit may still leave
	   live producer exposure in SideBook.Lots.
	*/
	if t.recordProducerRealizedPnLLocked(
		lot,
		rec,
	) {
		if err := t.saveProducerHistoryNoLock(); err != nil {
			log.Printf(
				"[ERROR] producer.history.save_failed "+
					"stage=realized_pnl producer=%s "+
					"entry_order_id=%s exit_order_id=%s err=%v",
				lot.Producer,
				rec.EntryOrderID,
				rec.ExitOrderID,
				err,
			)
		}
	}

	capN := t.cfg.ExitHistorySize

	if capN <= 0 {
		capN = 8
	}
	archiveAndPruneExits(t.exitsArchivePath(), &t.lastExits, capN)

	const tolExit = 1e-9
	isPartial := baseFilled+tolExit < baseRequested

	if isPartial {
		lot.SizeBase = baseRequested - baseFilled
		lot.EntryFee -= entryPortion
		if lot.EntryFee < 0 {
			lot.EntryFee = 0
		}

		if priceExec > 0 && minNotional > 0 {
			t.consolidateDust(book, priceExec, minNotional)
			t.archiveOrphanDust(book, priceExec, minNotional)
		}

		/*
		   This was an exchange partial exit, but residual processing may have
		   removed the original EntryOrderID from the authoritative SideBook.

		   Realized PnL for this ExitRecord was already accumulated earlier.

		   Only now, after residual sizing/dust handling, determine whether the
		   original producer exposure is still live.
		*/
		if t.markProducerExitedIfNotLiveLocked(
			lot,
			rec,
		) {
			if err := t.saveProducerHistoryNoLock(); err != nil {
				log.Printf(
					"[ERROR] producer.history.save_failed "+
						"stage=exited producer=%s "+
						"entry_order_id=%s exit_order_id=%s err=%v",
					lot.Producer,
					rec.EntryOrderID,
					rec.ExitOrderID,
					err,
				)
			}
		}

		msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)", exitTime.Format(time.RFC3339), priceExec, exitReason, lot.ProducerReason, pl, entryPortion+exitFee)
		if t.cfg.UseDirectSlack {
			postSlack(msg)
		}
		_ = t.saveStateNoLock()
		return msg, nil
	}

	book.Lots = append(book.Lots[:localIdx], book.Lots[localIdx+1:]...)

	if len(book.RunnerIDs) > 0 {
		out := book.RunnerIDs[:0]
		for _, rid := range book.RunnerIDs {
			if rid == localIdx {
				continue
			}
			if rid > localIdx {
				rid--
			}
			out = append(out, rid)
		}
		book.RunnerIDs = append([]int(nil), out...)
	}

	if priceExec > 0 && minNotional > 0 {
		t.consolidateDust(book, priceExec, minNotional)
		t.archiveOrphanDust(book, priceExec, minNotional)
	}

	/*
	   This is the full-exit branch.

	   The authoritative lot has already been removed from SideBook.Lots,
	   and any subsequent dust/consolidation transformations for this tick
	   have completed.

	   Realized PnL for this ExitRecord was already accumulated earlier.

	   If the original producer EntryOrderID is no longer present in either
	   authoritative SideBook, mark the SAME ProducerAttempt exited.
	*/
	if t.markProducerExitedIfNotLiveLocked(
		lot,
		rec,
	) {
		if err := t.saveProducerHistoryNoLock(); err != nil {
			log.Printf(
				"[ERROR] producer.history.save_failed "+
					"stage=exited producer=%s "+
					"entry_order_id=%s exit_order_id=%s err=%v",
				lot.Producer,
				rec.EntryOrderID,
				rec.ExitOrderID,
				err,
			)
		}
	}

	if wasNewest {
		now := time.Now().UTC()
		if lot.Side == SideBuy {
			t.lastAddBuy = now
			t.winLowBuy = 0
			t.latchedGateBuy = 0
		} else {
			t.lastAddSell = now
			t.winHighSell = 0
			t.latchedGateSell = 0
		}
	}

	if removedWasRunner {
		if lot.Side == SideBuy && t.equityStageBuy > 0 {
			t.equityStageBuy--
		}
		if lot.Side == SideSell && t.equityStageSell > 0 {
			t.equityStageSell--
		}
	}

	if len(book.Lots) == 0 {
		if lot.Side == SideBuy {
			t.equityStageBuy = 0
		} else {
			t.equityStageSell = 0
		}
	}

	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)", exitTime.Format(time.RFC3339), priceExec, exitReason, lot.ProducerReason, pl, entryPortion+exitFee)
	if t.cfg.UseDirectSlack {
		postSlack(msg)
	}
	_ = t.saveStateNoLock()
	return msg, nil
}

func (t *Trader) maybeCloseDustBasket(ctx context.Context, side OrderSide, livePrice float64) (string, bool, error) {
	if livePrice <= 0 {
		return "", false, nil
	}

	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}

	var dust []*Position
	if side == SideBuy {
		dust = t.dustBuyLots
	} else {
		dust = t.dustSellLots
	}

	if len(dust) == 0 {
		return "", false, nil
	}

	totalBase := 0.0
	totalOpenNotional := 0.0
	totalEntryFee := 0.0
	var entryIDs []string

	for _, lot := range dust {
		if lot == nil || lot.SizeBase <= 0 {
			continue
		}
		totalBase += lot.SizeBase
		totalOpenNotional += lot.OpenPrice * lot.SizeBase
		totalEntryFee += lot.EntryFee
		if strings.TrimSpace(lot.EntryOrderID) != "" {
			entryIDs = append(entryIDs, lot.EntryOrderID)
		}
	}

	baseRequested := floorToStep(totalBase, t.cfg.BaseStep)
	if baseRequested <= 0 {
		return "", false, nil
	}

	notional := baseRequested * livePrice
	if notional < minNotional {
		return "", false, nil
	}

	closeSide := SideSell
	if side == SideSell {
		closeSide = SideBuy
	}

	exitTime := time.Now().UTC()

	t.mu.Unlock()

	var placed *PlacedOrder
	var err error

	placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, notional)
	if err != nil {
		t.mu.Lock()
		return "", false, err
	}

	t.mu.Lock()

	priceExec := livePrice
	baseFilled := baseRequested
	exitOrderID := ""

	if placed != nil {
		if placed.Price > 0 {
			priceExec = placed.Price
		}
		if placed.BaseSize > 0 {
			baseFilled = placed.BaseSize
		}
		exitOrderID = placed.ID
	}

	exitFee := baseFilled * priceExec * (t.cfg.FeeRatePct / 100.0)
	if placed != nil && placed.CommissionUSD > 0 {
		exitFee = placed.CommissionUSD
	}

	weightedOpen := 0.0
	if totalBase > 0 {
		weightedOpen = totalOpenNotional / totalBase
	}

	gross := 0.0
	for _, lot := range dust {
		if lot == nil || lot.SizeBase <= 0 {
			continue
		}
		if side == SideBuy {
			gross += (priceExec - lot.OpenPrice) * lot.SizeBase
		} else {
			gross += (lot.OpenPrice - priceExec) * lot.SizeBase
		}
	}

	pl := gross - totalEntryFee - exitFee

	t.dailyPnL += pl
	t.equityUSD += pl

	rec := ExitRecord{
		Time:            exitTime,
		Side:            side,
		OpenPrice:       weightedOpen,
		ClosePrice:      priceExec,
		SizeBase:        baseFilled,
		OpenNotionalUSD: totalOpenNotional,
		EntryFeeUSD:     totalEntryFee,
		ExitFeeUSD:      exitFee,
		PNLUSD:          pl,
		Reason:          "dust_basket_cleanup",
		ExitMode:        ExitModeDustBasket,
		WasRunner:       false,
		EntryOrderID:    "basket:" + strings.Join(entryIDs, ","),
		ExitOrderID:     exitOrderID,
		Version:         Version,
	}

	t.lastExits = append(t.lastExits, rec)

	capN := t.cfg.ExitHistorySize
	if capN <= 0 {
		capN = 8
	}
	archiveAndPruneExits(t.exitsArchivePath(), &t.lastExits, capN)

	if side == SideBuy {
		t.dustBuyLots = nil
	} else {
		t.dustSellLots = nil
	}

	msg := fmt.Sprintf(
		"DUST-BASKET-EXIT %s side=%s closeSide=%s base=%.8f open=%.2f close=%.2f pnl=%.4f notional=%.2f",
		exitTime.Format(time.RFC3339),
		side,
		closeSide,
		baseFilled,
		weightedOpen,
		priceExec,
		pl,
		notional,
	)

	// log.Printf("[TRACE] dust.basket.close %s", msg)

	_ = t.saveStateNoLock()
	return msg, true, nil
}

type PendingEntry struct {
	OrderID             string
	Side                OrderSide
	Producer            EntryProducer
	ProducerReason      string
	PendingCancelPolicy PendingSignalCancelPolicy
	Intent              *PendingIntent

	ResultC chan OpenResult    `json:"-"`
	Cancel  context.CancelFunc `json:"-"`

	Book        *SideBook  `json:"-"`
	SpareUSD    *float64   `json:"-"`
	LastAdd     *time.Time `json:"-"`
	WinExtreme  *float64   `json:"-"`
	LatchedGate *float64   `json:"-"`

	EquityTriggered bool

	Completed bool

	clearOwner func() `json:"-"`
	// Optional gate evaluated immediately before committing a completed
	// broker result. Returning false postpones commit until a later tick.
	CommitEligible func(*PendingEntry) bool `json:"-"`
}

type PendingIntent struct {
	Enabled  bool
	Producer EntryProducer

	Side           OrderSide
	LimitPx        float64
	BaseAtLimit    float64
	Quote          float64
	Take           float64
	ProducerReason string

	RefundPortionUSD float64 `json:"refund_portion_usd"`

	ProductID  string
	DecisionID string
	CreatedAt  time.Time
	Deadline   time.Time

	EquityBuy  bool
	EquitySell bool

	// Current live exchange order ID.
	OrderID string

	// Late-fill/reprice tracking.
	History []string `json:"history,omitempty"`

	AccumBase   float64
	AccumQuote  float64
	AccumFeeUSD float64

	ConfidenceMult float64 `json:"confidence_mult,omitempty"`
	ProfitGateUSD  float64 `json:"profit_gate_usd,omitempty"`
	EntryMethod    string  `json:"entry_method,omitempty"`

	// Case3A recovery metadata.
	RecoveryNetUSD float64        `json:"recovery_net_usd,omitempty"`
	RecoveryMethod RecoveryMethod `json:"recovery_method,omitempty"`

	CancelRequested bool `json:"cancel_requested,omitempty"`

	// Empty for normal entries.
	// Case3A uses this to identify the entry that caused the replacement.
	SourceEntryOrderID  string                    `json:"source_entry_order_id,omitempty"`
	PendingCancelPolicy PendingSignalCancelPolicy `json:"pending_cancel_policy,omitempty"`
}

func (t *Trader) positionExistsByEntryOrderID(orderID string) bool {
	if t == nil || orderID == "" {
		return false
	}

	for _, side := range []OrderSide{SideBuy, SideSell} {
		book := t.book(side)
		if book == nil {
			continue
		}

		for _, lot := range book.Lots {
			if lot == nil {
				continue
			}

			if lot.EntryOrderID == orderID {
				return true
			}
		}
	}

	return false
}
func pendingEntryID(side OrderSide, orderID string) string {
	if orderID == "" {
		return string(side)
	}
	return fmt.Sprintf("%s:%s", side, orderID)
}
func (t *Trader) ensurePendingEntries() {
	if t.pendingEntries == nil {
		t.pendingEntries = make(map[string]*PendingEntry)
	}
}

// Entry Production:
/*
	source wrapper
		↓
	construct PendingIntent
		↓
	produceEntry
		↓
	submit maker order
		↓
	construct PendingEntry
		↓
	pendingEntries[OrderID]
		↓
	generic poll/reprice lifecycle
		↓
	OpenResult
		↓
	generic commit
*/
// Buy Source Wrapper
func (t *Trader) startProducerBuyEntry(
	ctx context.Context,
	intent *PendingIntent,
	attempt *ProducerAttempt,
) (*PendingEntry, error) {

	if intent == nil {
		return nil, fmt.Errorf(
			"startProducerBuyEntry: nil intent",
		)
	}

	if attempt == nil {
		return nil, fmt.Errorf(
			"startProducerBuyEntry: nil producer attempt",
		)
	}

	if intent.Producer == EntryProducerNone {
		return nil, fmt.Errorf(
			"startProducerBuyEntry: missing entry producer",
		)
	}

	if intent.Side != SideBuy {
		return nil, fmt.Errorf(
			"startProducerBuyEntry: invalid side=%s",
			intent.Side,
		)
	}

	if attempt.Events == nil {
		attempt.Events =
			make(map[ProducerStage]ProducerEvent)
	}

	attempt.Events[ProducerStageProduced] =
		ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: intent.CreatedAt,

			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Stage:    ProducerStageProduced,

			DecisionID: intent.DecisionID,

			Reason: intent.ProducerReason,
		}

	entry, err := t.produceEntry(
		ctx,
		intent,
		attempt,
	)

	if err != nil {
		err = t.handleEntryProduceError(
			ctx,
			intent,
			attempt,
			err,
		)

		return nil, err
	}

	return entry, nil
}

// Sell Source Wrapper
func (t *Trader) startProducerSellEntry(
	ctx context.Context,
	intent *PendingIntent,
	attempt *ProducerAttempt,
) (*PendingEntry, error) {

	if intent == nil {
		return nil, fmt.Errorf(
			"startProducerSellEntry: nil intent",
		)
	}

	if attempt == nil {
		return nil, fmt.Errorf(
			"startProducerSellEntry: nil producer attempt",
		)
	}

	if intent.Producer == EntryProducerNone {
		return nil, fmt.Errorf(
			"startProducerSellEntry: missing entry producer",
		)
	}

	if intent.Side != SideSell {
		return nil, fmt.Errorf(
			"startProducerSellEntry: invalid side=%s",
			intent.Side,
		)
	}

	if attempt.Events == nil {
		attempt.Events =
			make(map[ProducerStage]ProducerEvent)
	}

	/*
		stage=produced belongs to this source wrapper.

		Time is when the wrapper is actually reached.

		CreatedAt and DecisionID remain the original Decision-stage
		lifecycle identity.
	*/
	attempt.Events[ProducerStageProduced] =
		ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: intent.CreatedAt,

			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Stage:    ProducerStageProduced,

			DecisionID: intent.DecisionID,

			Reason: intent.ProducerReason,
		}

	/*
		produceEntry() continues the SAME ProducerAttempt.

		It must not create or return another ProducerAttempt.
	*/
	entry, err := t.produceEntry(
		ctx,
		intent,
		attempt,
	)

	/*
		Cleanup remains wrapper-owned.

		handleEntryProduceError() enriches this same attempt with
		cleanup_cancelled / cleanup_cancel_failed when applicable.
	*/
	if err != nil {
		err = t.handleEntryProduceError(
			ctx,
			intent,
			attempt,
			err,
		)

		return nil, err
	}

	return entry, nil
}

// Case3A Source Wrapper
func (t *Trader) startCase3AReplacement(
	ctx context.Context,
	intent *PendingIntent,
	attempt *ProducerAttempt,
) (string, error) {

	if intent == nil {
		return "", errors.New(
			"Case3A replacement: nil intent",
		)
	}

	if attempt == nil {
		return "", errors.New(
			"Case3A replacement: nil producer attempt",
		)
	}

	if !intent.Enabled {
		return "", nil
	}

	sourceEntryOrderID := strings.TrimSpace(
		intent.SourceEntryOrderID,
	)
	if sourceEntryOrderID == "" {
		return "", errors.New(
			"Case3A replacement: missing SourceEntryOrderID",
		)
	}

	intent.ProductID = t.cfg.ProductID
	intent.SourceEntryOrderID = sourceEntryOrderID

	intent.EquityBuy = false
	intent.EquitySell = false
	intent.RefundPortionUSD = 0

	if intent.ConfidenceMult <= 0 {
		intent.ConfidenceMult = 1.0
	}

	if intent.History == nil {
		intent.History =
			make([]string, 0, 5)
	}

	// Quote must agree with the actual Case3A price/size.
	if intent.Quote <= 0 &&
		intent.LimitPx > 0 &&
		intent.BaseAtLimit > 0 {

		intent.Quote =
			intent.LimitPx *
				intent.BaseAtLimit
	}

	if attempt.Events == nil {
		attempt.Events =
			make(map[ProducerStage]ProducerEvent)
	}

	/*
		stage=produced belongs to this source wrapper.

		Time is when Case3A production is actually attempted.

		CreatedAt and DecisionID remain the original lifecycle
		identity created before this wrapper.
	*/
	attempt.Events[ProducerStageProduced] =
		ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: intent.CreatedAt,

			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Stage:    ProducerStageProduced,

			DecisionID: intent.DecisionID,

			Reason: intent.ProducerReason,
		}

	entry, err := t.produceEntry(
		ctx,
		intent,
		attempt,
	)

	/*
		Cleanup remains wrapper-owned.

		handleEntryProduceError() enriches this same attempt with
		cleanup_cancelled / cleanup_cancel_failed when applicable.
	*/
	if err != nil {
		err = t.handleEntryProduceError(
			ctx,
			intent,
			attempt,
			err,
		)

		return "", err
	}

	/*
		Internal invariant:
		nil error from produceEntry() must imply a non-nil entry.
	*/
	if entry == nil {
		return "", errors.New(
			"produceEntry returned nil entry with nil error",
		)
	}

	return entry.OrderID, nil
}

//Entry Producer:
/*validatePendingIntent()
		↓
submitPendingIntent()
		↓
buildPendingEntry()
		↓
registerPendingEntry()
		↓
startEntryPoller()
*/
func (t *Trader) produceEntry(
	ctx context.Context,
	intent *PendingIntent,
	attempt *ProducerAttempt,
) (*PendingEntry, error) {

	if intent == nil {
		return nil, fmt.Errorf(
			"produceEntry: nil intent",
		)
	}

	if attempt == nil {
		return nil, fmt.Errorf(
			"produceEntry: nil producer attempt",
		)
	}

	if attempt.Events == nil {
		attempt.Events =
			make(map[ProducerStage]ProducerEvent)
	}

	/*
		DecisionID, CreatedAt, ProducerAttempt, and PendingIntent
		were already created at Decision stage.

		produceEntry() continues that SAME lifecycle.

		It must never:
		  - create a new DecisionID;
		  - regenerate CreatedAt;
		  - create a replacement ProducerAttempt.
	*/

	addFailureEvent := func(
		produceErr *EntryProduceError,
	) {
		if produceErr == nil {
			return
		}

		event := ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: intent.CreatedAt,

			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Stage:    ProducerStageEntryFailed,

			DecisionID: intent.DecisionID,
			OrderID:    produceErr.OrderID,

			Reason: intent.ProducerReason,

			ErrorCode:       produceErr.Code,
			CleanupRequired: produceErr.CleanupRequired,
		}

		if produceErr.Err != nil {
			event.Error =
				produceErr.Err.Error()
		}

		attempt.Events[event.Stage] =
			event
	}

	// Validation helper owns and constructs its exact failure.
	if produceErr :=
		t.validatePendingIntent(
			intent,
		); produceErr != nil {

		addFailureEvent(
			produceErr,
		)

		return nil, produceErr
	}

	// Submission helper owns and constructs its exact failure.
	orderID, produceErr :=
		t.submitPendingIntent(
			ctx,
			intent,
		)

	if produceErr != nil {
		addFailureEvent(
			produceErr,
		)

		return nil, produceErr
	}

	// Build helper owns and constructs its exact failure.
	entry, produceErr :=
		t.buildPendingEntry(
			intent,
			orderID,
		)

	if produceErr != nil {
		addFailureEvent(
			produceErr,
		)

		return nil, produceErr
	}

	// Registration helper owns and constructs its exact failure.
	if produceErr :=
		t.registerPendingEntry(
			entry,
		); produceErr != nil {

		addFailureEvent(
			produceErr,
		)

		return nil, produceErr
	}

	/*
		Registration succeeded.

		Advance the same-side latch immediately so another same-side
		pending entry must achieve additional adverse movement before
		qualifying again.

		Save the latch together with the newly registered pending entry.
	*/
	oldBuyLatch :=
		t.latchedGateBuy

	oldSellLatch :=
		t.latchedGateSell

	nextLatch :=
		pendingRegistrationLatchPrice(
			intent.Side,
			intent.LimitPx,
			intent.BaseAtLimit,
			t.cfg.FeeRatePct,
		)

	switch intent.Side {
	case SideBuy:
		if t.latchedGateBuy == 0 ||
			nextLatch < t.latchedGateBuy {

			t.latchedGateBuy =
				nextLatch
		}

	case SideSell:
		if t.latchedGateSell == 0 ||
			nextLatch > t.latchedGateSell {

			t.latchedGateSell =
				nextLatch
		}
	}

	if err :=
		t.saveStateNoLock(); err != nil {

		/*
			Roll back only the local state mutated by produceEntry().
		*/
		t.latchedGateBuy =
			oldBuyLatch

		t.latchedGateSell =
			oldSellLatch

		t.mu.Lock()

		current, exists :=
			t.pendingEntries[orderID]

		if exists &&
			current == entry {

			delete(
				t.pendingEntries,
				orderID,
			)
		}

		t.mu.Unlock()

		produceErr :=
			&EntryProduceError{
				Code: EntryProduceErrPersistState,

				Producer: intent.Producer,

				Side: fmt.Sprint(
					intent.Side,
				),

				OrderID: orderID,

				CleanupRequired: true,

				Err: err,
			}

		addFailureEvent(
			produceErr,
		)

		return nil, produceErr
	}

	/*
		The entry has now been successfully produced and registered
		as a pending asynchronous entry.

		This pending event belongs to the SAME DecisionID and
		ProducerAttempt created at Decision stage.
	*/
	pendingEvent :=
		ProducerEvent{
			Time: time.Now().UTC(),

			CreatedAt: intent.CreatedAt,

			Producer: intent.Producer,

			Side: fmt.Sprint(
				intent.Side,
			),

			Stage: ProducerStagePending,

			DecisionID: intent.DecisionID,

			OrderID: entry.OrderID,

			Reason: intent.ProducerReason,
		}

	attempt.Events[ProducerStagePending] = pendingEvent

	log.Printf(
		"[PRODUCER] stage=pending "+
			"producer=%s side=%s order_id=%s reason=%q",
		entry.Producer,
		entry.Side,
		entry.OrderID,
		entry.ProducerReason,
	)

	t.startEntryPoller(
		ctx,
		entry,
	)

	return entry, nil
}

// Entry Producer Main Helpers
func (t *Trader) validatePendingIntent(
	intent *PendingIntent,
) *EntryProduceError {
	if t == nil {
		return &EntryProduceError{
			Code: EntryProduceErrNilTrader,
		}
	}

	if intent == nil {
		return &EntryProduceError{
			Code: EntryProduceErrNilPendingIntent,
		}
	}

	switch intent.Side {
	case SideBuy, SideSell:
		// valid

	default:
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidSide,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"Side=%q",
				intent.Side,
			),
		}
	}

	if strings.TrimSpace(intent.ProductID) == "" {
		return &EntryProduceError{
			Code:     EntryProduceErrMissingProductID,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
		}
	}

	if intent.LimitPx <= 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidPrice,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"LimitPx=%.8f",
				intent.LimitPx,
			),
		}
	}

	if intent.BaseAtLimit <= 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidQuantity,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"BaseAtLimit=%.8f",
				intent.BaseAtLimit,
			),
		}
	}

	if intent.Quote < 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidQuote,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"Quote=%.8f",
				intent.Quote,
			),
		}
	}

	if intent.Take < 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidTake,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"Take=%.8f",
				intent.Take,
			),
		}
	}

	if intent.RefundPortionUSD < 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidRefundPortion,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"RefundPortionUSD=%.8f",
				intent.RefundPortionUSD,
			),
		}
	}

	if intent.ConfidenceMult < 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidConfidenceMult,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"ConfidenceMult=%.8f",
				intent.ConfidenceMult,
			),
		}
	}

	if intent.ProfitGateUSD < 0 {
		return &EntryProduceError{
			Code:     EntryProduceErrInvalidProfitGate,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
			Err: fmt.Errorf(
				"ProfitGateUSD=%.8f",
				intent.ProfitGateUSD,
			),
		}
	}

	if intent.Producer == EntryProducerNone {
		return &EntryProduceError{
			Code: EntryProduceErrMissingProducer,
			Side: fmt.Sprint(intent.Side),
		}
	}

	if strings.TrimSpace(intent.ProducerReason) == "" {
		return &EntryProduceError{
			Code:     EntryProduceErrMissingProducerReason,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
		}
	}

	if intent.PendingCancelPolicy == PendingSignalCancelUnspecified {
		return &EntryProduceError{
			Code:     EntryProduceErrMissingPendingCancelPolicy,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
		}
	}

	return nil
}

func (t *Trader) submitPendingIntent(
	ctx context.Context,
	intent *PendingIntent,
) (string, *EntryProduceError) {
	if t == nil {
		return "", &EntryProduceError{
			Code: EntryProduceErrSubmitNilTrader,
		}
	}

	if ctx == nil {
		return "", &EntryProduceError{
			Code: EntryProduceErrSubmitNilContext,
		}
	}

	if intent == nil {
		return "", &EntryProduceError{
			Code: EntryProduceErrSubmitNilPendingIntent,
		}
	}

	if t.broker == nil {
		return "", &EntryProduceError{
			Code:     EntryProduceErrSubmitNilBroker,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
		}
	}

	/*
		Use the values already established by the wrapper and validated
		by validatePendingIntent().

		This function does not:
		  - apply strategy rules;
		  - inspect the entry source;
		  - change price or size;
		  - register pending state;
		  - start a poller;
		  - persist Trader state.
	*/
	orderID, err := t.broker.PlaceLimitPostOnly(
		ctx,
		intent.ProductID,
		intent.Side,
		intent.LimitPx,
		intent.BaseAtLimit,
	)

	orderID = strings.TrimSpace(orderID)

	if err != nil {
		code :=
			EntryProduceErrPostOnlySubmit

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
			/*
				Binance matching-engine rejection:

				-2010 is broad, so the message is required to
				authoritatively identify insufficient balance.
			*/
			case binanceErr.BinanceCode == -2010 &&
				msg ==
					"Account has insufficient balance for requested action.":

				code =
					EntryProduceErrInsufficientBalance

			/*
				LIMIT_MAKER would immediately execute.

				This is Binance's authoritative post-only rejection.
			*/
			case (binanceErr.BinanceCode == -2010 ||
				binanceErr.BinanceCode == -1010) &&
				msg ==
					"Order would immediately match and take.":

				code =
					EntryProduceErrPostOnlyRejected

			/*
				Binance timeout.

				Execution status may be unknown, so this remains
				a submission failure requiring higher-level handling.
			*/
			case binanceErr.BinanceCode == -1007:

				code =
					EntryProduceErrSubmitTimeout

			/*
				Too many requests / rate limiting.
			*/
			case binanceErr.BinanceCode == -1003:

				code =
					EntryProduceErrRateLimited

			/*
				Any other structured Binance rejection is still
				authoritatively an exchange rejection, even though its
				more specific business meaning is not classified here.
			*/
			default:
				code =
					EntryProduceErrExchangeRejected
			}
		}

		return orderID, &EntryProduceError{
			Code:            code,
			Producer:        intent.Producer,
			Side:            fmt.Sprint(intent.Side),
			OrderID:         orderID,
			CleanupRequired: orderID != "",
			Err:             err,
		}
	}

	if orderID == "" {
		return "", &EntryProduceError{
			Code:     EntryProduceErrMissingSubmittedOrderID,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
		}
	}

	return orderID, nil
}

func (t *Trader) buildPendingEntry(
	intent *PendingIntent,
	orderID string,
) (*PendingEntry, *EntryProduceError) {
	if t == nil {
		return nil, &EntryProduceError{
			Code: EntryProduceErrBuildNilTrader,
		}
	}

	if intent == nil {
		return nil, &EntryProduceError{
			Code: EntryProduceErrBuildNilPendingIntent,
		}
	}

	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, &EntryProduceError{
			Code:     EntryProduceErrBuildMissingOrderID,
			Producer: intent.Producer,
			Side:     fmt.Sprint(intent.Side),
		}
	}

	now := time.Now().UTC()

	intent.OrderID = orderID

	/*
	   CreatedAt belongs to the producer decision and was created once
	   by the source wrapper.

	   buildPendingEntry() must never regenerate or overwrite it.

	   The local time here is used only to establish the pending-order
	   deadline.
	*/
	intent.Deadline = now.Add(
		time.Duration(t.cfg.LimitTimeoutSec) * time.Second,
	)

	if intent.History == nil {
		intent.History = make([]string, 0, 5)
	}

	entry := &PendingEntry{
		Side:                intent.Side,
		Producer:            intent.Producer,
		ProducerReason:      intent.ProducerReason,
		PendingCancelPolicy: intent.PendingCancelPolicy,
		OrderID:             intent.OrderID,
		Intent:              intent,

		ResultC: make(chan OpenResult, 1),

		Completed: false,
	}

	log.Printf(
		"[PRODUCER] stage=pending "+
			"producer=%s side=%s order_id=%s reason=%q",
		entry.Producer,
		entry.Side,
		entry.OrderID,
		entry.ProducerReason,
	)

	if intent.Producer == EntryProducerCase3AReplacement {
		entry.CommitEligible = t.Case3ACommitEligible
	}

	switch intent.Side {
	case SideBuy:
		entry.Book = t.book(SideBuy)
		entry.SpareUSD = &t.SpareBuyUSD
		entry.LastAdd = &t.lastAddBuy
		entry.WinExtreme = &t.winLowBuy
		entry.LatchedGate = &t.latchedGateBuy
		entry.EquityTriggered = intent.EquityBuy

	case SideSell:
		entry.Book = t.book(SideSell)
		entry.SpareUSD = &t.SpareSellUSD
		entry.LastAdd = &t.lastAddSell
		entry.WinExtreme = &t.winHighSell
		entry.LatchedGate = &t.latchedGateSell
		entry.EquityTriggered = intent.EquitySell

	default:
		return nil, &EntryProduceError{
			Code:            EntryProduceErrBuildUnsupportedSide,
			Producer:        intent.Producer,
			Side:            fmt.Sprint(intent.Side),
			OrderID:         orderID,
			CleanupRequired: true,
			Err: fmt.Errorf(
				"Side=%v",
				intent.Side,
			),
		}
	}

	entry.clearOwner = func() {
		t.mu.Lock()
		defer t.mu.Unlock()

		current, ok := t.pendingEntries[entry.OrderID]
		if ok && current == entry {
			delete(t.pendingEntries, entry.OrderID)
		}
	}

	return entry, nil
}

func (t *Trader) registerPendingEntry(
	entry *PendingEntry,
) *EntryProduceError {
	if t == nil {
		return &EntryProduceError{
			Code: EntryProduceErrRegisterNilTrader,
		}
	}

	if entry == nil {
		return &EntryProduceError{
			Code: EntryProduceErrRegisterNilPendingEntry,
		}
	}

	if entry.Intent == nil {
		return &EntryProduceError{
			Code:     EntryProduceErrRegisterNilPendingIntent,
			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			OrderID:  strings.TrimSpace(entry.OrderID),
		}
	}

	orderID := strings.TrimSpace(entry.OrderID)
	if orderID == "" {
		return &EntryProduceError{
			Code:            EntryProduceErrRegisterMissingOrderID,
			Producer:        entry.Producer,
			Side:            fmt.Sprint(entry.Side),
			CleanupRequired: true,
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pendingEntries == nil {
		t.pendingEntries = make(map[string]*PendingEntry)
	}

	if _, exists := t.pendingEntries[orderID]; exists {
		return &EntryProduceError{
			Code:            EntryProduceErrRegisterDuplicateOrderID,
			Producer:        entry.Producer,
			Side:            fmt.Sprint(entry.Side),
			OrderID:         orderID,
			CleanupRequired: true,
		}
	}

	t.pendingEntries[orderID] = entry

	// log.Printf(
	// "[TRACE] pending.register "+
	// "producer=%s side=%s order_id=%s "+
	// "limit=%.8f base=%.8f quote=%.8f",
	// entry.Producer,
	// entry.Side,
	// orderID,
	// entry.Intent.LimitPx,
	// entry.Intent.BaseAtLimit,
	// entry.Intent.Quote,
	// )

	return nil
}

func (t *Trader) handleEntryProduceError(
	ctx context.Context,
	intent *PendingIntent,
	attempt *ProducerAttempt,
	err error,
) error {
	if err == nil {
		return nil
	}

	var produceErr *EntryProduceError

	if !errors.As(err, &produceErr) {
		log.Printf(
			"[ERROR] producer.entry_failed "+
				"producer=%s side=%s "+
				"code=unclassified err=%q",
			intent.Producer,
			intent.Side,
			err,
		)

		return err
	}

	/*
		stage=entry_failed has already been created by produceEntry()
		from this EntryProduceError and stored in:

		    attempt.Events[ProducerStageEntryFailed]

		Do not create or overwrite it here.
	*/
	log.Printf(
		"[PRODUCER] stage=entry_failed "+
			"producer=%s side=%s order_id=%s "+
			"code=%s cleanup_required=%t error=%q",
		produceErr.Producer,
		produceErr.Side,
		produceErr.OrderID,
		produceErr.Code,
		produceErr.CleanupRequired,
		produceErr.Err,
	)

	if !produceErr.CleanupRequired ||
		produceErr.OrderID == "" {

		return produceErr
	}

	cancelErr := t.broker.CancelOrder(
		ctx,
		intent.ProductID,
		produceErr.OrderID,
	)

	if cancelErr != nil {
		cleanupErr := &EntryProduceError{
			Code:            EntryProduceErrCleanupCancel,
			Producer:        produceErr.Producer,
			Side:            produceErr.Side,
			OrderID:         produceErr.OrderID,
			CleanupRequired: true,
			Err:             cancelErr,
		}

		if attempt != nil {
			if attempt.Events == nil {
				attempt.Events =
					make(map[ProducerStage]ProducerEvent)
			}

			attempt.Events[ProducerStageCleanupCancelFailed] =
				ProducerEvent{
					Time:      time.Now().UTC(),
					CreatedAt: attempt.CreatedAt,

					Producer: produceErr.Producer,
					Side:     produceErr.Side,
					Stage:    ProducerStageCleanupCancelFailed,

					DecisionID: attempt.DecisionID,
					OrderID:    produceErr.OrderID,

					Reason: intent.ProducerReason,

					ErrorCode:       cleanupErr.Code,
					Error:           cancelErr.Error(),
					CleanupRequired: true,
				}
		}

		log.Printf(
			"[PRODUCER] stage=cleanup_cancel_failed "+
				"producer=%s side=%s order_id=%s "+
				"original_code=%s code=%s err=%q",
			produceErr.Producer,
			produceErr.Side,
			produceErr.OrderID,
			produceErr.Code,
			cleanupErr.Code,
			cancelErr,
		)

		return cleanupErr
	}

	if attempt != nil {
		if attempt.Events == nil {
			attempt.Events =
				make(map[ProducerStage]ProducerEvent)
		}

		attempt.Events[ProducerStageCleanupCancelled] =
			ProducerEvent{
				Time:      time.Now().UTC(),
				CreatedAt: attempt.CreatedAt,

				Producer: produceErr.Producer,
				Side:     produceErr.Side,
				Stage:    ProducerStageCleanupCancelled,

				DecisionID: attempt.DecisionID,
				OrderID:    produceErr.OrderID,

				Reason: intent.ProducerReason,

				CleanupRequired: false,
			}
	}

	log.Printf(
		"[PRODUCER] stage=cleanup_cancelled "+
			"producer=%s side=%s order_id=%s "+
			"original_code=%s",
		produceErr.Producer,
		produceErr.Side,
		produceErr.OrderID,
		produceErr.Code,
	)

	return produceErr
}

func (t *Trader) startEntryPoller(
	parentCtx context.Context,
	entry *PendingEntry,
) {
	if parentCtx == nil {
		return
	}

	if entry == nil || entry.Intent == nil {
		return
	}

	/*
		The producer attempt already exists before polling begins.

		The poller receives the same PendingEntry and therefore retains
		the permanent producer correlation through:

		    entry.Intent.DecisionID
		    entry.Intent.CreatedAt
		    entry.Producer
		    entry.Side
		    entry.OrderID
		    entry.ProducerReason

		The poller must never create a new ProducerAttempt or DecisionID.

		Any producer lifecycle facts discovered asynchronously, including
		granular failure classifications discovered by the poller, are
		returned through:

		OpenResult.ProducerEvents["ProducerStage"]

		Failure details are carried inside ProducerEvent through:

			ErrorCode
			Error
			CleanupRequired
	*/
	pollCtx, cancel := context.WithCancel(parentCtx)
	entry.Cancel = cancel

	if entry.ResultC == nil {
		entry.ResultC = make(chan OpenResult, 1)
	}

	go t.runPendingEntryPoller(
		pollCtx,
		entry,
		entry.ResultC,
		entry.OrderID,
		entry.Side,
		entry.Intent.Deadline,
		entry.Intent.LimitPx,
		entry.Intent.BaseAtLimit,
		t.cfg.LimitPriceOffsetBps,
	)
}

func (t *Trader) runPendingEntryPoller(
	pollCtx context.Context,
	entry *PendingEntry,
	resultC chan OpenResult,
	initialOrderID string,
	side OrderSide,
	deadline time.Time,
	initialLimitPx float64,
	initialBaseAtLimit float64,
	offsetBps float64,
) {
	// log.Printf(
	// "[TRACE] postonly.poll.start "+
	// "producer=%s side=%s init_id=%s "+
	// "init_limit=%.8f init_base=%.8f "+
	// "deadline=%s offset_bps=%.3f",
	// entry.Producer,
	// side,
	// initialOrderID,
	// initialLimitPx,
	// initialBaseAtLimit,
	// deadline.Format(time.RFC3339),
	// offsetBps,
	// )

	// defer log.Printf(
	// "[TRACE] postonly.poll.stopped "+
	// "producer=%s side=%s initial_id=%s",
	// entry.Producer,
	// side,
	// initialOrderID,
	// )

	orderID := initialOrderID
	lastLimitPx := initialLimitPx
	lastReprice := time.Now()

	var sessionBase float64
	var sessionQuote float64
	var sessionFee float64

	var lastSeenBase float64
	var lastSeenQuote float64
	var lastSeenFee float64

	var repriceCount int

	/*
		The poller may discover multiple producer lifecycle events before
		it produces one terminal OpenResult.

		The wrapper-created correlation remains authoritative:

		    entry.Intent.DecisionID
		    entry.Intent.CreatedAt
		    entry.Producer
		    entry.Side

		The poller must never:
		  - create a ProducerAttempt;
		  - create or change DecisionID;
		  - mutate producerHistory;
		  - persist producerHistory.

		Instead, every lifecycle fact discovered here is transported in:

		    OpenResult.ProducerEvents[ProducerStage]

		The drain later merges these events into the already-existing
		ProducerAttempt.
	*/
	producerEvents := make(
		map[ProducerStage]ProducerEvent,
	)

	/*
		addProducerEvent converts a lifecycle fact, and when applicable
		an already-authoritative EntryProduceError, into the ProducerEvent
		transported by OpenResult.

		By default, the first event discovered for a stage is retained.

		replace=true is used only when the same lifecycle stage is
		intentionally updated, such as stage=pending after repricing.
	*/
	addProducerEvent := func(
		stage ProducerStage,
		eventOrderID string,
		produceErr *EntryProduceError,
		replace bool,
	) {
		if entry == nil || entry.Intent == nil {
			return
		}

		decisionID := strings.TrimSpace(
			entry.Intent.DecisionID,
		)
		if decisionID == "" {
			/*
				DecisionID belongs to the source wrapper.

				The poller must never manufacture producer correlation.
			*/
			return
		}

		if _, exists := producerEvents[stage]; exists &&
			!replace {

			return
		}

		event := ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: entry.Intent.CreatedAt,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			Stage:    stage,

			DecisionID: decisionID,
			OrderID:    eventOrderID,

			Reason: entry.ProducerReason,
		}

		/*
			Failure classification is discovered at the exact failure
			point and arrives here as EntryProduceError.

			No error classification occurs inside addProducerEvent().
		*/
		if produceErr != nil {
			event.ErrorCode = produceErr.Code
			event.CleanupRequired =
				produceErr.CleanupRequired

			if produceErr.Err != nil {
				event.Error =
					produceErr.Err.Error()
			}
		}

		producerEvents[stage] = event
	}

poll:
	for time.Now().Before(deadline) {
		select {
		case <-pollCtx.Done():
			// log.Printf(
			// "[TRACE] postonly.poll.cancelled "+
			// "producer=%s side=%s last_id=%s",
			// entry.Producer,
			// side,
			// orderID,
			// )

			break poll

		default:
		}

		ord, getErr := t.broker.GetOrder(
			pollCtx,
			entry.Intent.ProductID,
			orderID,
		)

		if getErr != nil {
			/*
				GetOrder failed during asynchronous entry polling.

				This exact failure is discovered here.

				Existing trading behavior is preserved:
				the poller does not terminate because of this error and
				may successfully retrieve the order on a later poll.
			*/
			produceErr := &EntryProduceError{
				Code: EntryProduceErrPollerGetOrderFailed,

				Producer: entry.Producer,
				Side:     fmt.Sprint(entry.Side),
				OrderID:  orderID,

				CleanupRequired: false,

				// Variable/runtime diagnostic detail.
				Err: getErr,
			}

			addProducerEvent(
				ProducerStagePollerGetOrderFailed,
				orderID,
				produceErr,
				false,
			)
		}

		if getErr == nil && ord != nil {
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

			sessionBase += dBase
			sessionQuote += dQuote
			sessionFee += dFee

			lastSeenBase = ord.BaseSize
			lastSeenQuote = ord.QuoteSpent
			lastSeenFee = ord.CommissionUSD

			entry.Intent.AccumBase = sessionBase
			entry.Intent.AccumQuote = sessionQuote
			entry.Intent.AccumFeeUSD = sessionFee

			status := strings.ToUpper(
				strings.TrimSpace(ord.Status),
			)

			// log.Printf(
			// "[TRACE] postonly.poll.tick "+
			// "producer=%s side=%s order_id=%s status=%s "+
			// "price=%.8f base=%.8f quote=%.2f fee=%.6f "+
			// "sess_agg[base=%.8f quote=%.2f fee=%.6f] "+
			// "reprices=%d",
			// entry.Producer,
			// side,
			// orderID,
			// status,
			// ord.Price,
			// ord.BaseSize,
			// ord.QuoteSpent,
			// ord.CommissionUSD,
			// sessionBase,
			// sessionQuote,
			// sessionFee,
			// repriceCount,
			// )

			switch status {

			case "FILLED":
				placed := placedOrderFromAggregate(
					sessionBase,
					sessionQuote,
					sessionFee,
				)

				/*
					The exchange has authoritatively reported FILLED.

					This is a lifecycle fact, not a failure.
				*/
				addProducerEvent(
					ProducerStageFilled,
					orderID,
					nil,
					false,
				)

				// log.Printf(
				// "[TRACE] postonly.filled "+
				// "producer=%s order_id=%s "+
				// "price=%.8f baseFilled=%.8f "+
				// "quoteSpent=%.2f fee=%.4f",
				// entry.Producer,
				// orderID,
				// ord.Price,
				// ord.BaseSize,
				// ord.QuoteSpent,
				// ord.CommissionUSD,
				// )

				// log.Printf(
				// "[KPI] maker.open.filled "+
				// "producer=%s side=%s vwap=%.8f "+
				// "base=%.8f quote=%.2f fee=%.6f "+
				// "order_id=%s",
				// entry.Producer,
				// side,
				// placed.Price,
				// placed.BaseSize,
				// placed.QuoteSpent,
				// placed.CommissionUSD,
				// orderID,
				// )

				safeSend(
					resultC,
					OpenResult{
						Filled:  true,
						Placed:  placed,
						OrderID: orderID,

						ProducerEvents: producerEvents,
					},
				)

				return

			case "PARTIALLY_FILLED",
				"NEW",
				"PENDING_CANCEL":

				if t.pendingEntryCancelRequested(entry) {
					/*
						The existing trading logic has already requested
						cancellation.

						Record only the lifecycle fact. Do not issue another
						cancel or alter existing cancellation behavior.
					*/
					addProducerEvent(
						ProducerStageCancelRequested,
						orderID,
						nil,
						false,
					)

					// log.Printf(
					// "[TRACE] postonly.reprice.skip.cancel_requested "+
					// "producer=%s side=%s order_id=%s "+
					// "last_status=%s",
					// entry.Producer,
					// side,
					// orderID,
					// status,
					// )

					lastReprice = time.Now()
					break
				}

				repriceAfter := time.Duration(
					t.cfg.RepriceIntervalMs,
				) * time.Millisecond

				if time.Since(lastReprice) <
					repriceAfter {

					break
				}

				// log.Printf(
				// "[TRACE] postonly.reprice.try "+
				// "producer=%s side=%s order_id=%s "+
				// "status=%s last_limit=%.8f "+
				// "reprice_count=%d",
				// entry.Producer,
				// side,
				// orderID,
				// status,
				// lastLimitPx,
				// repriceCount,
				// )

				newID,
					newLastLimitPx,
					newRepriceCount,
					didReprice := t.maybeRepriceOnce(
					pollCtx,
					entry,
					orderID,
					initialLimitPx,
					initialBaseAtLimit,
					lastLimitPx,
					offsetBps,
					repriceCount,
				)

				if didReprice &&
					newID != orderID {

					// log.Printf(
					// "[TRACE] postonly.reprice.swap "+
					// "producer=%s side=%s old_id=%s "+
					// "new_id=%s new_limit=%.8f count=%d",
					// entry.Producer,
					// side,
					// orderID,
					// newID,
					// newLastLimitPx,
					// newRepriceCount,
					// )

					oldID := orderID

					orderID = newID
					lastLimitPx = newLastLimitPx
					repriceCount = newRepriceCount

					lastSeenBase = 0
					lastSeenQuote = 0
					lastSeenFee = 0

					/*
						Repricing is still the same producer decision.

						Update the transported pending stage so the drain can
						update the existing ProducerAttempt's pending event
						with the currently-live exchange OrderID.

						No new ProducerAttempt or DecisionID is created.
					*/
					addProducerEvent(
						ProducerStagePending,
						newID,
						nil,
						true,
					)

					log.Printf(
						"[PRODUCER] stage=pending "+
							"producer=%s side=%s "+
							"order_id=%s reason=%q "+
							"repriced=%t",
						entry.Producer,
						entry.Side,
						newID,
						entry.ProducerReason,
						true,
					)

					t.rekeyPendingEntry(
						entry,
						oldID,
						newID,
					)
				} else {
					// log.Printf(
					// "[TRACE] postonly.reprice.skip "+
					// "producer=%s side=%s order_id=%s "+
					// "reason=no_guard_or_no_improve "+
					// "last_limit=%.8f count=%d",
					// entry.Producer,
					// side,
					// orderID,
					// newLastLimitPx,
					// newRepriceCount,
					// )

					lastLimitPx = newLastLimitPx
					repriceCount = newRepriceCount
				}

				lastReprice = time.Now()

			case "CANCELED",
				"REJECTED",
				"EXPIRED":

				switch status {

				case "CANCELED":
					/*
						CANCELED is not automatically a failure.

						If the existing trading path requested cancellation,
						the exchange has now confirmed successful completion
						of that cancellation.

						Ensure both lifecycle facts are represented even if
						the exchange moved to CANCELED before the poller
						previously observed PENDING_CANCEL.
					*/
					if t.pendingEntryCancelRequested(
						entry,
					) {
						addProducerEvent(
							ProducerStageCancelRequested,
							orderID,
							nil,
							false,
						)

						addProducerEvent(
							ProducerStageCleanupCancelled,
							orderID,
							nil,
							false,
						)
					}

				case "REJECTED":
					/*
						The exchange has authoritatively rejected the
						pending entry order.

						The enum itself carries the stable human/machine
						classification. No variable Err detail is required
						here.
					*/
					produceErr := &EntryProduceError{
						Code: EntryProduceErrPollerRejected,

						Producer: entry.Producer,
						Side:     fmt.Sprint(entry.Side),
						OrderID:  orderID,

						CleanupRequired: false,
					}

					addProducerEvent(
						ProducerStageRejected,
						orderID,
						produceErr,
						false,
					)

				case "EXPIRED":
					/*
						The exchange has authoritatively expired the
						pending entry order.

						The enum itself carries the stable classification.
						No variable Err detail is required here.
					*/
					produceErr := &EntryProduceError{
						Code: EntryProduceErrPollerExpired,

						Producer: entry.Producer,
						Side:     fmt.Sprint(entry.Side),
						OrderID:  orderID,

						CleanupRequired: false,
					}

					addProducerEvent(
						ProducerStageExpired,
						orderID,
						produceErr,
						false,
					)
				}

				if sessionBase > 0 ||
					sessionQuote > 0 {

					placed := placedOrderFromAggregate(
						sessionBase,
						sessionQuote,
						sessionFee,
					)

					/*
						The terminal exchange status was not FILLED, but
						actual execution accumulated.

						The existing trading behavior already reports this
						as Filled=true, so observability records the same
						factual filled lifecycle stage.
					*/
					addProducerEvent(
						ProducerStageFilled,
						orderID,
						nil,
						false,
					)

					// log.Printf(
					// "[KPI] maker.open.filled "+
					// "producer=%s side=%s vwap=%.8f "+
					// "base=%.8f quote=%.2f fee=%.6f "+
					// "order_id=%s status=%s",
					// entry.Producer,
					// side,
					// placed.Price,
					// placed.BaseSize,
					// placed.QuoteSpent,
					// placed.CommissionUSD,
					// orderID,
					// status,
					// )

					safeSend(
						resultC,
						OpenResult{
							Filled:  true,
							Placed:  placed,
							OrderID: orderID,

							ProducerEvents: producerEvents,
						},
					)
				} else {
					safeSend(
						resultC,
						OpenResult{
							Filled:  false,
							Placed:  nil,
							OrderID: orderID,

							ProducerEvents: producerEvents,
						},
					)
				}

				// log.Printf(
				// "[TRACE] postonly.poll.done "+
				// "producer=%s side=%s order_id=%s "+
				// "final=%s vwap=%.8f base=%.8f "+
				// "quote=%.2f fee=%.6f",
				// entry.Producer,
				// side,
				// orderID,
				// status,
				// vwapFromAggregate(
				// sessionBase,
				// sessionQuote,
				// ),
				// sessionBase,
				// sessionQuote,
				// sessionFee,
				// )

				return
			}
		}

		select {
		case <-pollCtx.Done():
			// log.Printf(
			// "[TRACE] postonly.poll.cancelled "+
			// "producer=%s side=%s last_id=%s",
			// entry.Producer,
			// side,
			// orderID,
			// )

			break poll

		case <-time.After(
			200 * time.Millisecond,
		):
		}
	}

	/*
		Use a non-cancelled context for the final exchange cancel.

		The polling context may already have been cancelled.
	*/
	cancelCtx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)

	/*
		The existing poller has reached its final cancellation path.

		Discover cancel_requested before executing the existing CancelOrder.

		This is observability only; it does not change cancellation behavior.
	*/
	addProducerEvent(
		ProducerStageCancelRequested,
		orderID,
		nil,
		false,
	)

	cancelErr := t.broker.CancelOrder(
		cancelCtx,
		entry.Intent.ProductID,
		orderID,
	)

	cancel()

	if cancelErr != nil {
		/*
			The poller's own final CancelOrder failed.

			This failure is distinct from EntryProduceErrCleanupCancel,
			which was discovered in handleEntryProduceError() during the
			earlier entry-production cleanup path.

			Construct the poller's exact failure here.
		*/
		produceErr := &EntryProduceError{
			Code: EntryProduceErrPollerCancelFailed,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			OrderID:  orderID,

			CleanupRequired: true,

			// Variable/runtime diagnostic detail.
			Err: cancelErr,
		}

		addProducerEvent(
			ProducerStageCleanupCancelFailed,
			orderID,
			produceErr,
			false,
		)
	} else {
		/*
			The existing final CancelOrder succeeded.

			Any execution that accumulated before cancellation remains
			a separate lifecycle fact handled below.
		*/
		addProducerEvent(
			ProducerStageCleanupCancelled,
			orderID,
			nil,
			false,
		)
	}

	// log.Printf(
	// "[TRACE] postonly.poll.timeout "+
	// "producer=%s side=%s last_id=%s "+
	// "sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
	// entry.Producer,
	// side,
	// orderID,
	// sessionBase,
	// sessionQuote,
	// sessionFee,
	// )

	if sessionBase > 0 ||
		sessionQuote > 0 {

		placed := placedOrderFromAggregate(
			sessionBase,
			sessionQuote,
			sessionFee,
		)

		/*
			Execution accumulated before the final cancellation path.

			Existing trading behavior reports Filled=true, therefore
			observability discovers stage=filled as part of the same
			OpenResult.
		*/
		addProducerEvent(
			ProducerStageFilled,
			orderID,
			nil,
			false,
		)

		safeSend(
			resultC,
			OpenResult{
				Filled:  true,
				Placed:  placed,
				OrderID: orderID,

				ProducerEvents: producerEvents,
			},
		)

		return
	}

	safeSend(
		resultC,
		OpenResult{
			Filled:  false,
			Placed:  nil,
			OrderID: orderID,

			ProducerEvents: producerEvents,
		},
	)
}

func vwapFromAggregate(
	base float64,
	quote float64,
) float64 {
	if base <= 0 {
		return 0
	}

	return quote / base
}
func placedOrderFromAggregate(
	base float64,
	quote float64,
	feeUSD float64,
) *PlacedOrder {
	return &PlacedOrder{
		Price:         vwapFromAggregate(base, quote),
		BaseSize:      base,
		QuoteSpent:    quote,
		CommissionUSD: feeUSD,
	}
}
func (t *Trader) rekeyPendingEntry(
	entry *PendingEntry,
	oldOrderID string,
	newOrderID string,
) {
	oldOrderID = strings.TrimSpace(oldOrderID)
	newOrderID = strings.TrimSpace(newOrderID)

	if entry == nil || entry.Intent == nil || newOrderID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	current, exists := t.pendingEntries[oldOrderID]
	if !exists || current != entry {
		log.Printf(
			"[WARN] pending.rekey.owner_mismatch "+
				"producer=%s side=%s old_id=%s new_id=%s",
			entry.Producer,
			entry.Side,
			oldOrderID,
			newOrderID,
		)

		return
	}

	if existing, collision := t.pendingEntries[newOrderID]; collision &&
		existing != entry {

		log.Printf(
			"[ERROR] pending.rekey.collision "+
				"producer=%s side=%s old_id=%s new_id=%s",
			entry.Producer,
			entry.Side,
			oldOrderID,
			newOrderID,
		)

		return
	}

	delete(t.pendingEntries, oldOrderID)

	if oldOrderID != "" {
		entry.Intent.History = appendOrderHistory(
			entry.Intent.History,
			oldOrderID,
			5,
		)
	}

	entry.OrderID = newOrderID
	entry.Intent.OrderID = newOrderID

	t.pendingEntries[newOrderID] = entry
}
func appendOrderHistory(
	history []string,
	orderID string,
	max int,
) []string {
	orderID = strings.TrimSpace(orderID)

	if orderID == "" {
		return history
	}

	for _, existing := range history {
		if existing == orderID {
			return history
		}
	}

	history = append(history, orderID)

	if max > 0 && len(history) > max {
		history = history[len(history)-max:]
	}

	return history
}
func (t *Trader) pendingEntryCancelRequested(
	entry *PendingEntry,
) bool {
	if entry == nil || entry.Intent == nil {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	return entry.Intent.CancelRequested
}
func (t *Trader) maybeRepriceOnce(
	pctx context.Context,
	entry *PendingEntry,
	orderID string,
	initLimitPx float64,
	initBaseAtLimit float64,
	lastLimitPx float64,
	offsetBps float64,
	repriceCount int,
) (
	newOrderID string,
	newLastLimitPx float64,
	newRepriceCount int,
	didReprice bool,
) {
	if entry == nil {
		return orderID, lastLimitPx, repriceCount, false
	}

	intent := entry.Intent
	if intent == nil {
		return orderID, lastLimitPx, repriceCount, false
	}

	side := entry.Side

	rpStart := time.Now()

	// Global guards
	if !t.cfg.RepriceEnable {
		return orderID, lastLimitPx, repriceCount, false
	}

	if t.cfg.RepriceMaxCount > 0 &&
		repriceCount >= t.cfg.RepriceMaxCount {
		return orderID, lastLimitPx, repriceCount, false
	}

	bid, ask, bErr := t.broker.GetBBO(
		pctx,
		t.cfg.ProductID,
	)

	useBBO := bErr == nil &&
		bid > 0 &&
		ask > bid

	var newLimitPx float64

	if useBBO {

		if side == SideBuy {
			newLimitPx = bid
		} else {
			newLimitPx = ask
		}

	} else {

		ctxPx, cancelPx := context.WithTimeout(
			pctx,
			time.Second,
		)

		px, gErr := t.broker.GetNowPrice(
			ctxPx,
			t.cfg.ProductID,
		)

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

	tick := t.cfg.PriceTick

	if tick > 0 {

		if side == SideBuy {
			newLimitPx =
				math.Floor(newLimitPx/tick) * tick
		} else {
			newLimitPx =
				math.Ceil(newLimitPx/tick) * tick
		}
	}

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

		if side == SideBuy &&
			newLimitPx >= ask {

			newLimitPx =
				math.Nextafter(ask, 0)
		}

		if side == SideSell &&
			newLimitPx <= bid {

			newLimitPx =
				math.Nextafter(bid, +1)
		}
	}

	shouldReprice :=
		(tick > 0 &&
			math.Abs(newLimitPx-lastLimitPx) >= tick) ||
			(tick <= 0 &&
				newLimitPx != lastLimitPx)

	if shouldReprice &&
		t.cfg.RepriceMaxDriftBps > 0 {

		drift :=
			math.Abs(
				(newLimitPx-initLimitPx)/
					initLimitPx,
			) * 10000.0

		if drift > t.cfg.RepriceMaxDriftBps {
			shouldReprice = false
		}
	}

	newBase := initBaseAtLimit

	if intent.Quote > 0 {
		newBase = intent.Quote / newLimitPx
	}

	if t.cfg.BaseStep > 0 {
		newBase =
			math.Floor(newBase/t.cfg.BaseStep) *
				t.cfg.BaseStep
	}

	if shouldReprice &&
		!(newBase > 0 &&
			newBase*newLimitPx >= t.cfg.MinNotional) {

		shouldReprice = false
	}

	driftBps := 0.0

	if initLimitPx > 0 {
		driftBps =
			math.Abs(
				(newLimitPx-initLimitPx)/
					initLimitPx,
			) * 10000.0
	}

	improveTicks := 0.0

	if tick > 0 {
		improveTicks =
			math.Abs(newLimitPx-lastLimitPx) /
				tick
	}

	notional := newBase * newLimitPx

	notionalOK :=
		newBase > 0 &&
			notional >= t.cfg.MinNotional

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
		useBBO,
		bid,
		ask,
		initLimitPx,
		lastLimitPx,
		newLimitPx,
		tick,
		improveTicks,
		driftBps,
		t.cfg.RepriceMaxDriftBps,
		newBase,
		notional,
		t.cfg.MinNotional,
		notionalOK,
		shouldReprice,
		repriceCount,
		t.cfg.RepriceMaxCount,
	)

	if !shouldReprice {
		return orderID, lastLimitPx, repriceCount, false
	}

	if useBBO {
		// log.Printf(
		// "[TRACE] postonly.reprice.touch side=%s bid=%.8f ask=%.8f new=%.8f last=%.8f",
		// side,
		// bid,
		// ask,
		// newLimitPx,
		// lastLimitPx,
		// )
	} else {
		// log.Printf(
		// "[TRACE] postonly.reprice.mark side=%s new=%.8f last=%.8f",
		// side,
		// newLimitPx,
		// lastLimitPx,
		// )
	}

	_ = t.broker.CancelOrder(
		pctx,
		t.cfg.ProductID,
		orderID,
	)

	newID, perr :=
		t.broker.PlaceLimitPostOnly(
			pctx,
			t.cfg.ProductID,
			side,
			newLimitPx,
			newBase,
		)

	if perr != nil ||
		strings.TrimSpace(newID) == "" {

		return orderID,
			lastLimitPx,
			repriceCount,
			false
	}

	// log.Printf(
	// "[TRACE] postonly.reprice side=%s old_id=%s new_id=%s limit=%.8f baseReq=%.8f",
	// side,
	// orderID,
	// newID,
	// newLimitPx,
	// newBase,
	// )

	// The poller owns the registry rekey after this function returns.
	// Update only the repriced economic values here.
	intent.LimitPx = newLimitPx
	intent.BaseAtLimit = newBase

	return newID,
		newLimitPx,
		repriceCount + 1,
		true
}

// Entry Drain result
// Entry Drain result
func (t *Trader) drainPendingEntry(
	entry *PendingEntry,
	now time.Time,
	wallNow time.Time,
) {
	if entry == nil ||
		entry.Completed ||
		entry.ResultC == nil {

		return
	}

	if entry.CommitEligible != nil &&
		!entry.CommitEligible(entry) {

		return
	}

	side := entry.Side
	pending := entry.Intent
	book := entry.Book

	/*
		Add one lifecycle event discovered locally by the drain.

		Poller-discovered events already arrive as ProducerEvent through:

		    OpenResult.ProducerEvents

		Drain-local discoveries therefore use the same representation
		directly.

		No EntryProduceError intermediary is required for failures whose
		classification is owned by the drain itself.

		The returned map must always be assigned by the caller because the
		incoming map may be nil and therefore allocated here.
	*/
	addDrainProducerEvent := func(
		events map[ProducerStage]ProducerEvent,
		stage ProducerStage,
		orderID string,
		errorCode EntryProduceErrorCode,
		errorText string,
		cleanupRequired bool,
		replace bool,
	) map[ProducerStage]ProducerEvent {

		if pending == nil {
			return events
		}

		decisionID := strings.TrimSpace(
			pending.DecisionID,
		)
		if decisionID == "" {
			return events
		}

		if events == nil {
			events =
				make(map[ProducerStage]ProducerEvent)
		}

		if _, exists := events[stage]; exists && !replace {

			return events
		}

		events[stage] = ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: pending.CreatedAt,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			Stage:    stage,

			DecisionID: decisionID,
			OrderID:    orderID,

			Reason: pending.ProducerReason,

			ErrorCode:       errorCode,
			Error:           errorText,
			CleanupRequired: cleanupRequired,
		}

		return events
	}

	/*
		Convert one post-poller lifecycle event set into a ProducerAttempt
		fragment and pass it through the canonical producer-history mutation
		helper.

		The original ProducerAttempt was registered by the higher-level
		producer caller before polling began.

		This fragment carries the same permanent identity:

		    DecisionID
		    CreatedAt
		    Producer
		    Side

		recordProducerAttemptLocked() therefore performs:

		    Behavior 1 — register
		        only if the attempt is unexpectedly absent;

		    Behavior 2 — enrich
		        when the DecisionID already exists.

		No DecisionID is created or regenerated here.
	*/
	recordDrainProducerEvents := func(
		events map[ProducerStage]ProducerEvent,
	) {
		if pending == nil ||
			len(events) == 0 {

			return
		}

		decisionID := strings.TrimSpace(
			pending.DecisionID,
		)
		if decisionID == "" {
			return
		}

		attempt := &ProducerAttempt{
			DecisionID: decisionID,
			CreatedAt:  pending.CreatedAt,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),

			Events: events,
		}

		t.recordProducerAttemptLocked(
			attempt,
		)
	}

	/*
		Persist producer-history independently from trader state.

		Producer-history persistence failure remains observability-only
		and must never alter trading behavior.
	*/
	saveProducerHistory := func() {
		if err := t.saveProducerHistoryNoLock(); err != nil {
			log.Printf(
				"[ERROR] producer.history.save_failed "+
					"producer=%s decision_id=%s err=%v",
				entry.Producer,
				func() string {
					if pending == nil {
						return ""
					}

					return pending.DecisionID
				}(),
				err,
			)
		}
	}

	/*
		Producer correlation must still be available here because this
		drain owns the asynchronous producer lifecycle.

		If Producer itself is missing, there is no valid producer-history
		owner under which an attempt fragment can be recorded.

		Do not manufacture producer ownership.
	*/
	if entry.Producer == EntryProducerNone {
		log.Printf(
			"[ERROR] postonly.drain.missing_producer "+
				"side=%s order_id=%s",
			side,
			entry.OrderID,
		)

		return
	}

	/*
		The PendingEntry producer and PendingIntent producer must represent
		the same producer decision.

		This failure is discovered locally by the drain, therefore construct
		ProducerEvent directly and enrich the existing attempt through
		recordProducerAttemptLocked().
	*/
	if pending != nil &&
		entry.Producer != pending.Producer {

		events := make(
			map[ProducerStage]ProducerEvent,
		)

		errorText := fmt.Sprintf(
			"entry producer=%s intent producer=%s",
			entry.Producer,
			pending.Producer,
		)

		events =
			addDrainProducerEvent(
				events,
				ProducerStageDrainProducerMismatch,
				entry.OrderID,
				EntryProduceErrDrainProducerMismatch,
				errorText,
				false,
				false,
			)

		recordDrainProducerEvents(
			events,
		)

		saveProducerHistory()

		log.Printf(
			"[ERROR] postonly.drain.producer_mismatch "+
				"side=%s order_id=%s "+
				"entry_producer=%s intent_producer=%s",
			side,
			entry.OrderID,
			entry.Producer,
			pending.Producer,
		)

		return
	}

	/*
		Finish the pending lifecycle after a terminal broker result.

		The owner-specific cleanup is supplied when PendingEntry is
		constructed, so this generic drain remains producer-agnostic.

		The current lifecycle event map is supplied so any state persistence
		failure discovered during finish() is added to the same asynchronous
		lifecycle and enriched into the same ProducerAttempt.
	*/
	finish := func(
		events map[ProducerStage]ProducerEvent,
	) {
		if entry.Cancel != nil {
			entry.Cancel()
		}

		if entry.clearOwner != nil {
			entry.clearOwner()
		}

		/*
			The local `pending` variable still retains the original
			PendingIntent correlation after entry.Intent is cleared.
		*/
		entry.Intent = nil
		entry.ResultC = nil
		entry.Cancel = nil
		entry.Completed = true

		if err := t.saveStateNoLock(); err != nil {

			/*
				This persistence failure is discovered by the drain.

				Construct its ProducerEvent directly rather than routing it
				through EntryProduceError.
			*/
			events =
				addDrainProducerEvent(
					events,
					ProducerStageDrainPersistStateFailed,
					entry.OrderID,
					EntryProduceErrDrainPersistStateFailed,
					err.Error(),
					false,
					false,
				)

			recordDrainProducerEvents(
				events,
			)

			saveProducerHistory()

			log.Printf(
				"[ERROR] saveState "+
					"(drain %s producer=%s id=%s): %v",
				side,
				entry.Producer,
				entry.OrderID,
				err,
			)
		}
	}

	select {
	case res, ok := <-entry.ResultC:

		/*
			OpenResult.ProducerEvents is the common asynchronous
			observability transport.

			The poller may already have populated this map.

			If the channel is closed, `res` is the zero OpenResult, so
			initialize the map before adding a drain-local event.
		*/
		if res.ProducerEvents == nil {
			res.ProducerEvents =
				make(map[ProducerStage]ProducerEvent)
		}

		if !ok {
			res.ProducerEvents =
				addDrainProducerEvent(
					res.ProducerEvents,
					ProducerStageDrainChannelClosed,
					entry.OrderID,
					EntryProduceErrDrainChannelClosed,
					"pending entry result channel closed",
					false,
					false,
				)

			recordDrainProducerEvents(
				res.ProducerEvents,
			)

			saveProducerHistory()

			log.Printf(
				"[ERROR] postonly.drain.channel_closed "+
					"side=%s producer=%s id=%s",
				side,
				entry.Producer,
				entry.OrderID,
			)

			// finish() may call entry.clearOwner(), which acquires t.mu.
			// step() already owns t.mu while draining entries.
			t.mu.Unlock()

			finish(
				res.ProducerEvents,
			)

			t.mu.Lock()

			return
		}

		/*
			The asynchronous poller has delivered its result.

			Every lifecycle event discovered by the poller is already a
			ProducerEvent with authoritative:

			    Stage
			    ErrorCode
			    Error
			    CleanupRequired
			    DecisionID
			    CreatedAt

			Construct a ProducerAttempt fragment and enrich the original
			attempt before any commit processing occurs.

			This ensures poller visibility survives even if commit later fails.
		*/
		recordDrainProducerEvents(
			res.ProducerEvents,
		)

		saveProducerHistory()

		// log.Printf(
		// "[TRACE] postonly.drain.recv side=%s producer=%s id=%s order_id=%s filled=%v placed_nil=%v",
		// side,
		// entry.Producer,
		// entry.OrderID,
		// res.OrderID,
		// res.Filled,
		// res.Placed == nil,
		// )

		/*
			Decide whether this asynchronous result is safe to apply.

			Repricing may create several exchange order IDs. Accept a fill
			when it matches:

			  1. the current pending order ID; or
			  2. an order ID recorded in PendingIntent.History.

			When pending state is missing but the broker reports a real fill,
			accept it rather than orphaning an exchange position.

			Trading behavior is unchanged.
		*/
		accept := false

		if res.Filled &&
			res.Placed != nil {

			// log.Printf(
			// "[TRACE] postonly.drain.placed side=%s producer=%s order_id=%s price=%.8f base=%.8f quote=%.2f fee=%.6f",
			// side,
			// entry.Producer,
			// res.OrderID,
			// res.Placed.Price,
			// res.Placed.BaseSize,
			// res.Placed.QuoteSpent,
			// res.Placed.CommissionUSD,
			// )

			if pending != nil {
				if res.OrderID ==
					pending.OrderID {

					accept = true
				} else {
					for _, historicalID := range pending.History {

						if res.OrderID ==
							historicalID {

							accept = true
							break
						}
					}
				}

				if !accept {
					/*
						A real fill was reported, but its OrderID does not
						match either the current pending order or its
						reprice history.

						This is a drain-local lifecycle failure.

						Construct ProducerEvent directly and add it to the same
						OpenResult event map.
					*/
					errorText := fmt.Sprintf(
						"filled order %q does not match current pending order %q or history",
						res.OrderID,
						pending.OrderID,
					)

					res.ProducerEvents =
						addDrainProducerEvent(
							res.ProducerEvents,
							ProducerStageFillOrderMismatch,
							res.OrderID,
							EntryProduceErrDrainFillOrderMismatch,
							errorText,
							false,
							false,
						)

					recordDrainProducerEvents(
						res.ProducerEvents,
					)

					saveProducerHistory()
				}
			} else {
				/*
					Existing behavior intentionally accepts a real exchange
					fill when pending state is unavailable rather than
					orphaning the exchange position.

					Do not alter that trading behavior.
				*/
				accept = true

				log.Printf(
					"[WARN] postonly.fill.without_pending "+
						"side=%s producer=%s order_id=%s",
					side,
					entry.Producer,
					res.OrderID,
				)
			}
		}

		if accept {
			if book == nil {
				/*
					A real exchange fill exists, but the drain cannot commit
					it because its target position book is absent.

					This is discovered locally by the drain.

					The PendingEntry deliberately remains incomplete so the
					existing reconciliation behavior is preserved.
				*/
				res.ProducerEvents =
					addDrainProducerEvent(
						res.ProducerEvents,
						ProducerStageCommitFailed,
						res.OrderID,
						EntryProduceErrDrainBookNil,
						"filled entry cannot be committed: position book is nil",
						false,
						true,
					)

				recordDrainProducerEvents(
					res.ProducerEvents,
				)

				saveProducerHistory()

				log.Printf(
					"[ERROR] postonly.fill.book_nil "+
						"side=%s producer=%s id=%s "+
						"order_id=%s reconciliation_required=true",
					side,
					entry.Producer,
					entry.OrderID,
					res.OrderID,
				)

				return
			}

			/*
				stage=filled was already discovered by the poller and
				delivered through res.ProducerEvents.

				Do not create another filled event in the drain.
			*/
			log.Printf(
				"[PRODUCER] stage=filled "+
					"producer=%s side=%s order_id=%s "+
					"price=%.8f base=%.8f quote=%.8f fee_usd=%.8f",
				entry.Producer,
				entry.Side,
				res.OrderID,
				res.Placed.Price,
				res.Placed.BaseSize,
				res.Placed.QuoteSpent,
				res.Placed.CommissionUSD,
			)

			/*
				commitEntryFill() owns classification of failures discovered
				inside the commit path.

				It may also append non-fatal ProducerEvents directly into
				res.ProducerEvents because OpenResult is passed by pointer.
			*/
			if err := t.commitEntryFill(
				entry,
				&res,
				now,
				wallNow,
			); err != nil {

				/*
					An EntryProduceError returned from commitEntryFill()
					already contains the authoritative commit failure code.

					The drain does not reclassify it. It merely represents that
					classification as the lifecycle event commit_failed.
				*/
				var produceErr *EntryProduceError

				if errors.As(
					err,
					&produceErr,
				) {
					errorText := ""

					if produceErr.Err != nil {
						errorText =
							produceErr.Err.Error()
					}

					res.ProducerEvents =
						addDrainProducerEvent(
							res.ProducerEvents,
							ProducerStageCommitFailed,
							res.OrderID,
							produceErr.Code,
							errorText,
							produceErr.CleanupRequired,
							true,
						)
				} else {
					/*
						Defensive fallback only.

						Known commit failure points should return
						*EntryProduceError.

						If a plain error escapes, retain it under the existing
						drain fallback classification.
					*/
					res.ProducerEvents =
						addDrainProducerEvent(
							res.ProducerEvents,
							ProducerStageCommitFailed,
							res.OrderID,
							EntryProduceErrDrainCommitFailed,
							err.Error(),
							false,
							true,
						)
				}

				/*
					commitEntryFill() may also have added non-fatal events,
					for example commit_spare_pointer_nil, before returning.

					The complete current map is now sent as one enrichment
					fragment.
				*/
				recordDrainProducerEvents(
					res.ProducerEvents,
				)

				saveProducerHistory()

				log.Printf(
					"[ERROR] postonly.commit "+
						"side=%s producer=%s order_id=%s: %v",
					side,
					entry.Producer,
					res.OrderID,
					err,
				)

				return
			}

			/*
				commitEntryFill() returned successfully.

				It may have added lifecycle events such as:

				    commit_spare_pointer_nil
				    refund_consumed

				They remain in this same OpenResult.ProducerEvents map.
			*/

			_, refundConsumed :=
				res.ProducerEvents[ProducerStageRefundConsumed]

			if refundConsumed {

				/*
					A fully-refunded fill was successfully processed but no
					Position was created.

					Its terminal lifecycle is:

					    filled -> refund_consumed

					and not:

					    filled -> committed

					refund_consumed was created by commitEntryFill().
				*/
				recordDrainProducerEvents(
					res.ProducerEvents,
				)

				saveProducerHistory()

				log.Printf(
					"[PRODUCER] stage=refund_consumed "+
						"producer=%s side=%s order_id=%s",
					entry.Producer,
					entry.Side,
					res.OrderID,
				)
			} else {
				/*
					The fill successfully committed into position state.

					committed is a post-commit lifecycle fact owned by the drain,
					so construct ProducerEvent directly.
				*/
				res.ProducerEvents =
					addDrainProducerEvent(
						res.ProducerEvents,
						ProducerStageCommitted,
						res.OrderID,
						"",
						"",
						false,
						false,
					)

				recordDrainProducerEvents(
					res.ProducerEvents,
				)

				saveProducerHistory()

				log.Printf(
					"[PRODUCER] stage=committed "+
						"producer=%s side=%s order_id=%s",
					entry.Producer,
					entry.Side,
					res.OrderID,
				)
			}
		} else {
			/*
				A non-fill terminal result allows the normal entry path to
				reconsider the order unless cancellation was requested because
				the signal changed.

				Trading behavior remains unchanged.
			*/
			cancelRequested :=
				pending != nil &&
					pending.CancelRequested

			if cancelRequested {
				// log.Printf(
				// "[TRACE] postonly.cancel.ack side=%s producer=%s order_id=%s fallback=false reason=signal_changed",
				// side,
				// entry.Producer,
				// res.OrderID,
				// )
			} else {
				// log.Printf(
				// "[TRACE] postonly.recheck side=%s producer=%s set=true reason=timeout_or_error order_id=%s",
				// side,
				// entry.Producer,
				// res.OrderID,
				// )
			}
		}

		/*
			finish() may call entry.clearOwner(), which acquires t.mu.

			step() already owns t.mu while draining entries.
		*/
		t.mu.Unlock()

		finish(
			res.ProducerEvents,
		)

		t.mu.Lock()

	default:
		// No asynchronous result is available for this entry this tick.
	}
}

// Entry Drain Wrapper
func (t *Trader) pendingEntriesSnapshot() []*PendingEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries := make([]*PendingEntry, 0, len(t.pendingEntries))
	for _, entry := range t.pendingEntries {
		if entry != nil {
			entries = append(entries, entry)
		}
	}

	return entries
}

// Commit Result is Drain Result Helper
func (t *Trader) commitEntryFill(
	entry *PendingEntry,
	res *OpenResult,
	now time.Time,
	wallNow time.Time,
) error {
	/*
		Commit lifecycle facts discovered here belong to the same
		ProducerAttempt created by the source wrapper.

		res.ProducerEvents is a map, so mutations made here remain visible
		to the drain even though OpenResult itself is passed by value.

		This helper must never create a ProducerAttempt or DecisionID.
	*/
	addCommitProducerEvent := func(
		stage ProducerStage,
		orderID string,
		produceErr *EntryProduceError,
		replace bool,
	) {
		if entry == nil ||
			entry.Intent == nil {

			return
		}

		if res.ProducerEvents == nil {
			res.ProducerEvents =
				make(map[ProducerStage]ProducerEvent)
		}

		if _, exists := res.ProducerEvents[stage]; exists && !replace {

			return
		}

		decisionID := strings.TrimSpace(
			entry.Intent.DecisionID,
		)
		if decisionID == "" {
			/*
				Producer correlation belongs to the wrapper.

				Do not manufacture a DecisionID here.
			*/
			return
		}

		event := ProducerEvent{
			Time:      time.Now().UTC(),
			CreatedAt: entry.Intent.CreatedAt,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			Stage:    stage,

			DecisionID: decisionID,
			OrderID:    orderID,

			Reason: entry.ProducerReason,
		}

		if produceErr != nil {
			event.ErrorCode =
				produceErr.Code

			event.CleanupRequired =
				produceErr.CleanupRequired

			if produceErr.Err != nil {
				event.Error =
					produceErr.Err.Error()
			}
		}

		res.ProducerEvents[stage] = event
	}

	if entry == nil {
		return &EntryProduceError{
			Code: EntryProduceErrCommitNilPendingEntry,

			CleanupRequired: false,

			Err: errors.New(
				"nil pending entry",
			),
		}
	}

	if entry.Intent == nil {
		return &EntryProduceError{
			Code: EntryProduceErrCommitNilPendingIntent,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: errors.New(
				"nil PendingIntent",
			),
		}
	}

	if entry.Book == nil {
		return &EntryProduceError{
			Code: EntryProduceErrCommitNilPositionBook,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: errors.New(
				"nil position book",
			),
		}
	}

	if res.Placed == nil {
		return &EntryProduceError{
			Code: EntryProduceErrCommitMissingExecution,

			Producer: entry.Producer,
			Side:     fmt.Sprint(entry.Side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: errors.New(
				"filled result missing execution",
			),
		}
	}

	side := entry.Side
	pending := entry.Intent
	book := entry.Book

	// policy := entryPolicyForSide(side)
	policy := entryPolicyForSource(
		entry.Producer,
	)

	priceToUse := res.Placed.Price
	baseToUse := res.Placed.BaseSize
	quoteSpent := res.Placed.QuoteSpent
	entryFee := res.Placed.CommissionUSD

	if priceToUse <= 0 {
		return &EntryProduceError{
			Code: EntryProduceErrCommitInvalidExecutionPrice,

			Producer: entry.Producer,
			Side:     fmt.Sprint(side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: fmt.Errorf(
				"invalid execution price %.8f",
				priceToUse,
			),
		}
	}

	if baseToUse <= 0 {
		return &EntryProduceError{
			Code: EntryProduceErrCommitInvalidExecutionBase,

			Producer: entry.Producer,
			Side:     fmt.Sprint(side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: fmt.Errorf(
				"invalid execution base %.8f",
				baseToUse,
			),
		}
	}

	if t.positionExistsByEntryOrderID(
		res.OrderID,
	) {
		/*
			Idempotent commit path.

			The exchange fill has already been represented by an existing
			position with this EntryOrderID.

			This is not a commit failure.
		*/
		return nil
	}

	if entryFee <= 0 {
		entryFee =
			quoteSpent *
				(t.cfg.FeeRatePct / 100.0)
	}

	/*
		Refund-service adjustment.
	*/
	if pending.RefundPortionUSD > 0 {
		originalBase := baseToUse
		originalQuote := quoteSpent
		originalFee := entryFee

		refundBase :=
			pending.RefundPortionUSD /
				priceToUse

		if refundBase > baseToUse {
			refundBase = baseToUse
		}

		if refundBase < 0 {
			refundBase = 0
		}

		keptBase :=
			baseToUse -
				refundBase

		if keptBase < 0 {
			keptBase = 0
		}

		keptQuote := quoteSpent
		keptFee := entryFee

		refundQuote :=
			pending.RefundPortionUSD

		refundFee :=
			refundQuote *
				(t.cfg.FeeRatePct / 100.0)

		if originalBase > 0 {
			keptRatio :=
				keptBase /
					originalBase

			refundRatio :=
				refundBase /
					originalBase

			keptQuote =
				originalQuote *
					keptRatio

			keptFee =
				originalFee *
					keptRatio

			refundQuote =
				originalQuote *
					refundRatio

			refundFee =
				originalFee *
					refundRatio
		}

		t.creditRefundService(
			side,
			refundQuote,
			refundFee,
		)

		baseToUse = keptBase
		quoteSpent = keptQuote
		entryFee = keptFee
	}

	if baseToUse <= 0 ||
		quoteSpent <= 0 {

		/*
			The refund service consumed the complete filled execution.

			This is successful processing, not a commit failure, but no
			Position is created.

			Record the distinct lifecycle outcome in the same OpenResult
			ProducerEvents map so the drain can persist it under the same
			DecisionID.
		*/
		addCommitProducerEvent(
			ProducerStageRefundConsumed,
			res.OrderID,
			nil,
			false,
		)

		return nil
	}

	newLot := &Position{
		OpenPrice:       priceToUse,
		Side:            side,
		SizeBase:        baseToUse,
		OpenTime:        now,
		EntryFee:        entryFee,
		OpenNotionalUSD: quoteSpent,
		ProducerReason:  pending.ProducerReason,
		Take:            pending.Take,
		Version:         Version,
		EntryOrderID:    res.OrderID,

		RefundPortionUSD: pending.RefundPortionUSD,
		ConfidenceMult:   pending.ConfidenceMult,
		EntryMethod:      pending.EntryMethod,
		ProfitGateUSD:    pending.ProfitGateUSD,

		Producer: entry.Producer,
	}

	if newLot.ConfidenceMult <= 0 {
		newLot.ConfidenceMult = 0
	}

	if newLot.EntryMethod == "" {
		newLot.EntryMethod = "UNKNOWN"
	}

	if newLot.ProfitGateUSD <= 0 {
		newLot.ProfitGateUSD =
			t.cfg.ProfitGateUSD
	}

	// log.Printf(
	// "[KPI] lot.created side=%s producer=%s mode=%s conf=%.2f gate=%.2f order_id=%s",
	// newLot.Side,
	// entry.Producer,
	// newLot.EntryMethod,
	// newLot.ConfidenceMult,
	// newLot.ProfitGateUSD,
	// newLot.EntryOrderID,
	// )

	book.Lots = append(
		book.Lots,
		newLot,
	)

	t.consolidateDust(
		book,
		priceToUse,
		t.cfg.MinNotional,
	)

	t.archiveOrphanDust(
		book,
		priceToUse,
		t.cfg.MinNotional,
	)

	t.didConsolidateStartup = false

	if entry.SpareUSD != nil {
		*entry.SpareUSD -= quoteSpent

		if *entry.SpareUSD < 0 {
			*entry.SpareUSD = 0
		}
	} else {
		/*
			This is not fatal.

			The position commit continues, but BOT OPS must retain the
			anomaly rather than losing it in a log message.
		*/
		produceErr := &EntryProduceError{
			Code: EntryProduceErrCommitSparePointerNil,

			Producer: entry.Producer,
			Side:     fmt.Sprint(side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: errors.New(
				"SpareUSD pointer is nil",
			),
		}

		addCommitProducerEvent(
			ProducerStageCommitSparePointerNil,
			res.OrderID,
			produceErr,
			false,
		)
	}

	// Promote Equity-produced entries into runners. NormalLegacy entries
	// remain scalp-only and therefore never receive runner assignment.
	if policy.AllowRunner &&
		entry.EquityTriggered {

		newIndex :=
			len(book.Lots) - 1

		addRunner(
			book,
			newIndex,
		)

		runner := book.Lots[newIndex]

		runner.TrailActive = false
		runner.TrailPeak =
			runner.OpenPrice
		runner.TrailStop = 0

		t.applyRunnerTargets(
			runner,
		)

		// log.Printf(
		// "[TRACE] runner.assign idx=%d side=%s producer=%s open=%.8f take=%.8f",
		// newIndex,
		// side,
		// entry.Producer,
		// runner.OpenPrice,
		// runner.Take,
		// )
	}

	if policy.ResetLastAdd &&
		entry.LastAdd != nil {

		*entry.LastAdd = wallNow
	}

	if policy.ResetWinExtreme &&
		entry.WinExtreme != nil {

		*entry.WinExtreme = priceToUse
	}

	if policy.ResetLatchedGate &&
		entry.LatchedGate != nil {

		*entry.LatchedGate = 0
	}

	if policy.UpdateEquityBaseline {
		oldEquityBaseline :=
			t.lastAddEquity

		t.lastAddEquity =
			t.equityUSD

		log.Printf(
			"[TRACE] equity.baseline.set "+
				"side=%s producer=%s old=%.2f new=%.2f",
			side,
			entry.Producer,
			oldEquityBaseline,
			t.lastAddEquity,
		)
	}

	/*
		Case 10 — Stabilize RegimeNormal.

		A successful normal entry consumes the currently active
		directional opportunity only when the entry agrees with it:

			UP   + BUY  -> NORMAL
			DOWN + SELL -> NORMAL

		Case3A replacements leave the regime unchanged because their
		producer policy sets ResetRegime=false.
	*/
	if policy.ResetRegime &&
		t.shouldResetRegime(side) {

		t.toNormal(
			fmt.Sprintf(
				"successful_entry_fill "+
					"producer=%s side=%s order_id=%s",
				entry.Producer,
				side,
				res.OrderID,
			),
		)
	}

	message := fmt.Sprintf(
		"[LIVE ORDER] %s quote=%.2f "+
			"take=%.2f fee=%.4f reason=%s [%s]",
		side,
		quoteSpent,
		newLot.Take,
		entryFee,
		newLot.ProducerReason,
		"async postonly filled",
	)

	if t.cfg.UseDirectSlack {
		postSlack(message)
	}

	if err := t.saveStateNoLock(); err != nil {

		return &EntryProduceError{
			Code: EntryProduceErrCommitPersistState,

			Producer: entry.Producer,
			Side:     fmt.Sprint(side),
			OrderID:  res.OrderID,

			CleanupRequired: false,

			Err: err,
		}
	}

	log.Printf(
		"[PRODUCER] stage=committed "+
			"producer=%s side=%s order_id=%s "+
			"price=%.8f base=%.8f fee_usd=%.8f reason=%q",
		entry.Producer,
		side,
		res.OrderID,
		newLot.OpenPrice,
		newLot.SizeBase,
		newLot.EntryFee,
		newLot.ProducerReason,
	)

	return nil
}

func (t *Trader) Case3ACommitEligible(
	entry *PendingEntry,
) bool {
	if t == nil || entry == nil {
		return false
	}

	if entry.Intent == nil {
		return false
	}

	if entry.Producer != EntryProducerCase3AReplacement {
		return true
	}

	sourceEntryOrderID :=
		strings.TrimSpace(entry.Intent.SourceEntryOrderID)

	if sourceEntryOrderID == "" {
		return false
	}

	// The originating exit is considered successfully committed only after
	// the source lot has been removed from the live position books.
	return !t.positionExistsByEntryOrderID(
		sourceEntryOrderID,
	)
}

/*startPendingMakerExit
    ↓
resultCh := make(chan ExitResult, 1)
    ↓
PendingExit.ResultC = resultCh
    ↓
pendingExits[oid] = p
    ↓
watchPendingExit(ctx, p)
    ↓
p.ResultC <- ExitResult
    ↓
pendingExitsSnapshot()
    ↓
drainPendingExit()*/

func (t *Trader) startPendingMakerExit(ctx context.Context, lotSide OrderSide, entryOrderID string, side OrderSide, exitReason string, exitDecision string, limitPx float64, baseRequested float64) error {
	_ = side

	closeSide := SideSell
	if lotSide == SideSell {
		closeSide = SideBuy
	}

	entryOrderID = strings.TrimSpace(entryOrderID)
	if entryOrderID == "" {
		return fmt.Errorf("invalid pending maker exit: empty entry_id")
	}

	if limitPx <= 0 || baseRequested <= 0 {
		return fmt.Errorf("invalid pending maker exit limit=%.8f base=%.8f entry_id=%s", limitPx, baseRequested, entryOrderID)
	}

	oid, err := t.broker.PlaceLimitPostOnly(ctx, t.cfg.ProductID, closeSide, limitPx, baseRequested)
	if err != nil {
		return err
	}
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return fmt.Errorf("empty maker exit order id entry_id=%s", entryOrderID)
	}

	t.mu.Lock()

	book := t.book(lotSide)
	var lot *Position
	for _, l := range book.Lots {
		if l != nil && strings.TrimSpace(l.EntryOrderID) == entryOrderID {
			lot = l
			break
		}
	}

	if lot == nil {
		t.mu.Unlock()
		_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, oid)
		return fmt.Errorf("lot disappeared before pending exit registration entry_id=%s", entryOrderID)
	}

	if strings.TrimSpace(lot.FixedTPOrderID) != "" {
		existing := strings.TrimSpace(lot.FixedTPOrderID)
		t.mu.Unlock()
		_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, oid)
		return fmt.Errorf("lot already has pending exit entry_id=%s exit_id=%s", entryOrderID, existing)
	}

	lot.FixedTPOrderID = oid

	resultCh := make(chan ExitResult, 1)

	p := &PendingExit{
		Side:          lot.Side,
		ProductID:     t.cfg.ProductID,
		OrderID:       oid,
		EntryOrderID:  lot.EntryOrderID,
		ExitReason:    exitReason,
		ExitDecision:  exitDecision,
		LimitPx:       limitPx,
		BaseRequested: baseRequested,
		Deadline:      time.Now().Add(time.Duration(t.cfg.LimitTimeoutSec) * time.Second),
		ResultC:       resultCh,
	}

	t.pendingExits[oid] = p

	// log.Printf("[TRACE] pending_exit.register exit_id=%s pending=%d", oid, len(t.pendingExits))
	// log.Printf("[TRACE] pending_exit.start side=%s exit_id=%s entry_id=%s limit=%.8f base=%.8f reason=%s", p.Side, p.OrderID, p.EntryOrderID, p.LimitPx, p.BaseRequested, p.ExitReason)

	if err := t.saveStateNoLock(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
	}

	t.mu.Unlock()

	go t.watchPendingExit(ctx, p)
	return nil
}

func (t *Trader) watchPendingExit(ctx context.Context, p *PendingExit) {
	var sessBase, sessQuote, sessFee float64
	var lastSeenBase, lastSeenQuote, lastSeenFee float64

	orderID := strings.TrimSpace(p.OrderID)
	lastLimitPx := p.LimitPx
	initLimit := lastLimitPx
	lastReprice := time.Now()
	repriceCount := 0

	cfg := t.cfg
	tick := cfg.PriceTick
	baseStep := cfg.BaseStep
	offsetBps := cfg.LimitPriceOffsetBps
	minNotional := cfg.MinNotional
	if minNotional <= 0 {
		minNotional = cfg.OrderMinUSD
	}

	closeSide := SideSell
	if p.Side == SideSell {
		closeSide = SideBuy
	}

	accrue := func(ord *PlacedOrder) {
		if ord == nil {
			return
		}

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

		sessBase += dBase
		sessQuote += dQuote
		sessFee += dFee

		lastSeenBase = ord.BaseSize
		lastSeenQuote = ord.QuoteSpent
		lastSeenFee = ord.CommissionUSD
	}

	emit := func(exitID string) {
		var placed *PlacedOrder
		filled := sessBase > 0 || sessQuote > 0

		if filled {
			vwap := 0.0
			if sessBase > 0 {
				vwap = sessQuote / sessBase
			}

			placed = &PlacedOrder{
				Price:         vwap,
				BaseSize:      sessBase,
				QuoteSpent:    sessQuote,
				CommissionUSD: sessFee,
			}
		}

		select {
		case p.ResultC <- ExitResult{
			Filled:  filled,
			Placed:  placed,
			OrderID: exitID,
			Pending: p,
		}:
		case <-ctx.Done():
			return
		}
	}

	for time.Now().Before(p.Deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ord, err := t.broker.GetOrder(ctx, p.ProductID, orderID)
		if err == nil && ord != nil {
			accrue(ord)

			status := strings.ToUpper(strings.TrimSpace(ord.Status))
			// log.Printf(
			// "[TRACE] pending_exit.poll.tick side=%s exit_id=%s entry_id=%s status=%s price=%.8f base=%.8f quote=%.2f fee=%.6f sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
			// p.Side,
			// orderID,
			// p.EntryOrderID,
			// status,
			// ord.Price,
			// ord.BaseSize,
			// ord.QuoteSpent,
			// ord.CommissionUSD,
			// sessBase,
			// sessQuote,
			// sessFee,
			// )

			switch status {
			case "FILLED":
				emit(orderID)
				return
			case "CANCELED", "REJECTED", "EXPIRED":
				emit(orderID)
				return
			}
		}

		if cfg.RepriceEnable &&
			cfg.RepriceIntervalMs > 0 &&
			time.Since(lastReprice) >= time.Duration(cfg.RepriceIntervalMs)*time.Millisecond {

			if cfg.RepriceMaxCount <= 0 || repriceCount < cfg.RepriceMaxCount {
				ctxPx, cancelPx := context.WithTimeout(ctx, time.Second)
				px, gErr := t.broker.GetNowPrice(ctxPx, p.ProductID)
				cancelPx()

				if gErr == nil && px > 0 {
					newLimitPx := px
					if closeSide == SideSell {
						newLimitPx = px * (1.0 + offsetBps/10000.0)
					} else {
						newLimitPx = px * (1.0 - offsetBps/10000.0)
					}

					if tick > 0 {
						if closeSide == SideSell {
							newLimitPx = math.Ceil(newLimitPx/tick) * tick
						} else {
							newLimitPx = math.Floor(newLimitPx/tick) * tick
						}
					}

					shouldReprice := (tick > 0 && math.Abs(newLimitPx-lastLimitPx) >= tick) ||
						(tick <= 0 && newLimitPx != lastLimitPx)

					if shouldReprice && cfg.RepriceMaxDriftBps > 0 && initLimit > 0 {
						driftBps := math.Abs((newLimitPx-initLimit)/initLimit) * 10000.0
						if driftBps > cfg.RepriceMaxDriftBps {
							shouldReprice = false
						}
					}

					if shouldReprice && tick > 0 && cfg.RepriceMinImprovTicks > 1 {
						improveTicks := int(math.Abs(newLimitPx-lastLimitPx) / tick)

						if closeSide == SideSell &&
							!(newLimitPx > lastLimitPx && improveTicks >= cfg.RepriceMinImprovTicks) {
							shouldReprice = false
						}

						if closeSide == SideBuy &&
							!(newLimitPx < lastLimitPx && improveTicks >= cfg.RepriceMinImprovTicks) {
							shouldReprice = false
						}
					}

					newBase := p.BaseRequested
					if baseStep > 0 {
						newBase = math.Floor((newBase/baseStep)+1e-12) * baseStep
					}

					if shouldReprice && cfg.RepriceMinEdgeUSD > 0 && newBase > 0 {
						edgeUSD := math.Abs(newLimitPx-lastLimitPx) * newBase
						if edgeUSD < cfg.RepriceMinEdgeUSD {
							shouldReprice = false
						}
					}

					if shouldReprice && !(newBase > 0 && newBase*newLimitPx >= minNotional) {
						shouldReprice = false
					}

					if shouldReprice {
						oldID := orderID
						_ = t.broker.CancelOrder(ctx, p.ProductID, oldID)

						if oldOrd, oldErr := t.broker.GetOrder(ctx, p.ProductID, oldID); oldErr == nil && oldOrd != nil {
							accrue(oldOrd)
						}

						newID, perr := t.broker.PlaceLimitPostOnly(ctx, p.ProductID, closeSide, newLimitPx, newBase)
						newID = strings.TrimSpace(newID)

						if perr == nil && newID != "" {
							orderID = newID
							lastLimitPx = newLimitPx
							repriceCount++
							lastSeenBase = 0
							lastSeenQuote = 0
							lastSeenFee = 0

							t.apply(func(tt *Trader) {
								delete(tt.pendingExits, oldID)

								p.OrderID = newID
								p.LimitPx = newLimitPx
								p.BaseRequested = newBase
								tt.pendingExits[newID] = p

								book := tt.book(p.Side)
								for _, lot := range book.Lots {
									if lot != nil && strings.TrimSpace(lot.EntryOrderID) == strings.TrimSpace(p.EntryOrderID) {
										lot.FixedTPOrderID = newID
										break
									}
								}

								_ = tt.saveStateFrom(tt.snapshotStateLocked())
							})

							// log.Printf(
							// "[TRACE] pending_exit.reprice side=%s old_exit_id=%s new_exit_id=%s entry_id=%s limit=%.8f base=%.8f count=%d",
							// p.Side,
							// oldID,
							// newID,
							// p.EntryOrderID,
							// newLimitPx,
							// newBase,
							// repriceCount,
							// )
						}
					}
				}
			}

			lastReprice = time.Now()
		}

		time.Sleep(200 * time.Millisecond)
	}

	_ = t.broker.CancelOrder(ctx, p.ProductID, orderID)

	if ord, err := t.broker.GetOrder(ctx, p.ProductID, orderID); err == nil && ord != nil {
		accrue(ord)
	}

	// log.Printf(
	// "[TRACE] pending_exit.timeout_cancel exit_id=%s entry_id=%s sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
	// orderID,
	// p.EntryOrderID,
	// sessBase,
	// sessQuote,
	// sessFee,
	// )

	emit(orderID)
}

// Exit Drain Result
func (t *Trader) drainPendingExit(
	ctx context.Context,
	exit *PendingExit,
	candles []Candle,
	livePrice float64,
) {
	if exit == nil || exit.ResultC == nil {
		return
	}

	select {
	case res := <-exit.ResultC:
		t.completePendingExit(
			ctx,
			candles,
			livePrice,
			res,
		)

	default:
		return
	}
}
func (t *Trader) completePendingExit(ctx context.Context, candles []Candle, livePrice float64, res ExitResult) {
	_ = ctx
	_ = candles

	p := res.Pending
	if p == nil {
		// log.Printf("[TRACE] pending_exit.apply_skip reason=nil_pending order_id=%s", res.OrderID)
		return
	}

	orderID := strings.TrimSpace(res.OrderID)
	if orderID == "" {
		orderID = strings.TrimSpace(p.OrderID)
	}

	book := t.book(p.Side)

	localIdx := -1
	var lot *Position
	for i, l := range book.Lots {
		if l != nil && strings.TrimSpace(l.EntryOrderID) == strings.TrimSpace(p.EntryOrderID) {
			lot = l
			localIdx = i
			break
		}
	}

	if lot == nil || localIdx < 0 {
		delete(t.pendingExits, orderID)
		// log.Printf("[TRACE] pending_exit.apply_skip reason=lot_not_found order_id=%s entry_id=%s", orderID, p.EntryOrderID)
		_ = t.saveStateNoLock()
		return
	}

	lot.FixedTPOrderID = ""

	if !res.Filled || res.Placed == nil {
		delete(t.pendingExits, orderID)
		// log.Printf("[TRACE] pending_exit.unfilled order_id=%s entry_id=%s reason=%s", orderID, p.EntryOrderID, p.ExitReason)
		_ = t.saveStateNoLock()
		return
	}

	placed := res.Placed
	exitTime := time.Now().UTC()

	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}

	baseRequested := p.BaseRequested
	if baseRequested <= 0 {
		baseRequested = floorToStep(lot.SizeBase, t.cfg.BaseStep)
	}
	if baseRequested <= 0 {
		delete(t.pendingExits, orderID)
		// log.Printf("[TRACE] pending_exit.apply_skip reason=bad_base_requested order_id=%s", orderID)
		_ = t.saveStateNoLock()
		return
	}

	priceExec := livePrice
	if placed.Price > 0 {
		priceExec = placed.Price
	}

	baseFilled := baseRequested
	if placed.BaseSize > 0 {
		baseFilled = placed.BaseSize
	}
	if baseFilled > baseRequested {
		baseFilled = baseRequested
	}

	const tol = 1e-9
	if baseFilled+tol < baseRequested {
		log.Printf("[WARN] partial fill (pending exit): requested_base=%.8f filled_base=%.8f (%.2f%%)", baseRequested, baseFilled, 100.0*(baseFilled/baseRequested))
		// log.Printf("[TRACE] pending_exit.partial order_id=%s requested=%.8f filled=%.8f", orderID, baseRequested, baseFilled)
	}

	commissionUSD := 0.0
	if placed.CommissionUSD > 0 {
		commissionUSD = placed.CommissionUSD
	}

	wasNewest := localIdx == len(book.Lots)-1

	msg, err := t.applyFilledExitLocked(livePrice, priceExec, baseRequested, baseFilled, p.Side, localIdx, p.ExitReason, p.ExitDecision, exitTime, orderID, commissionUSD, minNotional, wasNewest)
	if err != nil {
		// log.Printf("[TRACE] pending_exit.apply_error order_id=%s err=%v", orderID, err)
		_ = t.saveStateNoLock()
		return
	}

	delete(t.pendingExits, orderID)
	_ = t.saveStateNoLock()

	log.Printf("[TRACE] pending_exit.applied order_id=%s entry_id=%s msg=%s", orderID, p.EntryOrderID, msg)
}

// Exit Drain Wrapper
func (t *Trader) pendingExitsSnapshot() []*PendingExit {
	t.mu.Lock()
	defer t.mu.Unlock()

	exits := make([]*PendingExit, 0, len(t.pendingExits))

	for _, exit := range t.pendingExits {
		if exit != nil {
			exits = append(exits, exit)
		}
	}

	return exits
}

type PendingProducerCounts struct {
	ByProducer map[EntryProducer]map[OrderSide]int

	TotalBuy  int
	TotalSell int
}

func (p PendingProducerCounts) Count(
	producer EntryProducer,
	side OrderSide,
) int {
	if bySide, ok := p.ByProducer[producer]; ok {
		return bySide[side]
	}

	return 0
}

func (t *Trader) pendingProducerCountsNoLock() PendingProducerCounts {
	result := PendingProducerCounts{
		ByProducer: make(
			map[EntryProducer]map[OrderSide]int,
		),
	}

	for _, entry := range t.pendingEntries {
		if entry == nil ||
			entry.Completed ||
			entry.Intent == nil {
			continue
		}

		producer := entry.Producer
		side := entry.Side

		if producer == EntryProducerNone {
			continue
		}

		switch side {
		case SideBuy:
			result.TotalBuy++

		case SideSell:
			result.TotalSell++

		default:
			continue
		}

		if _, ok := result.ByProducer[producer]; !ok {
			result.ByProducer[producer] =
				make(map[OrderSide]int)
		}

		result.ByProducer[producer][side]++
	}

	return result
}

func (t *Trader) handleCase3AReplacementError(
	repl PendingIntent,
	anchorID string,
	err error,
) {
	if err == nil {
		return
	}

	var produceErr *EntryProduceError

	if errors.As(err, &produceErr) {
		switch produceErr.Code {

		case EntryProduceErrPostOnlySubmit,
			EntryProduceErrSubmitNetworkFailed,
			EntryProduceErrInsufficientBalance,
			EntryProduceErrPostOnlyRejected,
			EntryProduceErrSubmitTimeout,
			EntryProduceErrRateLimited,
			EntryProduceErrExchangeRejected,
			EntryProduceErrPersistState:

			/*
				These granular submission classifications all originate from
				the same submission failure family that was previously represented
				by EntryProduceErrPostOnlySubmit.

				Keep existing Case3A retry behavior unchanged while preserving the
				more precise observability ErrorCode.
			*/
			t.markCase3AReplacementRetryLocked(
				repl,
				anchorID,
				fmt.Sprintf(
					"initial_modeB_replacement_retryable: %v",
					err,
				),
			)

		case EntryProduceErrCleanupCancel:

			log.Printf(
				"[ERROR] Case3A.replacement.cleanup_uncertain "+
					"method=%s anchor_id=%s order_id=%s err=%v",
				repl.RecoveryMethod.String(),
				anchorID,
				produceErr.OrderID,
				err,
			)

		case EntryProduceErrInvalidSide,
			EntryProduceErrMissingProductID,
			EntryProduceErrInvalidPrice,
			EntryProduceErrInvalidQuantity,
			EntryProduceErrInvalidQuote,
			EntryProduceErrInvalidTake,
			EntryProduceErrInvalidRefundPortion,
			EntryProduceErrInvalidConfidenceMult,
			EntryProduceErrInvalidProfitGate,
			EntryProduceErrMissingProducer,
			EntryProduceErrMissingProducerReason,
			EntryProduceErrMissingPendingCancelPolicy,
			EntryProduceErrBuildMissingOrderID,
			EntryProduceErrBuildUnsupportedSide,
			EntryProduceErrRegisterMissingOrderID,
			EntryProduceErrRegisterDuplicateOrderID:

			log.Printf(
				"[ERROR] Case3A.replacement.non_retryable "+
					"code=%s method=%s anchor_id=%s "+
					"order_id=%s err=%v",
				produceErr.Code,
				repl.RecoveryMethod.String(),
				anchorID,
				produceErr.OrderID,
				err,
			)

		default:

			log.Printf(
				"[ERROR] Case3A.replacement.unclassified_non_retryable "+
					"code=%s method=%s anchor_id=%s "+
					"order_id=%s err=%v",
				produceErr.Code,
				repl.RecoveryMethod.String(),
				anchorID,
				produceErr.OrderID,
				err,
			)
		}

		return
	}

	/*
		Non-EntryProduceError failures are also non-retryable by default.

		They remain visible in the log so they can be investigated and
		classified deliberately later rather than being retried blindly.
	*/
	log.Printf(
		"[ERROR] Case3A.replacement.unclassified_non_retryable "+
			"method=%s anchor_id=%s err=%v",
		repl.RecoveryMethod.String(),
		anchorID,
		err,
	)
}
