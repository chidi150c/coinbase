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
}

// BotState is the persistent snapshot of trader state.
type BotState struct {
	EquityUSD  float64
	DailyStart time.Time
	DailyPnL   float64
	Lots       []*Position
	Model      *AIMicroModel
}

type Trader struct {
	cfg        Config
	broker     Broker
	model      *AIMicroModel
	pos        *Position        // kept for backward compatibility with earlier logic
	lots       []*Position      // NEW: multiple lots when pyramiding is enabled
	lastAdd    time.Time        // NEW: last time a pyramid add was placed
	dailyStart time.Time
	dailyPnL   float64
	mu         sync.Mutex
	equityUSD  float64

	// NEW (minimal): optional extended head passed through to decide(); nil if unused.
	mdlExt *ExtendedLogit

	// NEW: path to persisted state file
	stateFile string
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,
	}
	// Try to load state if exists
	if err := t.loadState(); err == nil {
		log.Printf("[INFO] trader state restored from %s", t.stateFile)
	} else {
		log.Printf("[INFO] no prior state restored: %v", err)
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

// closeLotAtIndex closes a single lot at idx (assumes mutex held), performing I/O unlocked.
func (t *Trader) closeLotAtIndex(ctx context.Context, c []Candle, idx int) (string, error) {
	price := c[len(c)-1].Close
	lot := t.lots[idx]
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}
	base := lot.SizeBase
	quote := base * price

	// unlock for I/O
	t.mu.Unlock()
	if !t.cfg.DryRun {
		if _, err := t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote); err != nil {
			if t.cfg.Extended().UseDirectSlack {
				postSlack(fmt.Sprintf("ERR step: %v", err))
			}
			return "", fmt.Errorf("close order failed: %w", err)
		}
		mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
	}
	// re-lock
	t.mu.Lock()
	// refresh price snapshot (best-effort)
	price = c[len(c)-1].Close

	pl := (price - lot.OpenPrice) * base
	if lot.Side == SideSell {
		pl = (lot.OpenPrice - price) * base
	}

	// --- NEW: apply exit fee ---
	exitFee := quote * (t.cfg.FeeRatePct / 100.0)
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

	// remove lot idx
	t.lots = append(t.lots[:idx], t.lots[idx+1:]...)
	t.aggregateOpen()

	msg := fmt.Sprintf("EXIT %s at %.2f P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), price, pl, lot.EntryFee+exitFee)
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	_ = t.saveState()
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
	log.Printf("[DEBUG] Lots=%d, Decision=%s Reason=%s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v", len(t.lots), d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)

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

	// Determine if we are opening first lot or attempting a pyramid add.
	isAdd := len(t.lots) > 0 && allowPyramiding() && d.Signal == Buy

	// Gating for pyramiding adds — NOW MANDATORY: spacing + adverse move.
	if isAdd {
		// 1) Spacing: always enforce (s=0 means no wait; set >0 to require time gap)
		s := pyramidMinSeconds()
		if time.Since(t.lastAdd) < time.Duration(s)*time.Second {
			t.mu.Unlock()
			log.Printf("[DEBUG] pyramid: blocked by spacing; since_last=%v need>=%ds", time.Since(t.lastAdd), s)
			return "HOLD", nil
		}

		// 2) Adverse move vs last entry: always enforce
		//    (pct=0 means price must be <= last entry; set >0 for a stricter drop)
		pct := pyramidMinAdversePct()
		last := t.latestEntry()
		if last > 0 {
			minDrop := last * (1.0 - pct/100.0)
			if !(price <= minDrop) {
				t.mu.Unlock()
				log.Printf("[DEBUG] pyramid: blocked by adverse%%; price=%.2f last=%.2f need<=%.2f (%.3f%%)",
					price, last, minDrop, pct)
				return "HOLD", nil
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

	// Stops/takes
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	side := d.SignalToSide()
	if side == SideSell {
		stop = price * (1.0 + t.cfg.StopLossPct/100.0)
		take = price * (1.0 - t.cfg.TakeProfitPct/100.0)
	}

	// --- apply entry fee ---
	entryFee := quote * (t.cfg.FeeRatePct / 100.0)
	if t.cfg.DryRun {
		t.equityUSD -= entryFee
	}

	// Place live order without holding the lock.
	t.mu.Unlock()
	if !t.cfg.DryRun {
		if _, err := t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote); err != nil {
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
	newLot := &Position{
		OpenPrice: price,
		Side:      side,
		SizeBase:  base,
		Stop:      stop,
		Take:      take,
		OpenTime:  now,
		EntryFee:  entryFee,
	}
	t.lots = append(t.lots, newLot)
	t.lastAdd = now
	t.aggregateOpen()

	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f stop=%.2f take=%.2f fee=%.4f [%s]",
			d.Signal, quote, base, stop, take, entryFee, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s quote=%.2f stop=%.2f take=%.2f fee=%.4f [%s]",
			d.Signal, quote, stop, take, entryFee, d.Reason)
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
		EquityUSD:  t.equityUSD,
		DailyStart: t.dailyStart,
		DailyPnL:   t.dailyPnL,
		Lots:       t.lots,
		Model:      t.model,
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
	t.aggregateOpen()
	return nil
}

// ---- Phase-7 helpers (append-only; optional features) ----

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
