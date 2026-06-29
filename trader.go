// FILE: trader.go
// Package main – Position/risk management and the synchronized trading loop.
//
// What’s here:
//   • Position state (open price/side/size/stop/take)
//   • Trader: holds config, broker, model, equity/PnL, and mutex
//   • step(): the core synchronized tick that may OPEN, HOLD, or EXIT
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
	Reason string `json:"reason,omitempty"`

	// --- NEW (profit-gate data model) ---
	EstExitFeeUSD    float64  `json:"est_exit_fee_usd,omitempty"` // recomputed each tick from mark
	UnrealizedPnLUSD float64  `json:"unrealized_pnl_usd"`         // NET = gross - entry - estExit
	ExitMode         ExitMode `json:"exit_mode,omitempty"`        // RunnerTrailing | ScalpFixedTP
	Version          int      `json:"version"`
	FixedTPWorking   bool     `json:"-"` // internal flag: emulate a posted TP (re-post each tick while gate holds)
	ConfidenceMult   float64  `json:"confidence_mult,omitempty"`
	ProfitGateUSD    float64  `json:"profit_gate_usd,omitempty"`
	EntryAIMode      string   `json:"entry_ai_mode,omitempty"` // AI_MATCH or AI_FLAT

	TrailActivateGateUSD float64 `json:"activate_gate_usd"` // from TRAIL_ACTIVATE_USD (runner/scalp)
	TrailDistancePct     float64 `json:"distance_pct"`      // from TRAIL_DISTANCE_PCT (runner/scalp)

	// --- NEW: track maker-first TP exit order id (post-only limit attempt) ---
	FixedTPOrderID   string  `json:"-"`
	RefundPortionUSD float64 `json:"refund_portion_usd"`

	// --- NEW: stable lot identifier & entry order id (persisted) ---
	EntryOrderID string `json:"entry_order_id,omitempty"`
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
	LastAddEquitySell float64
	LastAddEquityBuy  float64

	// --- NEW: persist equity trigger staging indices per side ---
	EquityStageBuy  int
	EquityStageSell int
	Exits           []ExitRecord

	// --- NEW (persist pending maker-first opens & recheck flags) ---
	PendingBuy         *PendingOpen
	PendingSell        *PendingOpen
	PendingRecheckBuy  bool
	PendingRecheckSell bool
	RefundBuyUSD       float64
	RefundSellUSD      float64
	SpareBuyUSD        float64
	SpareSellUSD       float64
	PreviousAIRaw      Signal
	PendingExits       map[string]*PendingExit
}

// --- NEW (Phase 1): pending async maker-first open support ---
type PendingOpen struct {
	Side             OrderSide
	LimitPx          float64
	BaseAtLimit      float64
	Quote            float64
	Take             float64
	Reason           string
	RefundPortionUSD float64 `json:"refund_portion_usd"`
	ProductID        string
	CreatedAt        time.Time
	Deadline         time.Time
	EquityBuy        bool // whether this was equityTriggerBuy
	EquitySell       bool // whether this was equityTriggerSell
	// --- NEW: persisted working order id to allow rehydration ---
	OrderID string
	// NEW: keep last few order IDs so we accept late fills after a cancel/reprice.
	History []string `json:"history,omitempty"` // capped (e.g., last 5)
	// NEW: accumulate fills across reprices
	AccumBase       float64 // sum of executed base over all prior order IDs
	AccumQuote      float64 // sum of executed quote
	AccumFeeUSD     float64 // sum of fees (if provided)
	ConfidenceMult  float64 `json:"confidence_mult,omitempty"`
	ProfitGateUSD   float64 `json:"profit_gate_usd,omitempty"`
	EntryAIMode     string  `json:"entry_ai_mode,omitempty"` // AI_MATCH or AI_FLAT
	CancelRequested bool    `json:"cancel_requested,omitempty"`
}

type OpenResult struct {
	Filled  bool
	Placed  *PlacedOrder
	Err     error
	OrderID string
}

