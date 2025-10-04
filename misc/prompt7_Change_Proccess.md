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

type Position struct {
	OpenPrice float64
	Side      OrderSide
	SizeBase  float64
	Stop      float64
	Take      float64
	OpenTime  time.Time
	// --- NEW: record entry fee for later P/L adjustment ---
	EntryFee float64

	// --- NEW (runner-only trailing fields; used only when this lot is the runner) ---
	TrailActive bool    // becomes true after TRAIL_ACTIVATE_PCT threshold
	TrailPeak   float64 // best favorable price since activation (peak for long; trough for short)
	TrailStop   float64 // current trailing stop level derived from TrailPeak and TRAIL_DISTANCE_PCT

	// --- NEW: human-readable gates/why string captured at entry time ---
	Reason string `json:"reason,omitempty"`
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
}

type Trader struct {
	cfg       Config
	broker    Broker
	model     *AIMicroModel
	pos       *Position   // kept for backward compatibility with earlier logic (represents last lot in aggregate)
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

	// daily
	dailyStart time.Time
	dailyPnL   float64
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
	persist := getEnvBool("PERSIST_STATE", true)
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

// ---- helpers for pyramiding ----

func allowPyramiding() bool {
	return getEnvBool("ALLOW_PYRAMIDING", false)
}
func pyramidMinSeconds() int {
	return getEnvInt("PYRAMID_MIN_SECONDS_BETWEEN", 0)
}
func pyramidMinAdversePct() float64 {
	return getEnvFloat("PYRAMID_MIN_ADVERSE_PCT", 0.0) // 0 = no adverse-move requirement
}
func scalpTPDecayEnabled() bool   { return getEnvBool("SCALP_TP_DECAY_ENABLE", false) }
func scalpTPDecayMode() string    { return getEnv("SCALP_TP_DEC_MODE", "linear") }
func scalpTPDecPct() float64      { return getEnvFloat("SCALP_TP_DEC_PCT", 0.0) }      // % points
func scalpTPDecayFactor() float64 { return getEnvFloat("SCALP_TP_DECAY_FACTOR", 1.0) } // multiplicative
func scalpTPMinPct() float64      { return getEnvFloat("SCALP_TP_MIN_PCT", 0.0) }      // floor

// --- NEW: Option A – time-based exponential decay knobs (0 disables) ---
func pyramidDecayLambda() float64 { return getEnvFloat("PYRAMID_DECAY_LAMBDA", 0.0) }  // per-minute
func pyramidDecayMinPct() float64 { return getEnvFloat("PYRAMID_DECAY_MIN_PCT", 0.0) } // floor

// Cap concurrent lots (env-tunable). Default is effectively "no cap".
func maxConcurrentLots() int {
	n := getEnvInt("MAX_CONCURRENT_LOTS", 1_000_000)
	if n < 1 {
		n = 1_000_000 // safety: never block adds due to bad input
	}
	return n
}

// Spot SELL guard and paper overrides
func requireBaseForShort() bool { return getEnvBool("REQUIRE_BASE_FOR_SHORT", true) }
func paperBaseBalance() float64 { return getEnvFloat("PAPER_BASE_BALANCE", 0.0) }
func baseAssetOverride() string { return getEnv("BASE_ASSET", "") }
func baseStepOverride() float64 { return getEnvFloat("BASE_STEP", 0.0) } // 0 => unknown

// --- NEW: backtest-only quote balance helpers (BUY gating symmetry) ---
func paperQuoteBalance() float64 { return getEnvFloat("PAPER_QUOTE_BALANCE", 0.0) }
func quoteStepOverride() float64 { return getEnvFloat("QUOTE_STEP", 0.0) } // 0 => unknown

// Runner tuning (internal, no new env keys): runner takes profit farther, same stop by default.
const runnerTPMult = 2.0
const runnerStopMult = 1.0

// Minimal "runner gap" guard (disabled)
const runnerMinGapPct = 0.0

// --- NEW: runner-only trailing env tunables (0 disables) ---
func trailActivatePct() float64 {
	return getEnvFloat("TRAIL_ACTIVATE_PCT", 0.0)
}
func trailDistancePct() float64 {
	return getEnvFloat("TRAIL_DISTANCE_PCT", 0.0)
}

// --- NEW: post-only entry env tunables (0/disabled by default) ---
func limitPriceOffsetBps() float64 { return getEnvFloat("LIMIT_PRICE_OFFSET_BPS", 0.0) } // e.g., 5 = 0.05%
func spreadMinBps() float64        { return getEnvFloat("SPREAD_MIN_BPS", 0.0) }         // gate; 0 disables
func limitTimeoutSec() int         { return getEnvInt("LIMIT_TIMEOUT_SEC", 0) }          // wait window; 0 disables
func orderType() string            { return strings.ToLower(strings.TrimSpace(getEnv("ORDER_TYPE", "market"))) }

// --- NEW: risk ramping envs (side-aware) ---
func rampEnable() bool          { return getEnvBool("RAMP_ENABLE", false) }
func rampMode() string          { return strings.ToLower(strings.TrimSpace(getEnv("RAMP_MODE", "linear"))) } // linear|exp
func rampStartPct() float64     { return getEnvFloat("RAMP_START_PCT", 0.0) }
func rampStepPct() float64      { return getEnvFloat("RAMP_STEP_PCT", 0.0) }   // for linear
func rampGrowth() float64       { return getEnvFloat("RAMP_GROWTH", 1.0) }     // for exp
func rampMaxPct() float64       { return getEnvFloat("RAMP_MAX_PCT", 0.0) }    // 0=unbounded
func clamp(x, lo, hi float64) float64 {
	if hi > 0 && x > hi {
		return hi
	}
	if x < lo {
		return lo
	}
	return x
}

// latestEntry returns the most recent long lot entry price, or 0 if none.
// NOTE: maintained for compatibility; now uses SideBook(BUY).
func (t *Trader) latestEntry() float64 {
	book := t.books[SideBuy]
	if book == nil || len(book.Lots) == 0 {
		return 0
	}
	return book.Lots[len(book.Lots)-1].OpenPrice
}

// --- NEW: side-aware latestEntry helper (does not alter existing latestEntry name/signature) ---
func (t *Trader) latestEntryBySide(side OrderSide) float64 {
	book := t.books[side]
	if book == nil || len(book.Lots) == 0 {
		return 0
	}
	return book.Lots[len(book.Lots)-1].OpenPrice
}

// // aggregateOpen sets t.pos to the latest lot (for legacy reads) or nil.
// // Also rebuilds legacy t.lots and runnerIdx from the authoritative books.
// func (t *Trader) aggregateOpen() {
// 	t.refreshAggregateFromBooks()
// }

// // --- NEW: rebuild legacy aggregate view & runner index from books ---
// func (t *Trader) refreshAggregateFromBooks() {
// 	var agg []*Position
// 	rIdx := -1
// 	// Flatten BUY first then SELL for deterministic legacy indexing.
// 	if b := t.books[SideBuy]; b != nil {
// 		for i, p := range b.Lots {
// 			if b.RunnerID == i && rIdx == -1 {
// 				rIdx = len(agg) // runner position in aggregate
// 			}
// 			agg = append(agg, p)
// 		}
// 	}
// 	if s := t.books[SideSell]; s != nil {
// 		for i, p := range s.Lots {
// 			if s.RunnerID == i && rIdx == -1 {
// 				rIdx = len(agg)
// 			}
// 			agg = append(agg, p)
// 		}
// 	}
// 	t.lots = agg
// 	t.runnerIdx = rIdx
// 	if len(agg) == 0 {
// 		t.pos = nil
// 	} else {
// 		t.pos = agg[len(agg)-1]
// 	}
// }

// applyRunnerTargets adjusts stop/take for the designated runner lot.
func (t *Trader) applyRunnerTargets(p *Position) {
	if p == nil {
		return
	}
	op := p.OpenPrice
	if p.Side == SideBuy {
		p.Stop = op * (1.0 - (t.cfg.StopLossPct*runnerStopMult)/100.0)
		p.Take = op * (1.0 + (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
	} else {
		p.Stop = op * (1.0 + (t.cfg.StopLossPct*runnerStopMult)/100.0)
		p.Take = op * (1.0 - (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
	}
}

// --- NEW: runner trailing updater (no-ops if env not set or lot not runner).
// Returns (shouldExit, newTrailStopIfAny).
func (t *Trader) updateRunnerTrail(lot *Position, price float64) (bool, float64) {
	if lot == nil {
		return false, 0
	}
	act := trailActivatePct()
	dist := trailDistancePct()
	if act <= 0 || dist <= 0 {
		return false, 0
	}

	switch lot.Side {
	case SideBuy:
		activateAt := lot.OpenPrice * (1.0 + act/100.0)
		if !lot.TrailActive {
			if price >= activateAt {
				lot.TrailActive = true
				lot.TrailPeak = price
				lot.TrailStop = price * (1.0 - dist/100.0)
			}
		} else {
			if price > lot.TrailPeak {
				lot.TrailPeak = price
				ts := lot.TrailPeak * (1.0 - dist/100.0)
				if ts > lot.TrailStop {
					lot.TrailStop = ts
				}
			}
			if price <= lot.TrailStop && lot.TrailStop > 0 {
				return true, lot.TrailStop
			}
		}
	case SideSell:
		activateAt := lot.OpenPrice * (1.0 - act/100.0)
		if !lot.TrailActive {
			if price <= activateAt {
				lot.TrailActive = true
				lot.TrailPeak = price // trough for short
				lot.TrailStop = price * (1.0 + dist/100.0)
			}
		} else {
			if price < lot.TrailPeak {
				lot.TrailPeak = price
				lot.TrailStop = lot.TrailPeak * (1.0 + dist/100.0)
			}
			if price >= lot.TrailStop && lot.TrailStop > 0 {
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

// closeLotAtIndex closes a single lot at global aggregate idx (assumes mutex held), performing I/O unlocked.
// exitReason is a short label for logs: "take_profit" | "stop_loss" | "trailing_stop" (or other).
func (t *Trader) closeLotAtIndex(ctx context.Context, c []Candle, idx int, exitReason string) (string, error) {
	// Map aggregate index to (side, local index)
	side, localIdx := t.aggregateIndexToSide(idx)
	if side == "" {
		// fallback if mapping fails (shouldn't happen)
		side = SideBuy
		localIdx = idx
	}
	book := t.book(side)
	price := c[len(c)-1].Close
	lot := book.Lots[localIdx]
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}
	baseRequested := lot.SizeBase
	quote := baseRequested * price

	// unlock for I/O
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
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
	// re-lock
	t.mu.Lock()

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
	pl -= lot.EntryFee // subtract entry fee recorded
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
		lot.Side, kind, exitReason, lot.OpenPrice, priceExec, baseFilled, rawPL, lot.EntryFee, exitFee, pl)

	t.dailyPnL += pl
	t.equityUSD += pl

	// --- NEW: increment win/loss trades ---
	if pl >= 0 {
		mtxTrades.WithLabelValues("win").Inc()
	} else {
		mtxTrades.WithLabelValues("loss").Inc()
	}

	// Normalize/sanitize reason and side label for metrics
	reasonLbl := exitReason
	if reasonLbl == "" {
		reasonLbl = "other"
	}
	sideLbl := "buy"
	if lot.Side == SideSell {
		sideLbl = "sell"
	}
	mtxExitReasons.WithLabelValues(reasonLbl, sideLbl).Inc()

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

	// // Rebuild legacy aggregate view for logs/compat
	// t.refreshAggregateFromBooks()

	// Include reason in message for operator visibility
	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, lot.EntryFee+exitFee)
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

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	_ = t.shouldRefit(len(c)) // intentionally unused here (guard only)

	// Keep paper broker price in sync with the latest close so paper fills are realistic.
	if pb, ok := t.broker.(*PaperBroker); ok {
		if len(c) > 0 {
			pb.mu.Lock()
			pb.price = c[len(c)-1].Close
			pb.mu.Unlock()
		}
	}

	// TODO: remove TRACE
	lsb := len(t.book(SideBuy).Lots)
	lss := len(t.book(SideSell).Lots)
	log.Printf("TRACE step.start ts=%s price=%.8f lotsBuy=%d lotsSell=%d lastAddBuy=%s lastAddSell=%s winLowBuy=%.8f winHighSell=%.8f latchedGateBuy=%.8f latchedGateSell=%.8f",
		now.Format(time.RFC3339), c[len(c)-1].Close, lsb, lss,
		t.lastAddBuy.Format(time.RFC3339), t.lastAddSell.Format(time.RFC3339), t.winLowBuy, t.winHighSell, t.latchedGateBuy, t.latchedGateSell)

	// --- EXIT path: if any lots are open, evaluate TP/SL and trailing for each side; close one at a time.
	if (lsb > 0) || (lss > 0) {
		price := c[len(c)-1].Close
		nearestTakeBuy := 0.0
		nearestTakeSell := 0.0

		// Helper to scan a side book
		scanSide := func(side OrderSide, baseIdx int) (string, bool, error) {
			book := t.book(side)
			for i := 0; i < len(book.Lots); {
				lot := book.Lots[i]

				// runner trailing for this side
				if i == book.RunnerID {
					if trigger, tstop := t.updateRunnerTrail(lot, price); trigger {
						lot.Stop = tstop
						msg, err := t.closeLotAtIndex(ctx, c, baseIdx+i, "trailing_stop")
						if err != nil {
							t.mu.Unlock()
							return "", true, err
						}
						t.mu.Unlock()
						return msg, true, nil
					}
				}

				trigger := false
				exitReason := ""
				if lot.Side == SideBuy && (price <= lot.Stop || price >= lot.Take) {
					trigger = true
					if price <= lot.Stop {
						exitReason = "stop_loss"
					} else {
						exitReason = "take_profit"
					}
				}
				if lot.Side == SideSell && (price >= lot.Stop || price <= lot.Take) {
					trigger = true
					if price >= lot.Stop {
						exitReason = "stop_loss"
					} else {
						exitReason = "take_profit"
					}
				}
				if trigger {
					msg, err := t.closeLotAtIndex(ctx, c, baseIdx+i, exitReason)
					if err != nil {
						t.mu.Unlock()
						return "", true, err
					}
					t.mu.Unlock()
					return msg, true, nil
				}

				// nearest summary
				if lot.Side == SideBuy {
					if nearestTakeBuy == 0 || lot.Take < nearestTakeBuy {
						nearestTakeBuy = lot.Take
					}
				} else {
					if nearestTakeSell == 0 || lot.Take > nearestTakeSell {
						nearestTakeSell = lot.Take
					}
				}
				i++
			}
			return "", false, nil
		}

		// BUY side first, then SELL; base index for SELL is count of BUY lots
		if msg, done, err := scanSide(SideBuy, 0); done || err != nil {
			return msg, err
		}
		if msg, done, err := scanSide(SideSell, lsb); done || err != nil {
			return msg, err
		}

		log.Printf("[DEBUG] nearest Take (BuyLots)=%.2f (SellLots)=%.2f across %d BuyLots and %d SellLots", nearestTakeBuy, nearestTakeSell, lsb, lss)
	}

	d := decide(c, t.model, t.mdlExt)
	totalLots := lsb + lss
	log.Printf("[DEBUG] Total Lots=%d, Decision=%s Reason = %s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v",
		totalLots, d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	price := c[len(c)-1].Close

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

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}
	if d.Signal == Flat {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT [%s]", d.Reason), nil
	}

	// Respect lot cap (both sides)
	if (lsb + lss) >= maxConcurrentLots() {
		t.mu.Unlock()
		log.Printf("[DEBUG] lot cap reached (%d); HOLD", maxConcurrentLots())
		return "HOLD", nil
	}
	// Determine the side and its book
	side := d.SignalToSide()
	book := t.book(side)

	// Determine if we are opening first lot for THIS SIDE or attempting a pyramid add (side-aware).
	isAdd := len(book.Lots) > 0 && allowPyramiding() && (d.Signal == Buy || d.Signal == Sell)

	// --- NEW: variables to capture gate audit fields for the reason string (side-biased; no winLow) ---
	var (
		reasonGatePrice float64
		reasonLatched   float64
		reasonEffPct    float64
		reasonBasePct   float64
		reasonElapsedHr float64
	)

	// Gating for pyramiding adds — spacing + adverse move (with optional time-decay), side-aware.
	if isAdd {
		// Choose side-aware anchor set
		var lastAddSide time.Time
		if side == SideBuy {
			lastAddSide = t.lastAddBuy
		} else {
			lastAddSide = t.lastAddSell
		}

		// 1) Spacing
		s := pyramidMinSeconds()
		// TODO: remove TRACE
		log.Printf("TRACE pyramid.spacing since_last=%.1fs need>=%ds", time.Since(lastAddSide).Seconds(), s)
		if time.Since(lastAddSide) < time.Duration(s)*time.Second {
			t.mu.Unlock()
			hrs := time.Since(lastAddSide).Hours()
			log.Printf("[DEBUG] pyramid: blocked by spacing; since_last=%vHours need>=%ds", fmt.Sprintf("%.1f", hrs), s)
			return "HOLD", nil
		}

		// 2) Adverse move gate with optional time-based exponential decay.
		basePct := pyramidMinAdversePct()
		effPct := basePct
		lambda := pyramidDecayLambda()
		floor := pyramidDecayMinPct()
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
				}
				// baseline gate: last * (1 - effPct); latched replaces baseline
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
	if rampEnable() {
		k := len(book.Lots) // number of existing lots on THIS SIDE
		switch rampMode() {
		case "exp":
			start := rampStartPct()
			g := rampGrowth()
			if g <= 0 {
				g = 1.0
			}
			f := start
			for i := 0; i < k; i++ {
				f *= g
			}
			if max := rampMaxPct(); max > 0 && f > max {
				f = max
			}
			if f > 0 {
				riskPct = f
			}
		default: // linear
			start := rampStartPct()
			step := rampStepPct()
			f := start + float64(k)*step
			f = clamp(f, 0, rampMaxPct())
			if f > 0 {
				riskPct = f
			}
		}
	}

	quote := (riskPct / 100.0) * t.equityUSD
	if quote < t.cfg.OrderMinUSD {
		quote = t.cfg.OrderMinUSD
	}
	base := quote / price

	// TODO: remove TRACE
	log.Printf("TRACE sizing.pre side=%s eq=%.2f riskPct=%.4f quote=%.2f price=%.8f base=%.8f", side, t.equityUSD, riskPct, quote, price, base)

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// Reserve quote needed to close all existing short lots at current price.
		var reservedShortQuote float64
		if sb := t.book(SideSell); sb != nil {
			for _, lot := range sb.Lots {
				reservedShortQuote += lot.SizeBase * price
			}
		}

		// Ask broker for quote balance/step (uniform: live & DryRun).
		sym, aq, qs, err := t.broker.GetAvailableQuote(ctx, t.cfg.ProductID)
		if err != nil || strings.TrimSpace(sym) == "" {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: GetAvailableQuote failed: %v", err)
		}
		availQuote := aq
		qstep := qs
		if qstep <= 0 {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: missing/invalid QUOTE step for %s (step=%.8f)", t.cfg.ProductID, qstep)
		}

		// TODO: remove TRACE
		log.Printf("TRACE buy.gate.pre availQuote=%.2f reservedShort=%.2f needQuoteRaw=%.2f qstep=%.8f",
			availQuote, reservedShortQuote, quote, qstep)

		// Floor the needed quote to step.
		neededQuote := quote
		if qstep > 0 {
			n := math.Floor(neededQuote/qstep) * qstep
			if n > 0 {
				neededQuote = n
			}
		}

		spare := availQuote - reservedShortQuote
		if spare < 0 {
			spare = 0
		}
		if spare+spareEps < neededQuote {
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE BUY: need=%.2f quote, spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
				neededQuote, spare, availQuote, reservedShortQuote, qstep)
			// TODO: remove TRACE
			log.Printf("TRACE buy.gate.block need=%.2f spare=%.2f", neededQuote, spare)
			return "HOLD", nil
		}

		// Enforce exchange minimum notional after snapping, then snap UP to step to keep >= min; re-check spare.
		if neededQuote < t.cfg.OrderMinUSD {
			neededQuote = t.cfg.OrderMinUSD
			if qstep > 0 {
				steps := math.Ceil(neededQuote / qstep)
				neededQuote = steps * qstep
			}
			if spare+spareEps < neededQuote {
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE BUY: need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
					neededQuote, spare, availQuote, reservedShortQuote, qstep)
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
	if side == SideSell && requireBaseForShort() {
		// Sum reserved base for long lots (BUY side)
		var reservedLong float64
		if bb := t.book(SideBuy); bb != nil {
			for _, lot := range bb.Lots {
				reservedLong += lot.SizeBase
			}
		}

		// Ask broker for base balance/step (uniform: live & DryRun).
		sym, ab, stp, err := t.broker.GetAvailableBase(ctx, t.cfg.ProductID)
		if err != nil || strings.TrimSpace(sym) == "" {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: GetAvailableBase failed: %v", err)
		}
		availBase := ab
		step := stp
		if step <= 0 {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: missing/invalid BASE step for %s (step=%.8f)", t.cfg.ProductID, step)
		}

		// TODO: remove TRACE
		log.Printf("TRACE sell.gate.pre availBase=%.8f reservedLong=%.8f needBaseRaw=%.8f step=%.8f",
			availBase, reservedLong, base, step)

		// Floor the *needed* base to step (if known) and cap by spare availability
		neededBase := base
		if step > 0 {
			n := math.Floor(neededBase/step) * step
			if n > 0 {
				neededBase = n
			}
		}
		spare := availBase - reservedLong
		if spare < 0 {
			spare = 0
		}
		if spare+spareEps < neededBase {
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE SELL: need=%.8f base, spare=%.8f (avail=%.8f, reserved_longs=%.8f, step=%.8f)",
				neededBase, spare, availBase, reservedLong, step)
			// TODO: remove TRACE
			log.Printf("TRACE sell.gate.block need=%.8f spare=%.8f", neededBase, spare)
			return "HOLD", nil
		}

		// Use the floored base for the order by updating quote
		quote = neededBase * price
		base = neededBase

		// Ensure SELL meets exchange min funds and step rules (and re-check spare symmetry)
		if quote < t.cfg.OrderMinUSD {
			quote = t.cfg.OrderMinUSD
			base = quote / price
			if step > 0 {
				b := math.Floor(base/step) * step
				if b > 0 {
					base = b
					quote = base * price
				}
			}
			// >>> Symmetry: re-check spare after min-notional snap <<<
			if spare+spareEps < base {
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE SELL: need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, step=%.8f)",
					base, spare, availBase, reservedLong, step)
				// TODO: remove TRACE
				log.Printf("TRACE sell.gate.block minNotional need=%.8f spare=%.8f", base, spare)
				return "HOLD", nil
			}
		}

		// TODO: remove TRACE
		log.Printf("TRACE sell.gate.post needBase=%.8f spare=%.8f quote=%.2f", base, spare, quote)
	}

	// Stops/takes (baseline for scalps)
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	if side == SideSell {
		stop = price * (1.0 + t.cfg.StopLossPct/100.0)
		take = price * (1.0 - t.cfg.TakeProfitPct/100.0)
	}

	// Decide if this new entry will be the runner FOR THIS SIDE (only when there is no existing runner in the side).
	willBeRunner := (book.RunnerID == -1 && len(book.Lots) == 0)
	if willBeRunner {
		// Stretch runner targets without introducing new env keys.
		if side == SideBuy {
			stop = price * (1.0 - (t.cfg.StopLossPct*runnerStopMult)/100.0)
			take = price * (1.0 + (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
		} else {
			stop = price * (1.0 + (t.cfg.StopLossPct*runnerStopMult)/100.0)
			take = price * (1.0 - (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
		}
	} else if scalpTPDecayEnabled() {
		// This is a scalp add on THIS SIDE: compute k = number of existing scalps in this side
		k := len(book.Lots)
		if book.RunnerID >= 0 && book.RunnerID < len(book.Lots) {
			k = len(book.Lots) - 1 // exclude the side's runner
		}
		baseTP := t.cfg.TakeProfitPct
		tpPct := baseTP

		switch scalpTPDecayMode() {
		case "exp", "exponential":
			// geometric decay: baseTP * factor^k, floored
			f := scalpTPDecayFactor()
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
			dec := scalpTPDecPct()
			tpPct = baseTP - float64(k)*dec
		}

		minTP := scalpTPMinPct()
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
			k, scalpTPDecayMode(), t.cfg.TakeProfitPct, tpPct, minTP, take)
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
		offsetBps := limitPriceOffsetBps()
		limitWait := limitTimeoutSec()
		spreadGate := spreadMinBps() // NOTE: no spread source via Broker; positive gate disables maker attempt
		wantLimit := orderType() == "limit" && offsetBps > 0 && limitWait > 0

		// --- NEW: maker-first routing via Broker when ORDER_TYPE=limit ---
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
				baseAtLimit := quote / limitPx
				log.Printf("TRACE postonly.place side=%s limit=%.8f baseReq=%.8f timeout_sec=%d", side, limitPx, baseAtLimit, limitWait)

				// Place post-only limit and poll for fill
				orderID, err := t.broker.PlaceLimitPostOnly(ctx, t.cfg.ProductID, side, limitPx, baseAtLimit)
				if err == nil && strings.TrimSpace(orderID) != "" {
					deadline := time.Now().Add(time.Duration(limitWait) * time.Second)
					for time.Now().Before(deadline) {
						ord, gErr := t.broker.GetOrder(ctx, t.cfg.ProductID, orderID)
						if gErr == nil && ord != nil && (ord.BaseSize > 0 || ord.QuoteSpent > 0) {
							placed = ord
							log.Printf("TRACE postonly.filled order_id=%s price=%.8f baseFilled=%.8f quoteSpent=%.2f fee=%.4f",
								orderID, placed.Price, placed.BaseSize, placed.QuoteSpent, placed.CommissionUSD)
							mtxOrders.WithLabelValues("live", string(side)).Inc()
							mtxTrades.WithLabelValues("open").Inc()
							break
						}
						time.Sleep(200 * time.Millisecond)
					}
					if placed == nil {
						// Timeout — cancel and fall back to market
						_ = t.broker.CancelOrder(ctx, t.cfg.ProductID, orderID)
						log.Printf("TRACE postonly.timeout fallback=market order_id=%s", orderID)
					}
				} else if err != nil {
					log.Printf("TRACE postonly.error fallback=market err=%v", err)
				}
			}
		}

		// If maker path did not result in a fill, fall back to market path (baseline behavior).
		if placed == nil {
			// TODO: remove TRACE
			log.Printf("TRACE order.open request side=%s quote=%.2f baseEst=%.8f priceSnap=%.8f stop=%.8f take=%.8f",
				side, quote, base, price, stop, take)
			var err error
			placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
			if err != nil {
				// Retry once with ORDER_MIN_USD on insufficient-funds style failures.
				e := strings.ToLower(err.Error())
				if quote > t.cfg.OrderMinUSD && (strings.Contains(e, "insufficient") || strings.Contains(e, "funds") || strings.Contains(e, "400")) {
					log.Printf("[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f", quote, err, t.cfg.OrderMinUSD)
					quote = t.cfg.OrderMinUSD
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
	d = decide(c, t.model, t.mdlExt) // for reason payload reuse
	var gatesReason string
	if side == SideBuy {
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
		Stop:      stop,
		Take:      take,
		OpenTime:  now,
		EntryFee:  entryFee,
		Reason:    gatesReason, // side-biased; no winLow
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

	// TODO: remove TRACE
	log.Printf("TRACE lot.open side=%s open=%.8f sizeBase=%.8f stop=%.8f take=%.8f fee=%.4f BuyLots=%d SellLots=%d",
		side, newLot.OpenPrice, newLot.SizeBase, newLot.Stop, newLot.Take, newLot.EntryFee, len(t.book(SideBuy).Lots), len(t.book(SideBuy).Lots))

	// Assign/designate runner for THIS SIDE if none exists yet; otherwise this is a scalp.
	if book.RunnerID == -1 {
		book.RunnerID = len(book.Lots) - 1 // the just-added lot is the side's runner
		// Initialize runner trailing baseline
		r := book.Lots[book.RunnerID]
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		// Ensure runner's stretched targets are applied (baseline behavior for runner).
		t.applyRunnerTargets(r)
		// TODO: remove TRACE
		log.Printf("TRACE runner.assign idx=%d open=%.8f stop=%.8f take=%.8f", book.RunnerID, r.OpenPrice, r.Stop, r.Take)
	}

	// Rebuild legacy aggregate view for logs/compat
	// t.refreshAggregateFromBooks()

	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f stop=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			d.Signal, actualQuote, baseToUse, newLot.Stop, newLot.Take, entryFee, newLot.Reason, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s quote=%.2f stop=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			d.Signal, actualQuote, newLot.Stop, newLot.Take, entryFee, newLot.Reason, d.Reason)
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
	if t.stateFile == "" || !getEnvBool("PERSIST_STATE", true) {
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
	if t.stateFile == "" || !getEnvBool("PERSIST_STATE", true) {
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

	// Side-aware pyramiding persisted state
	t.lastAddBuy = st.LastAddBuy
	t.lastAddSell = st.LastAddSell
	t.winLowBuy = st.WinLowBuy
	t.winHighSell = st.WinHighSell
	t.latchedGateBuy = st.LatchedGateBuy
	t.latchedGateSell = st.LatchedGateSell

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
	// If we restored with open lots but have no lastAdd for a side, seed the decay clock to "now"
	// and reset adverse trackers/latches so they rebuild over real time (prevents instant latch).
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

// --- NEW: helper to map aggregate index -> (side, localIdx) using the current books ---
func (t *Trader) aggregateIndexToSide(idx int) (OrderSide, int) {
	if idx < 0 {
		return "", -1
	}
	bb := t.book(SideBuy)
	if idx < len(bb.Lots) {
		return SideBuy, idx
	}
	idx -= len(bb.Lots)
	sb := t.book(SideSell)
	if idx < len(sb.Lots) {
		return SideSell, idx
	}
	return "", -1
}

}} with only the necessary minimal changes to implement {{replace global-index lot closing with side-aware closeLot(ctx, c, side, localIdx) and update step() to call it directly}}. Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()). Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline. Only apply the minimal edits required to implement {{replace global-index lot closing with side-aware closeLot(ctx, c, side, localIdx) and update step() to call it directly}}. Return the complete file, copy-paste ready, in IDE.
