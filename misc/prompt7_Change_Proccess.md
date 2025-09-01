Generate a full copy of {{// FILE: trader.go
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
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	return &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
	}
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
	}
}

func (t *Trader) canTrade() bool {
	limit := t.cfg.MaxDailyLossPct / 100.0 * t.equityUSD
	return t.dailyPnL > -limit
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
		// Still have at least one lot; HOLD for this tick unless we’re adding (pyramiding) below.
		// We intentionally fall through to allow an additional BUY if enabled.
	}

	// Flat (no lots) or pyramiding add both must respect the daily loss circuit breaker.
	// if !t.canTrade() {
	// 	if t.cfg.Extended().UseDirectSlack {
	// 		postSlack("CIRCUIT_BREAKER_DAILY_LOSS — trading paused")
	// 	}
	// 	t.mu.Unlock()
	// 	return "CIRCUIT_BREAKER_DAILY_LOSS", nil
	// }

	// Make a decision (MINIMAL change: pass optional extended model).
	d := decide(c, t.model, t.mdlExt)
	log.Printf("[DEBUG] Lots=%d, Decision=%s Reason=%s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v", len(t.lots), d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)
	
	// --- NEW (minimal): ignore discretionary SELL signals while lots are open; exits are TP/SL only.
	if len(t.lots) > 0 && d.Signal == Sell {
		t.mu.Unlock()
		return "HOLD", nil
	}
	// ----------------------------------------------------------------------
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
}}, {{// FILE: config.go
// Package main – Runtime configuration model and loader.
//
// This file defines the Config struct (all the knobs your bot uses) and a
// helper to populate it from environment variables. The .env file is read
// by loadBotEnv() (see env.go), so you can tune behavior without exports.
//
// Typical flow (see main.go):
//   loadBotEnv()
//   initThresholdsFromEnv()
//   cfg := loadConfigFromEnv()
package main

// Config holds all runtime knobs for trading and operations.
type Config struct {
	// Trading target
	ProductID   string // e.g., "BTC-USD"
	Granularity string // e.g., "ONE_MINUTE"

	// Safety
	DryRun          bool
	MaxDailyLossPct float64
	RiskPerTradePct float64
	USDEquity       float64
	TakeProfitPct   float64
	StopLossPct     float64
	OrderMinUSD     float64
	LongOnly        bool // prevent SELL entries when flat on spot
	FeeRatePct float64 // new: % fee applied on entry/exit trades

	// Ops
	Port      int
	BridgeURL string // e.g., http://127.0.0.1:8787
	MaxHistoryCandle int
}

// loadConfigFromEnv reads the process env (already hydrated by loadBotEnv())
// and returns a Config with sane defaults if keys are missing.
func loadConfigFromEnv() Config {
	return Config{
		ProductID:       getEnv("PRODUCT_ID", "BTC-USD"),
		Granularity:     getEnv("GRANULARITY", "ONE_MINUTE"),
		DryRun:          getEnvBool("DRY_RUN", true),
		MaxDailyLossPct: getEnvFloat("MAX_DAILY_LOSS_PCT", 1.0),
		RiskPerTradePct: getEnvFloat("RISK_PER_TRADE_PCT", 0.25),
		USDEquity:       getEnvFloat("USD_EQUITY", 1000.0),
		TakeProfitPct:   getEnvFloat("TAKE_PROFIT_PCT", 0.8),
		StopLossPct:     getEnvFloat("STOP_LOSS_PCT", 0.4),
		OrderMinUSD:     getEnvFloat("ORDER_MIN_USD", 5.00),
		LongOnly:        getEnvBool("LONG_ONLY", true),
		FeeRatePct:      getEnvFloat("FEE_RATE_PCT", 0.3), // new
		Port:            getEnvInt("PORT", 8080),
		BridgeURL:       getEnv("BRIDGE_URL", "http://127.0.0.1:8787"),
		MaxHistoryCandle: getEnvInt("MAX_HISTORY_CANDLES", 5000),
	}
}

// UseLiveEquity returns true if live balances should rebase equity.
func (c *Config) UseLiveEquity() bool {
	return getEnvBool("USE_LIVE_EQUITY", false)
}

// ---- Phase-7 toggles (append-only; no behavior changes unless envs set) ----

// ModelMode selects the prediction path; baseline is the default.
type ModelMode string

const (
	ModelModeBaseline ModelMode = "baseline"
	ModelModeExtended ModelMode = "extended"
)

// ExtendedToggles exposes optional Phase-7 features without altering existing behavior.
type ExtendedToggles struct {
	ModelMode      ModelMode // baseline (default) or extended
	WalkForwardMin int       // minutes between live refits; 0 disables
	VolRiskAdjust  bool      // enable volatility-aware risk sizing
	UseDirectSlack bool      // true if SLACK_WEBHOOK is set (optional direct pings)
}

// Extended reads optional Phase-7 toggles from env. Defaults preserve baseline behavior.
func (c *Config) Extended() ExtendedToggles {
	mm := ModelMode(getEnv("MODEL_MODE", string(ModelModeBaseline)))
	if mm != ModelModeExtended {
		mm = ModelModeBaseline
	}
	return ExtendedToggles{
		ModelMode:      mm,
		WalkForwardMin: getEnvInt("WALK_FORWARD_MIN", 0),
		VolRiskAdjust:  getEnvBool("VOL_RISK_ADJUST", false),
		UseDirectSlack: getEnv("SLACK_WEBHOOK", "") != "",
	}
}
}}, {{// FILE: env.go
// Package main – Environment helpers for the trading bot.
//
// This file provides:
//   1) Small helpers to read environment variables with sane defaults
//      (strings, ints, floats, bools).
//   2) A safe loader (loadBotEnv) that reads /opt/coinbase/env/bot.env only,
//      ignoring secrets meant for the Python bridge.
//   3) Strategy threshold knobs (buyThreshold, sellThreshold, useMAFilter) and an
//      initializer (initThresholdsFromEnv) so you can tune behavior via env
//      without recompiling.
//
// Notes:
//   • The bot never requires `export $(cat .env ...)`.
//   • The Python FastAPI sidecar uses its own /opt/coinbase/env/bridge.env.

package main

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
)

// --------- Env helpers (used across files) ---------

func getEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
func getEnvFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
func getEnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "y", "yes":
		return true
	case "0", "false", "n", "no":
		return false
	case "":
		return def
	default:
		return def
	}
}
func getEnvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