type PendingExit struct {
	Side          OrderSide `json:"side"`
	ProductID     string    `json:"product_id"`
	OrderID       string    `json:"order_id"`
	EntryOrderID  string    `json:"entry_order_id"`
	ExitReason    string    `json:"exit_reason"`
	ExitDecision  string    `json:"exit_decision"`
	LimitPx       float64   `json:"limit_px"`
	BaseRequested float64   `json:"base_requested"`
	Deadline      time.Time `json:"deadline"`
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
	lastAddEquitySell float64 // replaces legacy lastAddEquity (SELL path)
	lastAddEquityBuy  float64 // BUY-side dip trigger baseline (not persisted)

	// --- NEW: equity trigger staging indices per side (0..3 for 25/50/75/100) ---
	equityStageBuy  int
	equityStageSell int

	// daily
	dailyStart time.Time
	dailyPnL   float64
	lastExits  []ExitRecord

	// --- NEW (Phase 4): async maker-first open state per-side ---
	pendingBuy        *PendingOpen
	pendingSell       *PendingOpen
	pendingBuyCh      chan OpenResult
	pendingSellCh     chan OpenResult
	pendingBuyCtx     context.Context
	pendingSellCtx    context.Context
	pendingBuyCancel  context.CancelFunc
	pendingSellCancel context.CancelFunc

	pendingExitCh chan ExitResult
	pendingExits  map[string]*PendingExit // key = orderID

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
}

