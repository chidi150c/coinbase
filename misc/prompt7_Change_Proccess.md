Generate a full copy of {{
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
}

// --- NEW: per-side book (authoritative store) ---
type SideBook struct {
	RunnerID int         `json:"runner_id"`
	Lots     []*Position `json:"lots"`
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
	mu        sync.Mutex
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
			SideBuy:  {RunnerID: -1, Lots: nil},
			SideSell: {RunnerID: -1, Lots: nil},
		},
	}
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
}

func (t *Trader) EquityUSD() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.equityUSD
}

// SetEquityUSD safely updates trader equity and the equity metric.
func (t *Trader) SetEquityUSD(v float64) {
	t.mu.Lock()
	t.equityUSD = v
	t.mu.Unlock()

	// update the metric with same naming style
	mtxPnL.Set(v)
	// persist new state (no-op if disabled)
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		// TODO: remove TRACE
		log.Printf("TRACE state.save error=%v", err)
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
		if err := t.saveState(); err != nil {
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
		b = &SideBook{RunnerID: -1}
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
	if book.RunnerID == localIdx {
		kind = "runner"
	}
	log.Printf("TRACE exit.classify side=%s kind=%s reason=%s open=%.8f exec=%.8f baseFilled=%.8f rawPL=%.6f entryFee=%.6f exitFee=%.6f finalPL=%.6f",
		lot.Side, kind, exitReason, lot.OpenPrice, priceExec, baseFilled, rawPL, entryPortion, exitFee, pl)

	t.dailyPnL += pl
	t.equityUSD += pl

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
		WasRunner:   (localIdx == book.RunnerID),
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
		if err := t.saveState(); err != nil {
			log.Printf("[WARN] saveState: %v", err)
			log.Printf("TRACE state.save error=%v", err)
		}
		return msg, nil
	}

	// --- FULL EXIT path (unchanged semantics aside from entry fee portion already applied) ---

	// Track if we removed the runner and adjust book.RunnerID accordingly after removal.
	removedWasRunner := (localIdx == book.RunnerID)

	// remove lot at localIdx
	book.Lots = append(book.Lots[:localIdx], book.Lots[localIdx+1:]...)

	// shift RunnerID if needed
	if book.RunnerID >= 0 {
		if localIdx < book.RunnerID {
			book.RunnerID-- // slice shifted left
		} else if localIdx == book.RunnerID {
			// runner removed; promote the NEWEST remaining lot (if any) to runner
			if len(book.Lots) > 0 {
				book.RunnerID = len(book.Lots) - 1
				// reset trailing fields for the newly promoted runner
				nr := book.Lots[book.RunnerID]
				nr.TrailActive = false
				nr.TrailPeak = nr.OpenPrice
				nr.TrailStop = 0
				// also re-apply runner targets (keeps existing behavior)
				t.applyRunnerTargets(nr)
			} else {
				book.RunnerID = -1
			}
		}
	}

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
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		// TODO: remove TRACE
		log.Printf("TRACE state.save error=%v", err)
	}

	_ = removedWasRunner // kept to emphasize runner path; no extra logs.
	return msg, nil
}