// --------- .env loader (bot-only) ---------

// loadBotEnv reads /opt/coinbase/env/bot.env and sets ONLY the keys the Go bot needs.
// It won't override variables already in the environment and ignores secrets not required.
func loadBotEnv() {
	path := "/opt/coinbase/env/bot.env"
	f, err := os.Open(path)
	if err != nil {
		log.Printf("env: %s not found, relying on process env", path)
		return
	}
	defer f.Close()

	needed := map[string]struct{}{
		"PRODUCT_ID": {}, "GRANULARITY": {}, "DRY_RUN": {}, "MAX_DAILY_LOSS_PCT": {},
		"RISK_PER_TRADE_PCT": {}, "USD_EQUITY": {}, "TAKE_PROFIT_PCT": {},
		"STOP_LOSS_PCT": {}, "ORDER_MIN_USD": {}, "LONG_ONLY": {}, "PORT": {}, "BRIDGE_URL": {},
		"BUY_THRESHOLD": {}, "SELL_THRESHOLD": {}, "USE_MA_FILTER": {}, "BACKTEST_SLEEP_MS": {},
		// ---- new, opt-in pyramiding/env-driven toggles ----
		"ALLOW_PYRAMIDING":             {},
		"PYRAMID_MIN_SECONDS_BETWEEN":  {},
		"PYRAMID_MIN_ADVERSE_PCT":      {},
		"USE_TICK_PRICE":               {},
		"TICK_INTERVAL_SEC":            {},
		"CANDLE_RESYNC_SEC":            {},
		"DAILY_BREAKER_MARK_TO_MARKET": {},
		"FEE_RATE_PCT": {},
	}

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[len("export "):])
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if _, ok := needed[key]; !ok {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if idx := strings.Index(val, "#"); idx >= 0 {
			val = strings.TrimSpace(val[:idx])
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
	log.Printf("env: loaded %s", path)
}

// --------- Tunable strategy thresholds (initialized in main) ---------

var (
	buyThreshold  float64
	sellThreshold float64
	useMAFilter   bool
)

func initThresholdsFromEnv() {
	buyThreshold = getEnvFloat("BUY_THRESHOLD", 0.55)
	sellThreshold = getEnvFloat("SELL_THRESHOLD", 0.45)
	useMAFilter = getEnvBool("USE_MA_FILTER", true)
}
}} and {{version: "3.9"

services:
  bot:
    image: golang:1.23
    working_dir: /app
    volumes:
      - ..:/app
      - /opt/coinbase/env:/opt/coinbase/env:ro 
    env_file:
      - /opt/coinbase/env/bot.env
    command: ["/usr/local/go/bin/go","run",".","-live","-interval","1"]
    #command: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1
    restart: unless-stopped
    expose:
      - "8080"
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    networks:
      monitoring_network:
        aliases: [bot, coinbase-bot]

  bridge:
    build:
      context: ../bridge
      dockerfile: Dockerfile
    image: coinbase-bridge:curl
    working_dir: /app/bridge
    volumes:
      - ../bridge:/app/bridge:ro
      - /opt/coinbase/env:/opt/coinbase/env:ro 
    env_file:
      - /opt/coinbase/env/bridge.env
    # use CMD from Dockerfile (no inline command override)
    expose:
      - "8787"
    restart: unless-stopped
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    networks:
      monitoring_network:
        aliases: [bridge]

  prometheus:
    image: prom/prometheus:latest
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml
      - monitoring_prometheus_data:/prometheus
    restart: unless-stopped
    networks:
      - monitoring_network

  alertmanager:
    image: prom/alertmanager:latest
    ports:
      - "9093:9093"
    volumes:
      - ./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro
    restart: unless-stopped
    networks:
      - monitoring_network

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3000:3000"
    environment:
      GF_SECURITY_ADMIN_USER: admin
      GF_SECURITY_ADMIN_PASSWORD: admin
    volumes:
      - monitoring_grafana_data:/var/lib/grafana
    depends_on:
      - prometheus
      - bot
      - bridge
    networks:
      - monitoring_network

volumes:
  monitoring_prometheus_data:
  monitoring_grafana_data:

networks:
  monitoring_network:
    driver: bridge
}}  with only the necessary minimal changes to implement {{CHANGE_DESCRIPTION: add state serialization in trader.go by saving equity, open positions, and model weights into a JSON state file on each tick and reload them on startup if present.

CHANGE_DESCRIPTION: update config.go to include STATE_FILE path (default /opt/coinbase/state/bot_state.json) configurable via env.

CHANGE_DESCRIPTION: add persistence directory volume in monitoring/docker-compose.yml for the bot container to mount /opt/coinbase/state.}}.
Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()).
Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline.
Only apply the minimal edits required to implement {{CHANGE_DESCRIPTION: add state serialization in trader.go by saving equity, open positions, and model weights into a JSON state file on each tick and reload them on startup if present.

CHANGE_DESCRIPTION: update config.go to include STATE_FILE path (default /opt/coinbase/state/bot_state.json) configurable via env.

CHANGE_DESCRIPTION: add persistence directory volume in monitoring/docker-compose.yml for the bot container to mount /opt/coinbase/state.}}.
Return the complete file, copy-paste ready.