func NewTrader(cfg Config, broker Broker) *Trader {
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,
		// runnerIdx:  -1,
		books: map[OrderSide]*SideBook{
			SideBuy:  {RunnerIDs: []int{}, Lots: nil},
			SideSell: {RunnerIDs: []int{}, Lots: nil},
		},
		stateApplyCh: make(chan func(*Trader), 128),
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
			if !t.cfg.DryRun && shouldFatalNoStateMount(t.stateFile) {
				log.Fatalf("[FATAL] persistence required but state path is not a mounted volume or not writable: STATE_FILE=%s ; "+
					"mount /opt/coinbase/state into the container and ensure it's writable. "+
					"Example docker-compose:\n  volumes:\n    - /opt/coinbase/state:/opt/coinbase/state",
					t.stateFile)
			}
		}
	}

	// In NewTrader(), use larger buffer:
	t.pendingExitCh = make(chan ExitResult, 64)
	t.pendingExits = make(map[string]*PendingExit)

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
		log.Printf("TRACE state.save error=%v", err)
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
			log.Printf("TRACE state.save error=%v", err)
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
		log.Printf("TRACE trail.activate side=%s activate_usd=%.2f net=%.2f price=%.8f peak=%.8f stop=%.8f",
			lot.Side, actUSD, lot.UnrealizedPnLUSD, price, lot.TrailPeak, lot.TrailStop)
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
					log.Printf("TRACE trail.raise lotSide=BUY peak=%.8f stop=%.8f", lot.TrailPeak, lot.TrailStop)
				}
			}
			if price <= lot.TrailStop && lot.TrailStop > 0 {
				// --- breadcrumb ---
				log.Printf("TRACE trail.trigger lotSide=BUY price=%.8f stop=%.8f", price, lot.TrailStop)
				return true, lot.TrailStop
			}
		} else { // SELL
			if price < lot.TrailPeak {
				lot.TrailPeak = price
				lot.TrailStop = lot.TrailPeak * (1.0 + distPct/100.0)
				// --- breadcrumb ---
				log.Printf("TRACE trail.raise lotSide=SELL trough=%.8f stop=%.8f", lot.TrailPeak, lot.TrailStop)
			}
			if price >= lot.TrailStop && lot.TrailStop > 0 {
				// --- breadcrumb ---
				log.Printf("TRACE trail.trigger lotSide=SELL price=%.8f stop=%.8f", price, lot.TrailStop)
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

	// tag reason with the absorbed lot's original EntryOrderID
	a.Reason = strings.TrimSpace(a.Reason + "|merge:" + b.EntryOrderID)

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

		// tag reason
		survivor.Reason = strings.TrimSpace(survivor.Reason + "|mergedRunner:" + source.EntryOrderID)

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
	// - we did NOT touch t.lastAddEquityBuy / t.lastAddEquitySell / t.equityStage*
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

func decisionFlatReason(d Decision) string {
	parts := []string{
		// AI / model summary
		fmt.Sprintf("pUp=%.5f", d.PUp),
		fmt.Sprintf("confidence=%.2f", d.Confidence),
		fmt.Sprintf("buyTh=%.5f", d.BuyThreshold),
		fmt.Sprintf("sellTh=%.5f", d.SellThreshold),

		// Decision summary
		fmt.Sprintf("aiRaw=%s", d.Raw),
		fmt.Sprintf("logicOpinion=%s", d.LogicOpinion),
		fmt.Sprintf("final=%s", d.Signal),

		// Logic gate
		fmt.Sprintf("logicEPS=%.5f", d.LogicEPS),
	}

	// Exit-only fields
	if d.PreviousAIRaw != Flat {
		parts = append(parts, fmt.Sprintf("previousAIRaw=%s", d.PreviousAIRaw))
	}
	if d.ExitNetPNLUSD != 0 {
		parts = append(parts, fmt.Sprintf("exitNetPNL=%.5f", d.ExitNetPNLUSD))
	}
	if d.StopLossLimitUSD != 0 {
		parts = append(parts, fmt.Sprintf("stopLossLimit=%.5f", d.StopLossLimitUSD))
	}
	if strings.TrimSpace(d.ExitClass) != "" {
		parts = append(parts, fmt.Sprintf("exitClass=%s", strings.TrimSpace(d.ExitClass)))
	}

	// Logic MACD
	parts = append(parts,
		fmt.Sprintf("logic_macd_line=%.5f", d.LogicMACDLine),
		fmt.Sprintf("logic_macd_turn=%.5f", d.LogicMACDTurn),
		fmt.Sprintf("logic_macd_hist=%.5f", d.LogicMACDHist),
		fmt.Sprintf("logic_macd_dhist=%.5f", d.LogicMACDDHist),
		fmt.Sprintf("logic_macd_dsmooth=%.5f", d.LogicMACDDSmooth),
		fmt.Sprintf("logic_macd_strong_positive=%t", d.LogicMACDStrongPositive),
		fmt.Sprintf("logic_macd_strong_negative=%t", d.LogicMACDStrongNegative),
		fmt.Sprintf("logic_macd_momentum_down=%t", d.LogicMACDMomentumDown),
		fmt.Sprintf("logic_macd_momentum_up=%t", d.LogicMACDMomentumUp),

		// Logic EMA
		fmt.Sprintf("logic_ema_spread=%.6f", d.LogicEMASpread),
		fmt.Sprintf("logic_ema2050=%.6f", d.LogicEMA2050),

		// Logic pattern
		fmt.Sprintf("logic_pattern_high_peak=%t", d.LogicPatternHighPeak),
		fmt.Sprintf("logic_pattern_low_bottom=%t", d.LogicPatternLowBottom),
		fmt.Sprintf("logic_pattern_price_down_up=%t", d.LogicPatternPriceDownUp),
		fmt.Sprintf("logic_pattern_price_up_down=%t", d.LogicPatternPriceUpDown),
		fmt.Sprintf("logic_pattern_buy=%t", d.LogicPatternBuy),
		fmt.Sprintf("logic_pattern_sell=%t", d.LogicPatternSell),
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

		// Persist SELL equity baseline; keep legacy field for older state compatibility.
		LastAddEquitySell: t.lastAddEquitySell,
		LastAddEquityBuy:  t.lastAddEquityBuy,

		// Persist equity stages
		EquityStageBuy:  t.equityStageBuy,
		EquityStageSell: t.equityStageSell,
		Exits:           t.lastExits,

		// NEW: persist pending and recheck flags
		PendingBuy:         t.pendingBuy,
		PendingSell:        t.pendingSell,
		PendingRecheckBuy:  t.pendingRecheckBuy,
		PendingRecheckSell: t.pendingRecheckSell,
		RefundBuyUSD:       t.refundBuyUSD,
		RefundSellUSD:      t.refundSellUSD,
		SpareBuyUSD:        t.SpareBuyUSD,
		SpareSellUSD:       t.SpareSellUSD,
		PendingExits:       t.pendingExits,
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
	if !(t.cfg.DryRun || t.cfg.UseLiveEquity()) {
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

	// Restore per-side books (no migration; assume st.Book*.RunnerIDs reflects persisted state)
	t.books[SideBuy] = &SideBook{
		RunnerIDs: st.BookBuy.RunnerIDs,
		Lots:      st.BookBuy.Lots,
	}
	t.books[SideSell] = &SideBook{
		RunnerIDs: st.BookSell.RunnerIDs,
		Lots:      st.BookSell.Lots,
	}

	// Backfill per-lot trailing/TP defaults based on runner vs scalp buckets
	containsIdx := func(xs []int, i int) bool {
		for _, v := range xs {
			if v == i {
				return true
			}
		}
		return false
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
	t.lastAddEquitySell = st.LastAddEquitySell
	t.lastAddEquityBuy = st.LastAddEquityBuy
	t.lastExits = st.Exits

	// Restore equity stages
	t.equityStageBuy = st.EquityStageBuy
	t.equityStageSell = st.EquityStageSell

	// Restore pending & recheck flags
	t.pendingBuy = st.PendingBuy
	t.pendingSell = st.PendingSell
	t.pendingRecheckBuy = st.PendingRecheckBuy
	t.pendingRecheckSell = st.PendingRecheckSell

	t.pendingExits = st.PendingExits
	if t.pendingExits == nil {
		t.pendingExits = make(map[string]*PendingExit)
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

func (t *Trader) pendingCancelRequested(side OrderSide) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if side == SideBuy && t.pendingBuy != nil {
		return t.pendingBuy.CancelRequested
	}
	if side == SideSell && t.pendingSell != nil {
		return t.pendingSell.CancelRequested
	}
	return false
}

// RehydratePending resumes any persisted post-only pending opens by restoring channels/contexts
// and restarting the poller using the saved OrderID and remaining deadline if the order is still open.
func (t *Trader) RehydratePending(ctx context.Context, mode RehydrateMode) {
	if mode != RehydrateModeResume {
		return
	}

	t.mu.Lock()
	defer t.unlockSafe()

	rehydrateOne := func(pend **PendingOpen, ch *chan OpenResult, pctx *context.Context, cancel *context.CancelFunc) {
		// No pending object → nothing to resume.
		if pend == nil || *pend == nil {
			return
		}
		p := *pend

		// If deadline passed, allow market fallback via recheck and clear pending.
		if !p.Deadline.IsZero() && time.Now().After(p.Deadline) {
			if p.Side == SideBuy {
				t.pendingRecheckBuy = true
			} else {
				t.pendingRecheckSell = true
			}
			*pend = nil
			_ = t.saveStateNoLock() // best-effort
			return
		}

		// Check current server-side status of the saved order (unlock for I/O).
		var ord *PlacedOrder
		if strings.TrimSpace(p.OrderID) != "" {
			b := t.broker // capture broker pointer for I/O while unlocked
			t.mu.Unlock()
			o, err := b.GetOrder(ctx, p.ProductID, p.OrderID)
			t.mu.Lock()
			if err == nil && o != nil {
				// If it already filled while we were down, emit completion and clear pending.
				if o.BaseSize > 0 || o.QuoteSpent > 0 {
					if *ch == nil {
						cc := make(chan OpenResult, 1)
						*ch = cc
					}
					select {
					case (*ch) <- OpenResult{Filled: true, Placed: o, OrderID: p.OrderID}:
					default:
					}
					_ = t.saveStateNoLock()
					return
				}
				ord = o // still open
			}
		}

		// If order not found (or nil), clear pending and enable market fallback on next tick.
		if ord == nil {
			if p.Side == SideBuy {
				t.pendingRecheckBuy = true
			} else {
				t.pendingRecheckSell = true
			}
			*pend = nil // FIX: was 'pend = nil' (no effect); must clear the caller's pointer
			_ = t.saveStateNoLock()
			return
		}

		// Ensure completion channel exists.
		if *ch == nil {
			cc := make(chan OpenResult, 1)
			*ch = cc
		}

		// Create a fresh context+cancel used by the poller goroutine.
		pc, cn := context.WithCancel(ctx)
		*pctx = pc
		*cancel = cn

		// Capture cfg fields locally to avoid concurrent reads from t inside the goroutine.
		cfg := t.cfg
		offsetBps := cfg.LimitPriceOffsetBps
		tick := cfg.PriceTick
		minNotional := cfg.MinNotional
		if minNotional <= 0 {
			minNotional = cfg.OrderMinUSD
		}
		baseStep := cfg.BaseStep
		productID := p.ProductID
		side := p.Side
		b := t.broker // capture broker

		// Spawn poller (resume) — reprice and watch for fill until deadline, with env guardrails
		go func(pcopy *PendingOpen, chOut chan OpenResult, pc context.Context) {
			orderID := pcopy.OrderID
			lastLimitPx := pcopy.LimitPx
			initLimit := lastLimitPx
			lastReprice := time.Now()
			var filled *PlacedOrder
			deadline := pcopy.Deadline

			// --- ENV GUARDRAILS (read once per poller) ---

			var repriceCount int

		poll:
			for time.Now().Before(deadline) {
				// Cancellation check
				select {
				case <-pc.Done():
					break poll
				default:
				}

				// 1) Check for fill
				if ord, gErr := b.GetOrder(pc, productID, orderID); gErr == nil && ord != nil && (ord.BaseSize > 0 || ord.QuoteSpent > 0) {
					filled = ord
					log.Printf("TRACE postonly.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
						orderID, filled.Price, filled.BaseSize, filled.QuoteSpent, filled.CommissionUSD)
					break
				}

				// 2) Periodic reprice (guarded)
				if time.Since(lastReprice) >= time.Duration(t.cfg.RepriceIntervalMs)*time.Millisecond {
					if t.cfg.RepriceEnable && (t.cfg.RepriceMaxCount <= 0 || repriceCount < t.cfg.RepriceMaxCount) {
						ctxPx, cancelPx := context.WithTimeout(pc, 1*time.Second)
						px, gErr := b.GetNowPrice(ctxPx, productID)
						cancelPx()
						if gErr == nil && px > 0 {
							newLimitPx := px
							if side == SideBuy {
								newLimitPx = px * (1.0 - offsetBps/10000.0)
							} else {
								newLimitPx = px * (1.0 + offsetBps/10000.0)
							}
							if tick > 0 {
								newLimitPx = math.Floor(newLimitPx/tick) * tick
							}
							shouldReprice := (tick > 0 && math.Abs(newLimitPx-lastLimitPx) >= tick) || (tick <= 0 && newLimitPx != lastLimitPx)

							// Guard: max drift from initial (bps)
							if shouldReprice && t.cfg.RepriceMaxDriftBps > 0 {
								driftBps := math.Abs((newLimitPx-initLimit)/initLimit) * 10000.0
								if driftBps > t.cfg.RepriceMaxDriftBps {
									shouldReprice = false
								}
							}

							// Guard: minimum improvement ticks (directional)
							if shouldReprice && tick > 0 && t.cfg.RepriceMinImprovTicks > 1 {
								improveTicks := int(math.Abs(newLimitPx-lastLimitPx) / tick)
								if side == SideBuy {
									if !(newLimitPx < lastLimitPx && improveTicks >= t.cfg.RepriceMinImprovTicks) {
										shouldReprice = false
									}
								} else {
									if !(newLimitPx > lastLimitPx && improveTicks >= t.cfg.RepriceMinImprovTicks) {
										shouldReprice = false
									}
								}
							}

							// Recompute base from quote
							newBase := pcopy.BaseAtLimit
							if pcopy.Quote > 0 {
								newBase = pcopy.Quote / newLimitPx
							}
							if baseStep > 0 {
								newBase = math.Floor(newBase/baseStep) * baseStep
							}

							// Guard: min edge USD
							if shouldReprice && t.cfg.RepriceMinEdgeUSD > 0 && newBase > 0 {
								edgeUSD := math.Abs(newLimitPx-lastLimitPx) * newBase
								if edgeUSD < t.cfg.RepriceMinEdgeUSD {
									shouldReprice = false
								}
							}

							// Ensure min-notional
							if shouldReprice && !(newBase > 0 && newBase*newLimitPx >= minNotional) {
								shouldReprice = false
							}

							if shouldReprice {
								_ = b.CancelOrder(pc, productID, orderID)
								if newID, perr := b.PlaceLimitPostOnly(pc, productID, side, newLimitPx, newBase); perr == nil && strings.TrimSpace(newID) != "" {
									log.Printf("TRACE postonly.reprice side=%s old_id=%s new_id=%s limit=%.8f baseReq=%.8f",
										side, orderID, newID, newLimitPx, newBase)
									orderID = newID
									lastLimitPx = newLimitPx
									repriceCount++

									// --- PERSIST UPDATED PENDING STATE AFTER SUCCESSFUL REPRICE (REHYDRATE) ---
									t.apply(func(tt *Trader) {
										if side == SideBuy && tt.pendingBuy != nil {
											tt.pendingBuy.OrderID = newID
											tt.pendingBuy.LimitPx = newLimitPx
											tt.pendingBuy.BaseAtLimit = newBase
										} else if side == SideSell && tt.pendingSell != nil {
											tt.pendingSell.OrderID = newID
											tt.pendingSell.LimitPx = newLimitPx
											tt.pendingSell.BaseAtLimit = newBase
										}
										if pcopy != nil {
											pcopy.OrderID = newID
											pcopy.LimitPx = newLimitPx
											pcopy.BaseAtLimit = newBase
										}
										_ = tt.saveStateFrom(tt.snapshotStateLocked())
									})
									// -----------------------------------------------------------------------
								}
							}
						}
					}
					lastReprice = time.Now()
				}

				// Sleep-or-cancel wait
				select {
				case <-pc.Done():
					break poll
				case <-time.After(200 * time.Millisecond):
				}
			}

			// On timeout or cancellation, cancel any resting order.
			if filled == nil {
				_ = b.CancelOrder(pc, productID, orderID)
				log.Printf("TRACE postonly.timeout order_id=%s", orderID)
			}

			// Non-blocking completion signal.
			select {
			case chOut <- OpenResult{Filled: filled != nil, Placed: filled, OrderID: orderID}:
			default:
			}

			// Do NOT clear pending here.
			// step() drain must consume OpenResult first so it can copy pending Reason,
			// ConfidenceMult, EntryAIMode, ProfitGateUSD, RefundPortionUSD, etc.
			// Clearing pending here would create lots with missing entry metadata after restart.
			t.apply(func(tt *Trader) {
				err := tt.saveStateFrom(tt.snapshotStateLocked())
				if err != nil {
					log.Fatalf("TRACE Unable to save state after RehydratePending poller finish!!! side=%s Error: %v", side, err)
				}
			})
		}(p, *ch, *pctx)
	}

	// Resume both sides.
	rehydrateOne(&t.pendingBuy, &t.pendingBuyCh, &t.pendingBuyCtx, &t.pendingBuyCancel)
	rehydrateOne(&t.pendingSell, &t.pendingSellCh, &t.pendingSellCtx, &t.pendingSellCancel)
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

// --- NEW: side-aware lot closing (no global index) ---
func (t *Trader) closeLot(ctx context.Context, livePrice float64, side OrderSide, localIdx int, exitReason string, exitDecision string) (string, error) {
	book := t.book(side)
	lot := book.Lots[localIdx]
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}

	exitTime := time.Now().UTC()
	baseRequestedRaw := lot.SizeBase
	baseRequested := floorToStep(baseRequestedRaw, t.cfg.BaseStep)

	if baseRequested <= 0 {
		log.Printf("[CLOSE-SKIP] lotSide=%s closeSide=%s baseRaw=%.8f baseRounded=%.8f step=%.8f reason=%s", lot.Side, closeSide, baseRequestedRaw, baseRequested, t.cfg.BaseStep, exitReason)
		return "", nil
	}

	quote := baseRequested * livePrice
	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}

	if quote < minNotional {
		log.Printf("[CLOSE-SKIP] lotSide=%s closeSide=%s base=%.8f livePrice=%.2f notional=%.2f < min %.2f; deferring", lot.Side, closeSide, baseRequested, livePrice, quote, minNotional)
		return fmt.Sprintf("EXIT-SKIP %s side=%s→%s notional=%.2f < min=%.2f reason=%s", exitTime.Format(time.RFC3339), lot.Side, closeSide, quote, minNotional, exitReason), nil
	}

	isL2DeepLoss := exitReason == "threshold_stop_loss" && strings.Contains(exitDecision, "L2_DEEP_LOSS")
	usePendingMakerExit := lot.ExitMode == ExitModeScalpFixedTP && !isL2DeepLoss && t.cfg.LimitTimeoutSec > 0

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

	if !t.cfg.DryRun {
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

				log.Printf(
					"TRACE pending_exit.maker_px side=%s entry_id=%s take=%.8f live=%.8f maker_px=%.8f",
					lot.Side,
					lot.EntryOrderID,
					lot.Take,
					livePrice,
					limitPx,
				)

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

			if err != nil {
				log.Printf("TRACE pending_exit.start_failed side=%s entry_id=%s err=%v", lot.Side, lot.EntryOrderID, err)
				return "", nil
			}

			return fmt.Sprintf("PENDING_EXIT %s side=%s entry_id=%s limit=%.2f base=%.8f reason=%s", exitTime.Format(time.RFC3339), lot.Side, lot.EntryOrderID, limitPx, baseRequested, exitReason), nil
		}

		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote)

		log.Printf("TRACE order.close request side=%s baseReq=%.8f quoteEst=%.2f priceSnap=%.8f", closeSide, baseRequested, quote, livePrice)
		log.Printf("[KPI] taker.exit side=%s base=%.8f quote_est=%.2f reason=market_now", closeSide, baseRequested, quote)

		if err != nil {
			if t.cfg.UseDirectSlack {
				postSlack(fmt.Sprintf("ERR step: %v", err))
			}
			t.mu.Lock()
			return "", fmt.Errorf("close order failed: %w", err)
		}

		if placed != nil {
			log.Printf("TRACE order.close placed price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f", placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
		}
	}

	t.mu.Lock()

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

		const tol = 1e-9
		if baseFilled+tol < baseRequested {
			log.Printf("[WARN] partial fill (exit): requested_base=%.8f filled_base=%.8f (%.2f%%)", baseRequested, baseFilled, 100.0*(baseFilled/baseRequested))
			log.Printf("TRACE fill.exit partial requested=%.8f filled=%.8f", baseRequested, baseFilled)
		}
	}

	return t.applyFilledExitLocked(livePrice, priceExec, baseRequested, baseFilled, side, localIdx, exitReason, exitDecision, exitTime, placedOrderID(placed), commissionUSD, minNotional, wasNewest)
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

	log.Printf("TRACE exit.classify side=%s kind=%s reason=%s open=%.8f exec=%.8f baseFilled=%.8f rawPL=%.6f entryFee=%.6f exitFee=%.6f finalPL=%.6f", lot.Side, kind, exitReason, lot.OpenPrice, priceExec, baseFilled, rawPL, entryPortion, exitFee, pl)

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
		Reason:           exitReason + " | exitReason{" + exitDecision + "}  ||  openReason{" + lot.Reason + "}",
		ExitMode:         lot.ExitMode,
		WasRunner:        removedWasRunner,
		RefundPortionUSD: lot.RefundPortionUSD,
		EntryOrderID:     lot.EntryOrderID,
		ExitOrderID:      exitOrderID,
	}

	t.lastExits = append(t.lastExits, rec)

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
		lot.OpenNotionalUSD = lot.SizeBase * lot.OpenPrice
		if priceExec > 0 && minNotional > 0 {
			t.consolidateDust(book, priceExec, minNotional)
		}

		msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)", exitTime.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, entryPortion+exitFee)
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

	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)", exitTime.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, entryPortion+exitFee)
	if t.cfg.UseDirectSlack {
		postSlack(msg)
	}
	_ = t.saveStateNoLock()
	return msg, nil
}

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
	}

	t.pendingExits[oid] = p

	log.Printf("TRACE pending_exit.register exit_id=%s pending=%d", oid, len(t.pendingExits))
	log.Printf("TRACE pending_exit.start side=%s exit_id=%s entry_id=%s limit=%.8f base=%.8f reason=%s", p.Side, p.OrderID, p.EntryOrderID, p.LimitPx, p.BaseRequested, p.ExitReason)

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
		case t.pendingExitCh <- ExitResult{
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
			log.Printf(
				"TRACE pending_exit.poll.tick side=%s exit_id=%s entry_id=%s status=%s price=%.8f base=%.8f quote=%.2f fee=%.6f sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
				p.Side,
				orderID,
				p.EntryOrderID,
				status,
				ord.Price,
				ord.BaseSize,
				ord.QuoteSpent,
				ord.CommissionUSD,
				sessBase,
				sessQuote,
				sessFee,
			)

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

							log.Printf(
								"TRACE pending_exit.reprice side=%s old_exit_id=%s new_exit_id=%s entry_id=%s limit=%.8f base=%.8f count=%d",
								p.Side,
								oldID,
								newID,
								p.EntryOrderID,
								newLimitPx,
								newBase,
								repriceCount,
							)
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

	log.Printf(
		"TRACE pending_exit.timeout_cancel exit_id=%s entry_id=%s sess_base=%.8f sess_quote=%.2f sess_fee=%.6f",
		orderID,
		p.EntryOrderID,
		sessBase,
		sessQuote,
		sessFee,
	)

	emit(orderID)
}

