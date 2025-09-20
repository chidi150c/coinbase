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

// BotState is the persistent snapshot of trader state.
type BotState struct {
	EquityUSD      float64
	DailyStart     time.Time
	DailyPnL       float64
	Lots           []*Position
	Model          *AIMicroModel
	MdlExt         *ExtendedLogit
	WalkForwardMin int
	LastFit        time.Time
	LastAdd           time.Time
	WinLow            float64
	LatchedGate       float64
	WinHigh           float64
	LatchedGateShort  float64
}

type Trader struct {
	cfg        Config
	broker     Broker
	model      *AIMicroModel
	pos        *Position   // kept for backward compatibility with earlier logic
	lots       []*Position // NEW: multiple lots when pyramiding is enabled
	lastAdd    time.Time   // NEW: last time a pyramid add was placed
	dailyStart time.Time
	dailyPnL   float64
	mu         sync.Mutex
	equityUSD  float64

	// NEW (minimal): optional extended head passed through to decide(); nil if unused.
	mdlExt *ExtendedLogit

	// NEW: path to persisted state file
	stateFile string

	// NEW: track last model fit time for walk-forward
	lastFit time.Time

	// NEW: index of the designated runner lot (-1 if none). Not persisted; derived on load.
	runnerIdx int

	// --- NEW: adverse gate helpers (since last add) ---
	// BUY path trackers:
	winLow      float64 // lowest price seen since lastAdd (BUY adverse tracking)
	latchedGate float64 // latched adverse gate once threshold time is reached; reset on new add
	// SELL path trackers (NEW):
	winHigh           float64 // highest price seen since lastAdd (SELL adverse tracking)
	latchedGateShort  float64 // latched adverse gate for SELL once threshold time is reached; reset on new add
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,
		runnerIdx:  -1,
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
			// >>> FAIL-FAST (requested): if live (not DryRun) and persistence is expected,
			// and the state path isn't a mounted/writable volume, abort with a clear message.
			if !t.cfg.DryRun && shouldFatalNoStateMount(t.stateFile) {
				log.Fatalf("[FATAL] persistence required but state path is not a mounted volume or not writable: STATE_FILE=%s ; "+
					"mount /opt/coinbase/state into the container and ensure it's writable. "+
					"Example docker-compose:\n  volumes:\n    - /opt/coinbase/state:/opt/coinbase/state",
					t.stateFile)
			}
		}
	}
	// If state has existing lots but no runner assigned (fresh field), default runner to the oldest or 0.
	if t.runnerIdx == -1 && len(t.lots) > 0 {
		t.runnerIdx = 0
	}
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

// latestEntry returns the most recent long lot entry price, or 0 if none.
func (t *Trader) latestEntry() float64 {
	if len(t.lots) == 0 {
		return 0
	}
	return t.lots[len(t.lots)-1].OpenPrice
}

// aggregateOpen sets t.pos to the latest lot (for legacy reads) or nil.
func (t *Trader) aggregateOpen() {
	if len(t.lots) == 0 {
		t.pos = nil
		return
	}
	// keep last lot as representative for legacy checks
	t.pos = t.lots[len(t.lots)-1]
}

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

