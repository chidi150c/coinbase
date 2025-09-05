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

	// NEW: independent pyramiding anchor (stable reference, not tied to latest scalp)
	pyramidAnchorPrice float64
	pyramidAnchorTime  time.Time
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
	// Try to load state if exists
	if err := t.loadState(); err == nil {
		log.Printf("[INFO] trader state restored from %s", t.stateFile)
	} else {
		log.Printf("[INFO] no prior state restored: %v", err)
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
	// persist new state
	_ = t.saveState()
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
		_ = t.saveState()
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
func (t *Trader) closeLotAtIndex(ctx context.Context, c []Candle, idx int) (string, error) {
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
	exitFee := quoteExec * (t.cfg.FeeRatePct / 100.0)
	if placed != nil {
		if placed.CommissionUSD > 0 {
			exitFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (exit); falling back to FEE_RATE_PCT=%.4f%%", t.cfg.FeeRatePct)
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

	t.aggregateOpen()

	msg := fmt.Sprintf("EXIT %s at %.2f P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), priceExec, pl, lot.EntryFee+exitFee)
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	_ = t.saveState()

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

	now := c[len(c)-1].Time
	t.updateDaily(now)

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
					msg, err := t.closeLotAtIndex(ctx, c, i)
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
			if lot.Side == SideBuy && (price <= lot.Stop || price >= lot.Take) {
				trigger = true
			}
			if lot.Side == SideSell && (price >= lot.Stop || price <= lot.Take) {
				trigger = true
			}
			if trigger {
				msg, err := t.closeLotAtIndex(ctx, c, i)
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
	if len(t.lots) > 0 && d.Signal == Sell {
		t.mu.Unlock()
		return "HOLD", nil
	}
	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	price := c[len(c)-1].Close

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}
	if d.Signal == Flat {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT [%s]", d.Reason), nil
	}

	// Respect a hard cap on concurrent lots (runner + scalps). If at cap, do not add.
	if len(t.lots) >= maxConcurrentLots() {
		t.mu.Unlock()
		log.Printf("[DEBUG] lot cap reached (%d); HOLD", maxConcurrentLots())
		return "HOLD", nil
	}

	// Determine if we are opening first lot or attempting a pyramid add.
	isAdd := len(t.lots) > 0 && allowPyramiding() && d.Signal == Buy

	// Gating for pyramiding adds — spacing + adverse move (with optional time-decay).
	if isAdd {
		// 1) Spacing: always enforce (s=0 means no wait; set >0 to require time gap)
		s := pyramidMinSeconds()
		if time.Since(t.lastAdd) < time.Duration(s)*time.Second {
			t.mu.Unlock()
			log.Printf("[DEBUG] pyramid: blocked by spacing; since_last=%v need>=%ds", time.Since(t.lastAdd), s)
			return "HOLD", nil
		}

		// 2) Adverse move gate: ONLY vs last entry, with optional time-based exponential decay.
		basePct := pyramidMinAdversePct()
		effPct := basePct
		lambda := pyramidDecayLambda()
		elapsedMin := 0.0
		if lambda > 0 {
			if !t.lastAdd.IsZero() {
				elapsedMin = time.Since(t.lastAdd).Minutes()
			}
			decayed := basePct * math.Exp(-lambda*elapsedMin)
			floor := pyramidDecayMinPct()
			if decayed < floor {
				decayed = floor
			}
			effPct = decayed
		}

		last := t.latestEntry()
		if last > 0 {
			lastGate := last * (1.0 - effPct/100.0) // BUY: require drop of effPct from last entry
			if !(price <= lastGate) {
				t.mu.Unlock()
				log.Printf("[DEBUG] pyramid: blocked by last gate; price=%.2f last_gate<=%.2f base_pct=%.3f eff_pct=%.3f λ=%.4f elapsed_min=%.1f",
					+price, lastGate, basePct, effPct, lambda, elapsedMin)
				return "HOLD", nil
			}
		}
		// (runner-gap guard removed)
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

	// Stops/takes (baseline for scalps)
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	side := d.SignalToSide()
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
	entryFee := quote * (t.cfg.FeeRatePct / 100.0)
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
			if t.cfg.Extended().UseDirectSlack {
				postSlack(fmt.Sprintf("ERR step: %v", err))
			}
			return "", err
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
			log.Printf("[WARN] commission missing (entry); falling back to FEE_RATE_PCT=%.4f%%", t.cfg.FeeRatePct)
			entryFee = actualQuote * (t.cfg.FeeRatePct / 100.0)
		}
	} else {
		// DryRun path keeps previously computed entryFee and adjusts by delta as before.
	}

	if t.cfg.DryRun {
		// already deducted above for DryRun using quote; adjust to the actualQuote delta
		delta := (actualQuote - quote) * (t.cfg.FeeRatePct / 100.0)
		t.equityUSD -= delta
	}

	newLot := &Position{
		OpenPrice: priceToUse,
		Side:      side,
		SizeBase:  baseToUse,
		Stop:      stop,
		Take:      take,
		OpenTime:  now,
		EntryFee:  entryFee,
		// trailing fields default zero/false; they’ll be initialized if this becomes runner
	}
	t.lots = append(t.lots, newLot)
	t.lastAdd = now

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
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f stop=%.2f take=%.2f fee=%.4f [%s]",
			d.Signal, actualQuote, baseToUse, newLot.Stop, newLot.Take, entryFee, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s quote=%.2f stop=%.2f take=%.2f fee=%.4f [%s]",
			d.Signal, actualQuote, newLot.Stop, newLot.Take, entryFee, d.Reason)
	}
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	_ = t.saveState()
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
	if t.stateFile == "" {
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
	}
	bs, err := json.MarshalIndent(state, "", "  ")
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
	if t.stateFile == "" {
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
	t.equityUSD = st.EquityUSD
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
			r.TrailPeak = r.OpenPrice
		}
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