func (t *Trader) drainPendingExits(ctx context.Context, candles []Candle, livePrice float64) {
	for {
		select {
		case res := <-t.pendingExitCh:
			t.applyPendingExitResult(ctx, candles, livePrice, res)
		default:
			return
		}
	}
}

func (t *Trader) applyPendingExitResult(ctx context.Context, candles []Candle, livePrice float64, res ExitResult) {
	_ = ctx
	_ = candles

	p := res.Pending
	if p == nil {
		log.Printf("TRACE pending_exit.apply_skip reason=nil_pending order_id=%s", res.OrderID)
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
		log.Printf("TRACE pending_exit.apply_skip reason=lot_not_found order_id=%s entry_id=%s", orderID, p.EntryOrderID)
		_ = t.saveStateNoLock()
		return
	}

	lot.FixedTPOrderID = ""

	if !res.Filled || res.Placed == nil {
		delete(t.pendingExits, orderID)
		log.Printf("TRACE pending_exit.unfilled order_id=%s entry_id=%s reason=%s", orderID, p.EntryOrderID, p.ExitReason)
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
		log.Printf("TRACE pending_exit.apply_skip reason=bad_base_requested order_id=%s", orderID)
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
		log.Printf("TRACE pending_exit.partial order_id=%s requested=%.8f filled=%.8f", orderID, baseRequested, baseFilled)
	}

	commissionUSD := 0.0
	if placed.CommissionUSD > 0 {
		commissionUSD = placed.CommissionUSD
	}

	wasNewest := localIdx == len(book.Lots)-1

	msg, err := t.applyFilledExitLocked(livePrice, priceExec, baseRequested, baseFilled, p.Side, localIdx, p.ExitReason, p.ExitDecision, exitTime, orderID, commissionUSD, minNotional, wasNewest)
	if err != nil {
		log.Printf("TRACE pending_exit.apply_error order_id=%s err=%v", orderID, err)
		_ = t.saveStateNoLock()
		return
	}

	delete(t.pendingExits, orderID)
	_ = t.saveStateNoLock()

	log.Printf("TRACE pending_exit.applied order_id=%s entry_id=%s msg=%s", orderID, p.EntryOrderID, msg)
}