// closeLotAtIndex closes a single lot at idx (assumes mutex held), performing I/O unlocked.
// exitReason is a short label for logs: "take_profit" | "stop_loss" | "trailing_stop" (or other).
func (t *Trader) closeLotAtIndex(ctx context.Context, c []Candle, idx int, exitReason string) (string, error) {
	price := c[len(c)-1].Close
	lot := t.lots[idx]
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
		mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
	}
	// re-lock
	t.mu.Lock()

	// --- NEW: check if the lot being closed is the most recent add (newest OpenTime) ---
	wasNewest := true
	refOpen := lot.OpenTime
	for j := range t.lots {
		if j == idx {
			continue
		}
		if !t.lots[j].OpenTime.IsZero() && t.lots[j].OpenTime.After(refOpen) {
			wasNewest = false
			break
		}
	}

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

	t.dailyPnL += pl
	t.equityUSD += pl

	// --- NEW: increment win/loss trades ---
	if pl >= 0 {
		mtxTrades.WithLabelValues("win").Inc()
	} else {
		mtxTrades.WithLabelValues("loss").Inc()
	}

	// Track if we removed the runner and adjust runnerIdx accordingly after removal.
	removedWasRunner := (idx == t.runnerIdx)

	// remove lot idx
	t.lots = append(t.lots[:idx], t.lots[idx+1:]...)

	// shift runnerIdx if needed
	if t.runnerIdx >= 0 {
		if idx < t.runnerIdx {
			t.runnerIdx-- // slice shifted left
		} else if idx == t.runnerIdx {
			// runner removed; promote the NEWEST remaining lot (if any) to runner
			if len(t.lots) > 0 {
				t.runnerIdx = len(t.lots) - 1
				// reset trailing fields for the newly promoted runner
				nr := t.lots[t.runnerIdx]
				nr.TrailActive = false
				nr.TrailPeak = nr.OpenPrice
				nr.TrailStop = 0
				// also re-apply runner targets (keeps existing behavior)
				t.applyRunnerTargets(nr)
			} else {
				t.runnerIdx = -1
			}
		}
	}

	// --- if the closed lot was the most recent add, re-anchor pyramiding timers/state ---
	if wasNewest {
		// If any lots remain, restart the decay clock from now to avoid instant latch.
		// If none remain, also set now; next add will proceed normally.
		t.lastAdd = time.Now().UTC()
		// Reset adverse tracking; winLow/winHigh will start accumulating after t_floor_min,
		// and latching can only occur after 2*t_floor_min from this new anchor.
		t.winLow = 0
		t.latchedGate = 0
		t.winHigh = 0
		t.latchedGateShort = 0
	}

	t.aggregateOpen()
	// Include reason in message for operator visibility
	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, lot.EntryFee+exitFee)
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
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
	// Any refit logic must first check shouldRefit(len(c)).
	// This preserves restored weights when history is thin.
	_ = t.shouldRefit(len(c)) // intentionally unused here (guard only)

	// Keep paper broker price in sync with the latest close so paper fills are realistic.
	if pb, ok := t.broker.(*PaperBroker); ok {
		if len(c) > 0 {
			pb.mu.Lock()
			pb.price = c[len(c)-1].Close
			pb.mu.Unlock()
		}
	}

	// --- EXIT path: if any lots are open, evaluate TP/SL for each and close those that trigger.
	if len(t.lots) > 0 {
		price := c[len(c)-1].Close
		nearestStop := 0.0
		nearestTake := 0.0
		for i := 0; i < len(t.lots); {
			lot := t.lots[i]

			// --- NEW: runner-only trailing exit check (wired alongside TP/SL) ---
			if i == t.runnerIdx {
				if trigger, tstop := t.updateRunnerTrail(lot, price); trigger {
					// reflect trailing level for visibility in debug/Slack
					lot.Stop = tstop
					msg, err := t.closeLotAtIndex(ctx, c, i, "trailing_stop")
					if err != nil {
						t.mu.Unlock()
						return "", err
					}
					// closeLotAtIndex removed index i; continue without i++
					t.mu.Unlock()
					return msg, nil
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
				msg, err := t.closeLotAtIndex(ctx, c, i, exitReason)
				if err != nil {
					t.mu.Unlock()
					return "", err
				}
				// closeLotAtIndex removed index i; continue without i++
				t.mu.Unlock()
				return msg, nil
			}

			if lot.Side == SideBuy {
				if nearestStop == 0 || lot.Stop > nearestStop { // highest stop for long
					nearestStop = lot.Stop
				}
				if nearestTake == 0 || lot.Take < nearestTake { // lowest take for long
					nearestTake = lot.Take
				}
			} else { // SideSell
				if nearestStop == 0 || lot.Stop < nearestStop { // lowest stop for short
					nearestStop = lot.Stop
				}
				if nearestTake == 0 || lot.Take > nearestTake { // highest take for short
					nearestTake = lot.Take
				}
			}

			i++ // no trigger; move to next
		}
		log.Printf("[DEBUG] nearest stop=%.2f take=%.2f across %d lots", nearestStop, nearestTake, len(t.lots))
	}

	d := decide(c, t.model, t.mdlExt)
	log.Printf("[DEBUG] Lots=%d, Decision=%s Reason = %s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v", len(t.lots), d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)

	// Ignore discretionary SELL signals while lots are open; exits are TP/SL only.
	// if len(t.lots) > 0 && d.Signal == Sell {
	// 	t.mu.Unlock()
	// 	return "HOLD", nil
	// }

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	price := c[len(c)-1].Close

	// --- NEW: track lowest price since last add (BUY path) and highest price (SELL path) ---
	if !t.lastAdd.IsZero() {
		if t.winLow == 0 || price < t.winLow {
			t.winLow = price
		}
		if t.winHigh == 0 || price > t.winHigh {
			t.winHigh = price
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
	if len(t.lots) >= maxConcurrentLots() {
		t.mu.Unlock()
		log.Printf("[DEBUG] lot cap reached (%d); HOLD", maxConcurrentLots())
		return "HOLD", nil
	}

	// Determine if we are opening first lot or attempting a pyramid add.
	// --- CHANGED: enable SELL pyramiding symmetry ---
	isAdd := len(t.lots) > 0 && allowPyramiding() && (d.Signal == Buy || d.Signal == Sell)

	// --- NEW: variables to capture gate audit fields for the reason string (side-biased; no winLow) ---
	var (
		reasonGatePrice float64
		reasonLatched   float64
		reasonEffPct    float64
		reasonBasePct   float64
		reasonElapsedHr float64
	)

	// Gating for pyramiding adds — spacing + adverse move (with optional time-decay).
	if isAdd {
		// 1) Spacing: always enforce (s=0 means no wait; set >0 to require time gap)
		s := pyramidMinSeconds()
		if time.Since(t.lastAdd) < time.Duration(s)*time.Second {
			t.mu.Unlock()
			hrs := time.Since(t.lastAdd).Hours()
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
			if !t.lastAdd.IsZero() {
				elapsedMin = time.Since(t.lastAdd).Minutes()
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

		// Time (in minutes) to hit the floor once (t_floor_min); used for latching thresholds.
		tFloorMin := 0.0
		if lambda > 0 && basePct > floor {
			tFloorMin = math.Log(basePct/floor) / lambda
		}

		last := t.latestEntry()
		if last > 0 {
			if d.Signal == Buy {
				// BUY adverse tracker
				if elapsedMin >= tFloorMin {
					if t.winLow == 0 || price < t.winLow {
						t.winLow = price
					}
				} else {
					t.winLow = 0
				}
				// latch at 2*t_floor_min
				if t.latchedGate == 0 && elapsedMin >= 2.0*tFloorMin && t.winLow > 0 {
					t.latchedGate = t.winLow
				}
				// baseline gate: last * (1 - effPct); latched replaces baseline
				gatePrice := last * (1.0 - effPct/100.0)
				if t.latchedGate > 0 {
					gatePrice = t.latchedGate
				}
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGate

				if !(price <= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (BUY); price=%.2f last_gate<=%.2f win_low=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winLow, effPct, basePct, reasonElapsedHr)
					return "HOLD", nil
				}
			} else { // SELL
				// SELL adverse tracker
				if elapsedMin >= tFloorMin {
					if t.winHigh == 0 || price > t.winHigh {
						t.winHigh = price
					}
				} else {
					t.winHigh = 0
				}
				if t.latchedGateShort == 0 && elapsedMin >= 2.0*tFloorMin && t.winHigh > 0 {
					t.latchedGateShort = t.winHigh
				}
				// baseline gate: last * (1 + effPct); latched replaces baseline
				gatePrice := last * (1.0 + effPct/100.0)
				if t.latchedGateShort > 0 {
					gatePrice = t.latchedGateShort
				}
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateShort

				if !(price >= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (SELL); price=%.2f last_gate>=%.2f win_high=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winHigh, effPct, basePct, reasonElapsedHr)
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
	quote := (riskPct / 100.0) * t.equityUSD
	if quote < t.cfg.OrderMinUSD {
		quote = t.cfg.OrderMinUSD
	}
	base := quote / price
	side := d.SignalToSide()

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// Reserve quote needed to close all existing short lots at current price.
		var reservedShortQuote float64
		for _, lot := range t.lots {
			if lot.Side == SideSell {
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
				return "HOLD", nil
			}
		}

		// Use the final neededQuote; recompute base.
		quote = neededQuote
		base = quote / price
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && requireBaseForShort() {
		// Sum reserved base for long lots
		var reservedLong float64
		for _, lot := range t.lots {
			if lot.Side == SideBuy {
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
				return "HOLD", nil
			}
		}
	}

	// Stops/takes (baseline for scalps)
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	if side == SideSell {
		stop = price * (1.0 + t.cfg.StopLossPct/100.0)
		take = price * (1.0 - t.cfg.TakeProfitPct/100.0)
	}

	// Decide if this new entry will be the runner (only when there is no existing runner).
	willBeRunner := (t.runnerIdx == -1 && len(t.lots) == 0)
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
		// This is a scalp add: compute k = number of existing scalps
		k := len(t.lots)
		if t.runnerIdx >= 0 && t.runnerIdx < len(t.lots) {
			k = len(t.lots) - 1 // exclude the runner from the scalp index
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
		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
		if err != nil {
			// Retry once with ORDER_MIN_USD on insufficient-funds style failures.
			e := strings.ToLower(err.Error())
			if quote > t.cfg.OrderMinUSD && (strings.Contains(e, "insufficient") || strings.Contains(e, "funds") || strings.Contains(e, "400")) {
				log.Printf("[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f", quote, err, t.cfg.OrderMinUSD)
				quote = t.cfg.OrderMinUSD
				base = quote / price
				placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
			}
			if err != nil {
				if t.cfg.Extended().UseDirectSlack {
					postSlack(fmt.Sprintf("ERR step: %v", err))
				}
				return "", err
			}
		}
		mtxOrders.WithLabelValues("live", string(side)).Inc()
		mtxTrades.WithLabelValues("open").Inc()
	} else {
		mtxTrades.WithLabelValues("open").Inc()
	}

	// Re-lock to mutate state (append new lot or first lot).
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
		// trailing fields default zero/false; they’ll be initialized if this becomes runner
	}
	t.lots = append(t.lots, newLot)
	// Use wall clock for lastAdd to drive spacing/decay even if candle time is zero.
	t.lastAdd = wallNow
	// Reset adverse tracking for the new add.
	t.winLow = priceToUse
	t.latchedGate = 0
	t.winHigh = priceToUse
	t.latchedGateShort = 0

	// Assign/designate runner if none exists yet; otherwise this is a scalp.
	if t.runnerIdx == -1 {
		t.runnerIdx = len(t.lots) - 1 // the just-added lot is runner
		// Initialize runner trailing baseline
		r := t.lots[t.runnerIdx]
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		// Ensure runner's stretched targets are applied (keeps baseline behavior for runner).
		t.applyRunnerTargets(r)
	}

	t.aggregateOpen()

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
	state := BotState{
		EquityUSD:      t.equityUSD,
		DailyStart:     t.dailyStart,
		DailyPnL:       t.dailyPnL,
		Lots:           t.lots,
		Model:          t.model,
		MdlExt:         t.mdlExt,
		WalkForwardMin: t.cfg.Extended().WalkForwardMin,
		LastFit:        t.lastFit,
		LastAdd:          t.lastAdd,
		WinLow:           t.winLow,
		LatchedGate:      t.latchedGate,
		WinHigh:          t.winHigh,
		LatchedGateShort: t.latchedGateShort,
	}
	bs, err := json.MarshalIndent(state, "", " ")
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
	} else {
		// keep t.equityUSD as initialized from cfg.USDEquity; live rebase will adjust later
	}
	t.dailyStart = st.DailyStart
	t.dailyPnL = st.DailyPnL
	t.lots = st.Lots
	if st.Model != nil {
		t.model = st.Model
	}
	if st.MdlExt != nil {
		t.mdlExt = st.MdlExt
	}
	if !st.LastFit.IsZero() {
		t.lastFit = st.LastFit
	}

	t.aggregateOpen()
	// Re-derive runnerIdx if not set (old state files won't carry it).
	if t.runnerIdx == -1 && len(t.lots) > 0 {
		t.runnerIdx = 0
		// Initialize trailing baseline for current runner if not already set
		r := t.lots[t.runnerIdx]
		if r.TrailPeak == 0 {
			// Initialize baseline to current open price if peak is unset.
			r.TrailPeak = r.OpenPrice
		}
	}

	// Restore pyramiding gate memory (if present in state file).
	t.lastAdd          = st.LastAdd
	t.winLow           = st.WinLow
	t.latchedGate      = st.LatchedGate
	t.winHigh          = st.WinHigh
	t.latchedGateShort = st.LatchedGateShort

	// --- Restart warmup for pyramiding decay/adverse tracking ---
	// If we restored with open lots but have no lastAdd, seed the decay clock to "now"
	// and reset adverse trackers/latches so they rebuild over real time (prevents instant latch).
	if len(t.lots) > 0 && t.lastAdd.IsZero() {
		t.lastAdd = time.Now().UTC()
		t.winLow = 0
		t.latchedGate = 0
		t.winHigh = 0
		t.latchedGateShort = 0
	}
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