// ---- Core tick ----

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
			if t.pendingBuy != nil && res.Filled && res.Placed != nil {
				side := SideBuy
				book := t.book(side)
				priceToUse := res.Placed.Price
				baseToUse := res.Placed.BaseSize
				actualQuote := res.Placed.QuoteSpent
				entryFee := res.Placed.CommissionUSD
				feeRate := t.cfg.FeeRatePct
				if entryFee <= 0 {
					entryFee = actualQuote * (feeRate / 100.0)
				}
				newLot := &Position{
					OpenPrice: priceToUse,
					Side:      side,
					SizeBase:  baseToUse,
					OpenTime:  now,
					EntryFee:  entryFee,
					Reason:    t.pendingBuy.Reason,
					Take:      t.pendingBuy.Take,
					Version:   1,
				}
				book.Lots = append(book.Lots, newLot)
				t.lastAddBuy = wallNow
				t.winLowBuy = priceToUse
				t.latchedGateBuy = 0
				old := t.lastAddEquityBuy
				t.lastAddEquityBuy = t.equityUSD
				log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f", side, old, t.lastAddEquityBuy)
				if t.pendingBuy.EquityBuy {
					book.RunnerID = len(book.Lots) - 1
					r := book.Lots[book.RunnerID]
					r.TrailActive = false
					r.TrailPeak = r.OpenPrice
					r.TrailStop = 0
					t.applyRunnerTargets(r)
					log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", book.RunnerID, side, r.OpenPrice, r.Take)
				}
				msg := fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
					side, actualQuote, newLot.Take, entryFee, newLot.Reason, "async postonly filled")
				if t.cfg.Extended().UseDirectSlack {
					postSlack(msg)
				}
				if err := t.saveState(); err != nil {
					log.Printf("[WARN] saveState: %v", err)
					log.Printf("TRACE state.save error=%v", err)
				}
			} else {
				if t.pendingBuy != nil {
					t.pendingRecheckBuy = true
				}
			}
			if t.pendingBuyCancel != nil {
				t.pendingBuyCancel()
			}
			t.pendingBuy = nil
			t.pendingBuyCtx = nil
			t.pendingBuyCancel = nil
		default:
		}
	}
	if t.pendingSellCh != nil {
		select {
		case res := <-t.pendingSellCh:
			if t.pendingSell != nil && res.Filled && res.Placed != nil {
				side := SideSell
				book := t.book(side)
				priceToUse := res.Placed.Price
				baseToUse := res.Placed.BaseSize
				actualQuote := res.Placed.QuoteSpent
				entryFee := res.Placed.CommissionUSD
				feeRate := t.cfg.FeeRatePct
				if entryFee <= 0 {
					entryFee = actualQuote * (feeRate / 100.0)
				}
				newLot := &Position{
					OpenPrice: priceToUse,
					Side:      side,
					SizeBase:  baseToUse,
					OpenTime:  now,
					EntryFee:  entryFee,
					Reason:    t.pendingSell.Reason,
					Take:      t.pendingSell.Take,
					Version:   1,
				}
				book.Lots = append(book.Lots, newLot)
				t.lastAddSell = wallNow
				t.winHighSell = priceToUse
				t.latchedGateSell = 0
				old := t.lastAddEquitySell
				t.lastAddEquitySell = t.equityUSD
				log.Printf("TRACE equity.baseline.set side=%s old=%.2f new=%.2f", side, old, t.lastAddEquitySell)
				if t.pendingSell.EquitySell {
					book.RunnerID = len(book.Lots) - 1
					r := book.Lots[book.RunnerID]
					r.TrailActive = false
					r.TrailPeak = r.OpenPrice
					r.TrailStop = 0
					t.applyRunnerTargets(r)
					log.Printf("TRACE runner.assign idx=%d side=%s open=%.8f take=%.8f", book.RunnerID, side, r.OpenPrice, r.Take)
				}
				msg := fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
					side, actualQuote, newLot.Take, entryFee, newLot.Reason, "async postonly filled")
				if t.cfg.Extended().UseDirectSlack {
					postSlack(msg)
				}
				if err := t.saveState(); err != nil {
					log.Printf("[WARN] saveState: %v", err)
					log.Printf("TRACE state.save error=%v", err)
				}
			} else {
				if t.pendingSell != nil {
					t.pendingRecheckSell = true
				}
			}
			if t.pendingSellCancel != nil {
				t.pendingSellCancel()
			}
			t.pendingSell = nil
			t.pendingSellCtx = nil
			t.pendingSellCancel = nil
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

			// 1..4 → trailing (scalp); >4 → fixed TP
			n := idx + 1
			if n >= 1 && n <= 4 {
				// ScalpTrailing: Take = fee-aware activation price for scalp USD gate (preview only)
				lot.ExitMode = ExitModeScalpTrailing
				lot.TrailDistancePct = t.cfg.TrailDistancePctScalp
				lot.TrailActivateGateUSD = t.cfg.TrailActivateUSDScalp
				lot.Take = activationPrice(lot, lot.TrailActivateGateUSD, feeRatePct)
				if lot.TrailPeak == 0 {
					lot.TrailPeak = lot.OpenPrice
				}
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
	d := decide(c, t.model, t.mdlExt)
	totalLots := lsb + lss
	log.Printf("[DEBUG] Total Lots=%d, Decision=%s Reason = %s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v",
		totalLots, d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	// Determine the side and its book
	side := d.SignalToSide()
	book := t.book(side)

	// --- NEW (Phase 4): prevent duplicate opens while pending on this side (exits already ran) ---
	if (side == SideBuy && t.pendingBuy != nil) || (side == SideSell && t.pendingSell != nil) {
		t.mu.Unlock()
		return fmt.Sprintf("OPEN-PENDING side=%s", side), nil
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
					// TODO: remove TRACE
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
					// TODO: remove TRACE
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
			// TODO: remove TRACE
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
				// TODO: remove TRACE
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
			// TODO: remove TRACE
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
				// TODO: remove TRACE
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

	// Place live order without holding the lock.
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
		offsetBps := t.cfg.LimitPriceOffsetBps
		limitWait := t.cfg.LimitTimeoutSec
		spreadGate := t.cfg.SpreadMinBps // NOTE: no spread source via Broker; positive gate disables maker attempt
		wantLimit := strings.ToLower(strings.TrimSpace(t.cfg.OrderType)) == "limit" && offsetBps > 0 && limitWait > 0

		// --- NEW (Phase 4): maker-first routing via Broker when ORDER_TYPE=limit (async, per-side) ---
		if wantLimit {
			if spreadGate > 0 {
				log.Printf("TRACE postonly.skip reason=spread_gate_unavailable spread_min_bps=%.3f", spreadGate)
			} else {
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
						limitPx = math.Floor(limitPx/tick) * tick
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
								Reason:      "", // set later below
								ProductID:   t.cfg.ProductID,
								CreatedAt:   time.Now().UTC(),
								Deadline:    time.Now().Add(time.Duration(limitWait) * time.Second),
								EquityBuy:   equityTriggerBuy,
								EquitySell:  equityTriggerSell,
								OrderID:     orderID,
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
								Reason:      "", // set later below
								ProductID:   t.cfg.ProductID,
								CreatedAt:   time.Now().UTC(),
								Deadline:    time.Now().Add(time.Duration(limitWait) * time.Second),
								EquityBuy:   equityTriggerBuy,
								EquitySell:  equityTriggerSell,
								OrderID:     orderID,
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
							orderID := initOrderID
							lastLimitPx := initLimitPx
							initLimit := initLimitPx
							lastReprice := time.Now()

							// --- ENV GUARDRAILS (read once per poller) ---
							repriceEnabled := getEnvBool("REPRICE_ENABLE", true)
							repriceMaxCount := getEnvInt("REPRICE_MAX_COUNT", 10)
							repriceMaxDriftBps := getEnvFloat("REPRICE_MAX_DRIFT_BPS", 3.0) // 0 = unlimited
							repriceMinImproTicks := getEnvInt("REPRICE_MIN_IMPROV_TICKS", 1)
							if repriceMinImproTicks < 1 {
								repriceMinImproTicks = 1
							}
							repriceIntervalMs := getEnvInt("REPRICE_INTERVAL_MS", 1000)
							if repriceIntervalMs <= 0 {
								repriceIntervalMs = 450
							}
							repriceMinEdgeUSD := getEnvFloat("REPRICE_MIN_EDGE_USD", 0.0)

							var repriceCount int

						poll:
							for time.Now().Before(deadline) {
								// Check for cancellation before doing work
								select {
								case <-pctx.Done():
									break poll
								default:
								}

								// Check current order fill state first
								ord, gErr := t.broker.GetOrder(pctx, t.cfg.ProductID, orderID)
								if gErr == nil && ord != nil && (ord.BaseSize > 0 || ord.QuoteSpent > 0) {
									placed := ord
									log.Printf("TRACE postonly.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
										orderID, placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
									mtxOrders.WithLabelValues("live", string(side)).Inc()
									mtxTrades.WithLabelValues("open").Inc()

									// Non-blocking completion signal
									select {
									case ch <- OpenResult{Filled: true, Placed: placed, OrderID: orderID}:
									default:
									}

									// Clear pending and persist on exit
									t.mu.Lock()
									if side == SideBuy {
										t.pendingBuy = nil
										t.pendingBuyCtx = nil
										t.pendingBuyCancel = nil
									} else {
										t.pendingSell = nil
										t.pendingSellCtx = nil
										t.pendingSellCancel = nil
									}
									_ = t.saveState()
									t.mu.Unlock()
									return
								}

								// Reprice loop (guarded)
								if time.Since(lastReprice) >= time.Duration(repriceIntervalMs)*time.Millisecond {
									// Skip repricing entirely if disabled or max count reached
									if repriceEnabled && (repriceMaxCount <= 0 || repriceCount < repriceMaxCount) {
										// Fetch a fresh price snapshot
										var px float64
										ctxPx, cancelPx := context.WithTimeout(pctx, 1*time.Second)
										px, gErr = t.broker.GetNowPrice(ctxPx, t.cfg.ProductID)
										cancelPx()
										if gErr == nil && px > 0 {
											// Recompute target limit price using same offset
											newLimitPx := px
											if side == SideBuy {
												newLimitPx = px * (1.0 - offsetBps/10000.0)
											} else {
												newLimitPx = px * (1.0 + offsetBps/10000.0)
											}
											// Snap to tick if configured
											tick := t.cfg.PriceTick
											if tick > 0 {
												newLimitPx = math.Floor(newLimitPx/tick) * tick
											}

											// baseline: only reprice if snapped price changed
											shouldReprice := (tick > 0 && math.Abs(newLimitPx-lastLimitPx) >= tick) || (tick <= 0 && newLimitPx != lastLimitPx)

											// Guard: max drift from initial (bps)
											if shouldReprice && repriceMaxDriftBps > 0 {
												driftBps := math.Abs((newLimitPx-initLimit)/initLimit) * 10000.0
												if driftBps > repriceMaxDriftBps {
													shouldReprice = false
												}
											}

											// Guard: minimum improvement in ticks (directional)
											if shouldReprice && tick > 0 && repriceMinImproTicks > 1 {
												improveTicks := int(math.Abs(newLimitPx-lastLimitPx) / tick)
												// direction check: buy wants lower, sell wants higher
												if side == SideBuy {
													if !(newLimitPx < lastLimitPx && improveTicks >= repriceMinImproTicks) {
														shouldReprice = false
													}
												} else {
													if !(newLimitPx > lastLimitPx && improveTicks >= repriceMinImproTicks) {
														shouldReprice = false
													}
												}
											}

											// Recompute base from original quote for edge calculation & placement
											newBase := pend.BaseAtLimit
											if pend != nil && pend.Quote > 0 {
												newBase = pend.Quote / newLimitPx
											} else {
												newBase = initBaseAtLimit
											}
											// Snap base to step
											if t.cfg.BaseStep > 0 {
												newBase = math.Floor(newBase/t.cfg.BaseStep) * t.cfg.BaseStep
											}

											// Guard: min edge USD improvement
											if shouldReprice && repriceMinEdgeUSD > 0 && newBase > 0 {
												edgeUSD := math.Abs(newLimitPx-lastLimitPx) * newBase
												if edgeUSD < repriceMinEdgeUSD {
													shouldReprice = false
												}
											}

											// Ensure min-notional for the reprice candidate
											if shouldReprice && !(newBase > 0 && newBase*newLimitPx >= t.cfg.MinNotional) {
												shouldReprice = false
											}

											if shouldReprice {
												// Cancel current and re-place at the new price
												_ = t.broker.CancelOrder(pctx, t.cfg.ProductID, orderID)
												newID, perr := t.broker.PlaceLimitPostOnly(pctx, t.cfg.ProductID, side, newLimitPx, newBase)
												if perr == nil && strings.TrimSpace(newID) != "" {
													log.Printf("TRACE postonly.reprice side=%s old_id=%s new_id=%s limit=%.8f baseReq=%.8f",
														side, orderID, newID, newLimitPx, newBase)
													orderID = newID
													lastLimitPx = newLimitPx
													repriceCount++

													// --- PERSIST UPDATED PENDING STATE AFTER SUCCESSFUL REPRICE ---
													t.mu.Lock()
													if side == SideBuy && t.pendingBuy != nil {
														t.pendingBuy.OrderID = newID
														t.pendingBuy.LimitPx = newLimitPx
														t.pendingBuy.BaseAtLimit = newBase
													} else if side == SideSell && t.pendingSell != nil {
														t.pendingSell.OrderID = newID
														t.pendingSell.LimitPx = newLimitPx
														t.pendingSell.BaseAtLimit = newBase
													}
													if pend != nil {
														pend.OrderID = newID
														pend.LimitPx = newLimitPx
														pend.BaseAtLimit = newBase
													}
													_ = t.saveState()
													t.mu.Unlock()
													// -----------------------------------------------------------
												}
											}
										}
									}
									lastReprice = time.Now()
								}

								// Sleep-or-cancel wait
								select {
								case <-pctx.Done():
									break poll
								case <-time.After(200 * time.Millisecond):
								}
							}

							// On timeout or cancellation, cancel any resting order if still open.
							_ = t.broker.CancelOrder(pctx, t.cfg.ProductID, orderID)
							log.Printf("TRACE postonly.timeout order_id=%s", orderID)

							// Non-blocking completion signal (not filled)
							select {
							case ch <- OpenResult{Filled: false, Placed: nil, OrderID: orderID}:
							default:
							}

							// Clear pending and persist on exit
							t.mu.Lock()
							if side == SideBuy {
								t.pendingBuy = nil
								t.pendingBuyCtx = nil
								t.pendingBuyCancel = nil
							} else {
								t.pendingSell = nil
								t.pendingSellCtx = nil
								t.pendingSellCancel = nil
							}
							_ = t.saveState()
							t.mu.Unlock()
						}(orderID, side, time.Now().Add(time.Duration(limitWait)*time.Second), limitPx, baseAtLimit, pend, ch, pctxUse)

						// Build reason string now that all gates are known
						// (We set it after spawning to minimize time outside the lock.)
						t.mu.Lock()
						if side == SideBuy && t.pendingBuy != nil {
							if equityTriggerBuy && side == SideBuy {
								t.pendingBuy.Reason = fmt.Sprintf("EQUITY Trading: equityUSD=%.2f lastAddEquityBuy=%.2f pct_diff_buy=%.6f equitySpareQuote=%.2f",
									t.equityUSD, t.lastAddEquityBuy, t.equityUSD/t.lastAddEquityBuy, quote)
							} else {
								t.pendingBuy.Reason = fmt.Sprintf("async postonly")
							}
						}
						if side == SideSell && t.pendingSell != nil {
							if equityTriggerSell && side == SideSell {
								t.pendingSell.Reason = fmt.Sprintf("EQUITY Trading: equityUSD=%.2f lastAddEquitySell=%.2f pct_diff_sell=%.6f equitySpareBase=%.8f ",
									t.equityUSD, t.lastAddEquitySell, t.equityUSD/t.lastAddEquitySell, baseAtLimit)
							} else {
								t.pendingSell.Reason = fmt.Sprintf("async postonly")
							}
						}
						// persist pending state
						_ = t.saveState()
						t.mu.Unlock()

						// Re-lock and return early: do not fall back to market here.
						t.mu.Lock()
						return fmt.Sprintf("OPEN-PENDING side=%s", side), nil
					} else if err != nil {
						log.Printf("TRACE postonly.error fallback=market err=%v", err)
					}
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
				return "HOLD", nil
			}
			// TODO: remove TRACE
			log.Printf("TRACE order.open request side=%s quote=%.2f baseEst=%.8f priceSnap=%.8f take=%.8f",
				side, quote, base, price, take)
			var err error
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

	// --- NEW: side-biased Lot reason (without winLow) ---
	var gatesReason string
	// --- NEW: override sizing for equity scalp to use the entire spare base as the order size (SELL only) ---
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

	newLot := &Position{
		OpenPrice: priceToUse,
		Side:      side,
		SizeBase:  baseToUse,
		OpenTime:  now,
		EntryFee:  entryFee,
		Reason:    gatesReason, // side-biased; no winLow
		Take:      take,
		Version:   1,
	}
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
		msg = fmt.Sprintf("[LIVE ORDER] %s quote=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			side, actualQuote, newLot.Take, entryFee, newLot.Reason, d.Reason)
	}
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		// TODO: remove TRACE
		log.Printf("TRACE state.save error=%v", err)
	}
	t.mu.Unlock()
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

func (t *Trader) saveState() error {
	if t.stateFile == "" || !t.cfg.PersistState {
		return nil
	}
	// Build persisted books snapshot
	st := BotState{
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
	// Prefer configured/live equity rather than stale persisted equity when:
	//  - running in DRY_RUN / backtest, or
	//  - live-equity mode is enabled (we will rebase from Bridge when available).
	// This prevents negative/old EquityUSD from leaking into runs that don't want it.
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

	// Restore per-side books
	t.books[SideBuy] = &SideBook{RunnerID: st.BookBuy.RunnerID, Lots: st.BookBuy.Lots}
	t.books[SideSell] = &SideBook{RunnerID: st.BookSell.RunnerID, Lots: st.BookSell.Lots}
	// --- MIGRATION/BACKFILL for new trailing params on Position ---
	for _, side := range []OrderSide{SideBuy, SideSell} {
		book := t.book(side)
		for i, lot := range book.Lots {
			// Determine exit class to pick defaults
			if i == book.RunnerID {
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
					// trailing params not required for fixed-TP scalps
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

	// Restore equity stages (defaults to 0 if absent)
	t.equityStageBuy = st.EquityStageBuy
	t.equityStageSell = st.EquityStageSell

	// Restore pending and recheck flags (rehydration is performed by RehydratePending)
	t.pendingBuy = st.PendingBuy
	t.pendingSell = st.PendingSell
	t.pendingRecheckBuy = st.PendingRecheckBuy
	t.pendingRecheckSell = st.PendingRecheckSell

	// Initialize runner trailing baseline for current runners if not already set
	for _, side := range []OrderSide{SideBuy, SideSell} {
		book := t.book(side)
		if book.RunnerID >= 0 && book.RunnerID < len(book.Lots) {
			r := book.Lots[book.RunnerID]
			if r.TrailPeak == 0 {
				r.TrailPeak = r.OpenPrice
			}
		}
	}

	// --- Restart warmup for pyramiding decay/adverse tracking ---
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

	// Rebuild legacy aggregate view
	// t.refreshAggregateFromBooks()
	return nil
}

func (t *Trader) LastExits() []ExitRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
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
	defer t.mu.Unlock()

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
			_ = t.saveState() // best-effort
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
					_ = t.saveState()
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
			_ = t.saveState()
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
			repriceEnabled := getEnvBool("REPRICE_ENABLE", true)
			repriceMaxCount := getEnvInt("REPRICE_MAX_COUNT", 50)
			repriceMaxDriftBps := getEnvFloat("REPRICE_MAX_DRIFT_BPS", 0.0) // 0 = unlimited
			repriceMinImproTicks := getEnvInt("REPRICE_MIN_IMPROV_TICKS", 1)
			if repriceMinImproTicks < 1 {
				repriceMinImproTicks = 1
			}
			repriceIntervalMs := getEnvInt("REPRICE_INTERVAL_MS", 450)
			if repriceIntervalMs <= 0 {
				repriceIntervalMs = 450
			}
			repriceMinEdgeUSD := getEnvFloat("REPRICE_MIN_EDGE_USD", 0.0)

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
				if time.Since(lastReprice) >= time.Duration(repriceIntervalMs)*time.Millisecond {
					if repriceEnabled && (repriceMaxCount <= 0 || repriceCount < repriceMaxCount) {
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
							if shouldReprice && repriceMaxDriftBps > 0 {
								driftBps := math.Abs((newLimitPx-initLimit)/initLimit) * 10000.0
								if driftBps > repriceMaxDriftBps {
									shouldReprice = false
								}
							}

							// Guard: minimum improvement ticks (directional)
							if shouldReprice && tick > 0 && repriceMinImproTicks > 1 {
								improveTicks := int(math.Abs(newLimitPx-lastLimitPx) / tick)
								if side == SideBuy {
									if !(newLimitPx < lastLimitPx && improveTicks >= repriceMinImproTicks) {
										shouldReprice = false
									}
								} else {
									if !(newLimitPx > lastLimitPx && improveTicks >= repriceMinImproTicks) {
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
							if shouldReprice && repriceMinEdgeUSD > 0 && newBase > 0 {
								edgeUSD := math.Abs(newLimitPx-lastLimitPx) * newBase
								if edgeUSD < repriceMinEdgeUSD {
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
									t.mu.Lock()
									if side == SideBuy && t.pendingBuy != nil {
										t.pendingBuy.OrderID = newID
										t.pendingBuy.LimitPx = newLimitPx
										t.pendingBuy.BaseAtLimit = newBase
									} else if side == SideSell && t.pendingSell != nil {
										t.pendingSell.OrderID = newID
										t.pendingSell.LimitPx = newLimitPx
										t.pendingSell.BaseAtLimit = newBase
									}
									if pcopy != nil {
										pcopy.OrderID = newID
										pcopy.LimitPx = newLimitPx
										pcopy.BaseAtLimit = newBase
									}
									_ = t.saveState()
									t.mu.Unlock()
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

			// Clear pending and persist on exit
			t.mu.Lock()
			if side == SideBuy {
				t.pendingBuy = nil
				t.pendingBuyCtx = nil
				t.pendingBuyCancel = nil
			} else {
				t.pendingSell = nil
				t.pendingSellCtx = nil
				t.pendingSellCancel = nil
			}
			_ = t.saveState()
			t.mu.Unlock()
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

}} with only the necessary minimal changes to implement {{add LotID and EntryOrderID fields to Position, add LotID/EntryOrderID/ExitOrderID fields to ExitRecord, and add NextLotSeq to Trader and BotState to mint and persist stable lot IDs}}. Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()). Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline. Only apply the minimal edits required to implement {{add LotID and EntryOrderID fields to Position, add LotID/EntryOrderID/ExitOrderID fields to ExitRecord, and add NextLotSeq to Trader and BotState to mint and persist stable lot IDs}}. Fix any compile error(s). Return the complete file, copy-paste ready, in IDE.
