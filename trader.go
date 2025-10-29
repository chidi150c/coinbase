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
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---- Position & Trader ----

type ExitMode string

const (
	ExitModeRunnerTrailing ExitMode = "RunnerTrailing"
	ExitModeScalpTrailing  ExitMode = "ScalpTrailing"
	ExitModeScalpFixedTP   ExitMode = "ScalpFixedTP"
)

type Position struct {
	OpenPrice float64
	Side      OrderSide
	SizeBase  float64
	Take      float64
	OpenTime  time.Time
	// --- NEW: record entry fee for later P/L adjustment ---
	EntryFee float64
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
	ExitMode         ExitMode `json:"exit_mode,omitempty"`        // RunnerTrailing | ScalpTrailing | ScalpFixedTP
	Version          int      `json:"version"`
	FixedTPWorking   bool     `json:"-"` // internal flag: emulate a posted TP (re-post each tick while gate holds)

	TrailActivateGateUSD float64 `json:"activate_gate_usd"` // from TRAIL_ACTIVATE_USD (runner/scalp)
	TrailDistancePct     float64 `json:"distance_pct"`      // from TRAIL_DISTANCE_PCT (runner/scalp)

	// --- NEW: track maker-first TP exit order id (post-only limit attempt) ---
	FixedTPOrderID string `json:"-"`

	// --- NEW: stable lot identifier & entry order id (persisted) ---
	LotID        int    `json:"lot_id,omitempty"`
	EntryOrderID string `json:"entry_order_id,omitempty"`
}

// --- NEW: per-side book (authoritative store) ---
type SideBook struct {
	RunnerIDs []int       `json:"runner_ids,omitempty"`  // NEW: multiple runner indices (authoritative for multi-runner mode)
	Lots      []*Position `json:"lots"`
}

// BotState is the persistent snapshot of trader state.
// NOTE: Persist ONLY the SideBook-based schema now.
type BotState struct {
	EquityUSD      float64
	DailyStart     time.Time
	DailyPnL       float64
	Model          *AIMicroModel
	MdlExt         *ExtendedLogit
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

	// --- NEW: persist next lot sequence for stable LotIDs ---
	NextLotSeq int
}

// --- NEW (Phase 1): pending async maker-first open support ---
type PendingOpen struct {
	Side         OrderSide
	LimitPx      float64
	BaseAtLimit  float64
	Quote        float64
	Take         float64
	Reason       string
	ProductID    string
	CreatedAt    time.Time
	Deadline     time.Time
	EquityBuy    bool // whether this was equityTriggerBuy
	EquitySell   bool // whether this was equityTriggerSell
	// --- NEW: persisted working order id to allow rehydration ---
	OrderID string
	    // NEW: keep last few order IDs so we accept late fills after a cancel/reprice.
    History []string `json:"History,omitempty"` // capped (e.g., last 5)
    // NEW: accumulate fills across reprices
    AccumBase   float64 // sum of executed base over all prior order IDs
    AccumQuote  float64 // sum of executed quote
    AccumFeeUSD float64 // sum of fees (if provided)
}

type OpenResult struct {
	Filled  bool
	Placed  *PlacedOrder
	Err     error
	OrderID string
}

type Trader struct {
	cfg    Config
	broker Broker
	model  *AIMicroModel
	pos    *Position // kept for backward compatibility with earlier logic (represents last lot in aggregate)
	// lots      []*Position // legacy aggregate view (derived from books; do not mutate directly)
	mu        sync.RWMutex
	equityUSD float64

	// NEW (minimal): optional extended head passed through to decide(); nil if unused.
	mdlExt *ExtendedLogit

	// NEW: path to persisted state file
	stateFile string

	// NEW: track last model fit time for walk-forward
	lastFit time.Time

	// NEW: per-side books (authoritative)
	books map[OrderSide]*SideBook

	// NEW: index of the designated runner lot in legacy aggregate (-1 if none). Derived from books.
	// runnerIdx int

	// --- NEW: side-aware pyramiding state (kept in-memory; mirrored to legacy fields for logs) ---
	lastAddBuy      time.Time
	lastAddSell     time.Time
	winLowBuy       float64
	winHighSell     float64
	latchedGateBuy  float64
	latchedGateSell float64

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

	// --- NEW (Phase 2): recheck flags for market fallback gating ---
	pendingRecheckBuy  bool
	pendingRecheckSell bool

	// --- NEW: next lot sequence counter for stable LotIDs ---
	NextLotSeq int

	// --- NEW: centralized state manager channel ---
	stateApplyCh chan func(*Trader)
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,
		// runnerIdx:  -1,
		books: map[OrderSide]*SideBook{
			SideBuy:  {RunnerIDs: []int{}, Lots: nil},
			SideSell: {RunnerIDs: []int{}, Lots: nil},
		},
		NextLotSeq:   1,
		stateApplyCh: make(chan func(*Trader), 128),
	}

	// Start centralized state manager goroutine
	go func() {
		for fn := range t.stateApplyCh {
			t.mu.Lock()
			defer t.unlockSafe()
			fn(t)
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

	// Initialize legacy aggregate view for logs/compat.
	// t.refreshAggregateFromBooks()
	return t
}

// ExitRecord captures a compact snapshot for an exited lot.
type ExitRecord struct {
	Time        time.Time `json:"time"`
	Side        OrderSide `json:"side"`
	OpenPrice   float64   `json:"open_price"`
	ClosePrice  float64   `json:"close_price"`
	SizeBase    float64   `json:"size_base"`
	EntryFeeUSD float64   `json:"entry_fee_usd"`
	ExitFeeUSD  float64   `json:"exit_fee_usd"`
	PNLUSD      float64   `json:"pnl_usd"`
	Reason      string    `json:"reason"`
	ExitMode    ExitMode  `json:"exit_mode,omitempty"`
	WasRunner   bool      `json:"was_runner"`
	// --- NEW: identifiers for traceability ---
	LotID        int    `json:"lot_id,omitempty"`
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

	// update the metric with same naming style
	mtxPnL.Set(v)
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

// NEW (minimal): allow live loop to inject/refresh the optional extended model.
func (t *Trader) SetExtendedModel(m *ExtendedLogit) {
	t.mu.Lock()
	t.mdlExt = m
	t.mu.Unlock()
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

// Cap concurrent lots (env-tunable). Default is effectively "no cap".
func maxConcurrentLots() int {
	n := getEnvInt("MAX_CONCURRENT_LOTS", 1_000_000)
	if n < 1 {
		n = 1_000_000 // safety: never block adds due to bad input
	}
	return n
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
	case ExitModeScalpTrailing:
		actUSD = t.cfg.TrailActivateUSDScalp
		distPct = t.cfg.TrailDistancePctScalp
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

// --- NEW: side-aware lot closing (no global index) ---
func (t *Trader) closeLot(ctx context.Context, c []Candle, side OrderSide, localIdx int, exitReason string) (string, error) {
	book := t.book(side)
	price := c[len(c)-1].Close
	lot := book.Lots[localIdx]
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}
	baseRequested := lot.SizeBase
	quote := baseRequested * price

	// --- Effective min-notional: prefer cfg.MinNotional, fallback to cfg.OrderMinUSD ---
	minNotional := t.cfg.MinNotional
	if minNotional <= 0 {
		minNotional = t.cfg.OrderMinUSD
	}

	// --- Guard: skip close if quote is below our policy floor (exchange-agnostic) ---
	notional := quote
	if notional < minNotional {
		log.Printf("[CLOSE-SKIP] lotSide=%s closeSide=%s base=%.8f price=%.2f notional=%.2f < ORDER_MIN_USD %.2f; deferring",
			lot.Side, closeSide, baseRequested, price, notional, minNotional)
		msg := fmt.Sprintf("EXIT-SKIP %s side=%s→%s notional=%.2f < min=%.2f reason=%s",
			c[len(c)-1].Time.Format(time.RFC3339), lot.Side, closeSide, notional, minNotional, exitReason)
		return msg, nil
	}

	// --- NEW: maker-first post-only limit attempt for ScalpFixedTP exits ---
	wantLimitExit := (lot.ExitMode == ExitModeScalpFixedTP && t.cfg.LimitTimeoutSec > 0 && lot.Take > 0)

	// unlock for I/O
	t.mu.Unlock()
	var placed *PlacedOrder
	// --- locals to capture limit attempt state while unlocked ---
	var exitOrderID string
	var exitLimitPlaced bool
	var exitLimitFilled bool

	if !t.cfg.DryRun {
		if wantLimitExit {
			limitPx := lot.Take
			baseAtLimit := baseRequested
			// best-effort maker-first exit
			oid, err := t.broker.PlaceLimitPostOnly(ctx, t.cfg.ProductID, closeSide, limitPx, baseAtLimit)
			if err == nil && strings.TrimSpace(oid) != "" {
				exitOrderID = oid
				exitLimitPlaced = true
				deadline := time.Now().Add(time.Duration(t.cfg.LimitTimeoutSec) * time.Second)
				for time.Now().Before(deadline) {
					ord, gErr := t.broker.GetOrder(ctx, t.cfg.ProductID, oid)
					if gErr == nil && ord != nil && (ord.BaseSize > 0 || ord.QuoteSpent > 0) {
						placed = ord
						exitLimitFilled = true
						log.Printf("TRACE postonly.exit.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
							oid, placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
						mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
				if !exitLimitFilled {
					_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, oid)
					log.Printf("TRACE postonly.exit.timeout fallback=market order_id=%s", oid)
				}
			} else if err != nil {
				log.Printf("TRACE postonly.exit.error fallback=market err=%v", err)
			}
		}

		// If maker exit did not fill, fall back to market close (baseline behavior).
		if placed == nil {
			// TODO: remove TRACE
			log.Printf("TRACE order.close request side=%s baseReq=%.8f quoteEst=%.2f priceSnap=%.8f", closeSide, baseRequested, quote, price)
			var err error
			placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote)
			if err != nil {
				if t.cfg.Extended().UseDirectSlack {
					postSlack(fmt.Sprintf("ERR step: %v", err))
				}
				// Re-lock before returning so caller's Unlock matches.
				t.mu.Lock()
				return "", fmt.Errorf("close order failed: %w", err)
			}
			// TODO: remove TRACE
			if placed != nil {
				log.Printf("TRACE order.close placed price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
					placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
			}
			mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
		}
	}
	// re-lock
	t.mu.Lock()

	// --- NEW: attach/clear FixedTPOrderID tracking on the lot (only for the TP limit attempt) ---
	if wantLimitExit {
		if exitLimitPlaced {
			lot.FixedTPOrderID = exitOrderID
		}
		if exitLimitPlaced && !exitLimitFilled {
			lot.FixedTPOrderID = ""
		}
	}

	// --- NEW: check if the lot being closed is the most recent add for that side ---
	wasNewest := (localIdx == len(book.Lots)-1)

	// --- MINIMAL CHANGE: use actual filled size/price if available ---
	priceExec := c[len(c)-1].Close
	baseFilled := baseRequested
	if placed != nil {
		if placed.Price > 0 {
			priceExec = placed.Price
		}
		if placed.BaseSize > 0 {
			baseFilled = placed.BaseSize
		}
		// Log WARN on partial fill (filled < requested) with a small tolerance.
		const tol = 1e-9
		if baseFilled+tol < baseRequested {
			log.Printf("[WARN] partial fill (exit): requested_base=%.8f filled_base=%.8f (%.2f%%)",
				baseRequested, baseFilled, 100.0*(baseFilled/baseRequested))
			// TODO: remove TRACE
			log.Printf("TRACE fill.exit partial requested=%.8f filled=%.8f", baseRequested, baseFilled)
		}
	}
	// refresh price snapshot (best-effort) if no execution price was available
	if placed == nil || placed.Price <= 0 {
		priceExec = c[len(c)-1].Close
	}

	// --- Phase 3: pro-rate entry fee for the exited portion ---
	entryPortion := 0.0
	if baseRequested > 0 {
		entryPortion = lot.EntryFee * (baseFilled / baseRequested)
	}

	// compute P/L using actual fill size and execution price
	pl := (priceExec - lot.OpenPrice) * baseFilled
	if lot.Side == SideSell {
		pl = (lot.OpenPrice - priceExec) * baseFilled
	}

	// apply exit fee; prefer broker-provided commission if present ---
	quoteExec := baseFilled * priceExec
	feeRate := t.cfg.FeeRatePct
	exitFee := quoteExec * (feeRate / 100.0)
	if placed != nil {
		if placed.CommissionUSD > 0 {
			exitFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (exit); falling back to FEE_RATE_PCT=%.4f%%", feeRate)
		}
	}
	pl -= entryPortion // subtract only the proportional entry fee for the exited size
	pl -= exitFee      // subtract exit fee now

	// --- TRACE: classification breakdown ---
	rawPL := func() float64 {
		// raw (price move only), before fees:
		if lot.Side == SideBuy {
			return (priceExec - lot.OpenPrice) * baseFilled
		}
		return (lot.OpenPrice - priceExec) * baseFilled
	}()
	kind := "scalp"
	for _, rid := range book.RunnerIDs {
		if rid == localIdx {
			kind = "runner"
			break
		}
	}
	log.Printf("TRACE exit.classify side=%s kind=%s reason=%s open=%.8f exec=%.8f baseFilled=%.8f rawPL=%.6f entryFee=%.6f exitFee=%.6f finalPL=%.6f",
		lot.Side, kind, exitReason, lot.OpenPrice, priceExec, baseFilled, rawPL, entryPortion, exitFee, pl)

	t.dailyPnL += pl
	t.equityUSD += pl

	
	// Track if we removed the runner and adjust book.RunnerID accordingly after removal.
	removedWasRunner := false
	// NEW: also consider multi-runner slice
	if len(book.RunnerIDs) > 0 {
		for _, rid := range book.RunnerIDs {
			if rid == localIdx {
				removedWasRunner = true
				break
			}
		}
	}

	// Build ExitRecord (use exec price/size actually filled)
	rec := ExitRecord{
		Time:        c[len(c)-1].Time,
		Side:        lot.Side,
		OpenPrice:   lot.OpenPrice,
		ClosePrice:  priceExec,
		SizeBase:    baseFilled,
		EntryFeeUSD: entryPortion, // Phase 3: record proportional entry fee
		ExitFeeUSD:  exitFee,
		PNLUSD:      pl,
		Reason:      exitReason,
		ExitMode:    lot.ExitMode,
		WasRunner:   removedWasRunner,
		// NEW identifiers
		LotID:        lot.LotID,
		EntryOrderID: lot.EntryOrderID,
		ExitOrderID:  exitOrderID,
	}

	// Append with cap semantics (ring buffer behavior)
	t.lastExits = append(t.lastExits, rec)
	if capN := func() int {
		if t.cfg.ExitHistorySize > 0 {
			return t.cfg.ExitHistorySize
		}
		return 8
	}(); len(t.lastExits) > capN {
		t.lastExits = t.lastExits[len(t.lastExits)-capN:]
	}

	// --- NEW: increment win/loss trades ---
	if pl >= 0 {
		mtxTrades.WithLabelValues("win").Inc()
	} else {
		mtxTrades.WithLabelValues("loss").Inc()
	}

	// Normalize/sanitize reason and side label for metrics
	reasonLbl := exitReason
	sideLbl := "buy"
	if lot.Side == SideSell {
		sideLbl = "sell"
	}
	mtxExitReasons.WithLabelValues(reasonLbl, sideLbl).Inc()

	// --- Phase 3: handle partial vs full exit ---
	const tolExit = 1e-9
	isPartial := baseFilled+tolExit < baseRequested

	if isPartial {
		// Reduce remaining lot size and carry forward remaining entry fee
		lot.SizeBase = baseRequested - baseFilled
		lot.EntryFee = lot.EntryFee - entryPortion
		if lot.EntryFee < 0 {
			lot.EntryFee = 0 // safety clamp
		}

		// Do NOT remove the lot; do NOT shift RunnerID; do NOT re-anchor timers/stages
		msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)",
			c[len(c)-1].Time.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, entryPortion+exitFee)
		if t.cfg.Extended().UseDirectSlack {
			postSlack(msg)
		}
		// persist updated lot state
		if err := t.saveStateNoLock(); err != nil {
			log.Printf("[WARN] saveState: %v", err)
			log.Printf("TRACE state.save error=%v", err)
		}
		return msg, nil
	}

	// --- FULL EXIT path (unchanged semantics aside from entry fee portion already applied) ---

	// remove lot at localIdx
	book.Lots = append(book.Lots[:localIdx], book.Lots[localIdx+1:]...)

	// Update multi-runner slice: drop exited idx and shift higher ones down
	if len(book.RunnerIDs) > 0 {
		out := book.RunnerIDs[:0] // reuse capacity
		for _, rid := range book.RunnerIDs {
			if rid == localIdx {
				// exited lot was a runner → drop it
				continue
			}
			if rid > localIdx {
				rid-- // compact indices after removal
			}
			out = append(out, rid)
		}
		// reset to a clean slice (avoid keeping stale tail)
		book.RunnerIDs = append([]int(nil), out...)
	}

	// NOTE: Do NOT auto-promote any lot to runner.
	// If RunnerIDs becomes empty, the side simply has no runners now.

	// --- if the closed lot was the most recent add FOR THAT SIDE, re-anchor pyramiding timers/state ---
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

	// --- NEW: Back-step & Reset for equity stages (side-aware) ---
	if removedWasRunner {
		if lot.Side == SideBuy {
			if t.equityStageBuy > 0 {
				t.equityStageBuy--
			}
		} else {
			if t.equityStageSell > 0 {
				t.equityStageSell--
			}
		}
	}
	if len(book.Lots) == 0 {
		if lot.Side == SideBuy {
			t.equityStageBuy = 0
		} else {
			t.equityStageSell = 0
		}
	}

	// Include reason in message for operator visibility
	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, entryPortion+exitFee)
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveStateNoLock(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		// TODO: remove TRACE
		log.Printf("TRACE state.save error=%v", err)
	}

	_ = removedWasRunner // kept to emphasize runner path; no extra logs.
	return msg, nil
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
		MdlExt:         t.mdlExt,
		WalkForwardMin: t.cfg.Extended().WalkForwardMin,
		LastFit:        t.lastFit,

		BookBuy:  *t.book(SideBuy),
		BookSell: *t.book(SideSell),

		LastAddBuy:      t.lastAddBuy,
		LastAddSell:     t.lastAddSell,
		WinLowBuy:       t.winLowBuy,
		WinHighSell:     t.winHighSell,
		LatchedGateBuy:  t.latchedGateBuy,
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

		// NEW: persist next lot sequence
		NextLotSeq: t.NextLotSeq,
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
	if st.MdlExt != nil {
		t.mdlExt = st.MdlExt
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
				n := i + 1 // 1-based scalp index
				if n >= 1 && n <= 4 {
					// Scalp 1..4 → trailing (scalp params)
					if lot.TrailDistancePct == 0 {
						lot.TrailDistancePct = t.cfg.TrailDistancePctScalp
					}
					if lot.TrailActivateGateUSD == 0 {
						lot.TrailActivateGateUSD = t.cfg.TrailActivateUSDScalp
					}
					if lot.ExitMode == "" {
						lot.ExitMode = ExitModeScalpTrailing
					}
				} else {
					// Scalp >4 → fixed TP
					if lot.ExitMode == "" {
						lot.ExitMode = ExitModeScalpFixedTP
					}
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

	// NextLotSeq (recompute if absent)
	t.NextLotSeq = st.NextLotSeq
	if t.NextLotSeq <= 0 {
		maxID := 0
		for _, side := range []OrderSide{SideBuy, SideSell} {
			for _, lot := range t.book(side).Lots {
				if lot != nil && lot.LotID > maxID {
					maxID = lot.LotID
				}
			}
		}
		t.NextLotSeq = maxID + 1
		if t.NextLotSeq <= 0 {
			t.NextLotSeq = 1
		}
	}

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
// 2) optional walk-forward cadence satisfied (cfg.Extended().WalkForwardMin).
// This is a guard only; it performs no fitting and emits no logs/metrics.
func (t *Trader) shouldRefit(historyLen int) bool {
	if historyLen < t.cfg.MaxHistoryCandles {
		return false
	}
	min := t.cfg.Extended().WalkForwardMin
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
					*pend = nil
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
					mtxOrders.WithLabelValues("live", string(side)).Inc()
					mtxTrades.WithLabelValues("open").Inc()
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

			// Clear pending and persist on exit using centralized manager
			t.apply(func(tt *Trader) {
				if side == SideBuy {
					tt.pendingBuy = nil
					tt.pendingBuyCtx = nil
					tt.pendingBuyCancel = nil
				} else {
					tt.pendingSell = nil
					tt.pendingSellCtx = nil
					tt.pendingSellCancel = nil
				}
				err := tt.saveStateFrom(tt.snapshotStateLocked())
				if err !=nil{
					log.Fatalf("TRACE Unable to save state after order cancelling in RehydratePending!!! side=%s Error: %v", side, err)
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
