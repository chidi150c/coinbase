Generate a full copy of {{
// FILE: config.go
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

// NOTE: All knobs here are now read from UNPREFIXED env keys. Broker-specific
// API creds remain broker-prefixed (e.g., BINANCE_API_KEY/SECRET, HITBTC_API_KEY/SECRET)
// and are consumed by the respective broker clients, not here.

import "strings"

// Config holds all runtime knobs for trading and operations.
type Config struct {
	// Trading target
	ProductID   string // e.g., "BTC-USD" or "BTCUSDT"
	Granularity string // e.g., "ONE_MINUTE"

	// Safety & sizing
	DryRun          bool
	MaxDailyLossPct float64
	RiskPerTradePct float64
	USDEquity       float64
	TakeProfitPct   float64
	StopLossPct     float64
	OrderMinUSD     float64
	LongOnly        bool    // prevent SELL entries when flat on spot
	FeeRatePct      float64 // % fee applied on entry/exit trades

	// Ops
	Port              int
	BridgeURL         string // optional: http://127.0.0.1:8787 (only when using the bridge)
	MaxHistoryCandles int    // plural: loaded from MAX_HISTORY_CANDLES
	StateFile         string // path to persist bot state

	// Loop control (unprefixed; universal)
	UseTickPrice    bool // enable tick-driven loop
	TickIntervalSec int  // per-tick cadence
	CandleResyncSec int  // periodic candle resync in tick loop

	// Live equity gating (unprefixed; universal)
	LiveEquity bool // if true, rebase & refresh equity from live balances

	// Order entry (unprefixed; universal)
	OrderType            string // "market" or "limit"
	LimitPriceOffsetBps  int    // maker price offset from mid in bps
	SpreadMinBps         int    // minimum spread (bps) to attempt maker entry
	LimitTimeoutSec      int    // cancel-and-market fallback timeout (seconds)
}

// loadConfigFromEnv reads the process env (already hydrated by loadBotEnv())
// and returns a Config with sane defaults if keys are missing.
func loadConfigFromEnv() Config {
	cfg := Config{
		ProductID:         getEnv("PRODUCT_ID", "BTC-USD"),
		Granularity:       getEnv("GRANULARITY", "ONE_MINUTE"),

		// Universal, unprefixed knobs
		DryRun:            getEnvBool("DRY_RUN", true),
		MaxDailyLossPct:   getEnvFloat("MAX_DAILY_LOSS_PCT", 1.0),
		RiskPerTradePct:   getEnvFloat("RISK_PER_TRADE_PCT", 0.25),
		USDEquity:         getEnvFloat("USD_EQUITY", 1000.0),
		TakeProfitPct:     getEnvFloat("TAKE_PROFIT_PCT", 0.8),
		StopLossPct:       getEnvFloat("STOP_LOSS_PCT", 0.4),
		OrderMinUSD:       getEnvFloat("ORDER_MIN_USD", 5.00),
		LongOnly:          getEnvBool("LONG_ONLY", true),
		FeeRatePct:        getEnvFloat("FEE_RATE_PCT", 0.3),

		Port:              getEnvInt("PORT", 8080),
		BridgeURL:         getEnv("BRIDGE_URL", ""),
		MaxHistoryCandles: getEnvInt("MAX_HISTORY_CANDLES", 5000),
		StateFile:         getEnv("STATE_FILE", "/opt/coinbase/state/bot_state.json"),

		// Loop control
		UseTickPrice:    getEnvBool("USE_TICK_PRICE", false),
		TickIntervalSec: getEnvInt("TICK_INTERVAL_SEC", 1),
		CandleResyncSec: getEnvInt("CANDLE_RESYNC_SEC", 60),

		// Live equity
		LiveEquity: getEnvBool("USE_LIVE_EQUITY", false),

		// Order entry
		OrderType:           getEnv("ORDER_TYPE", "market"),
		LimitPriceOffsetBps: getEnvInt("LIMIT_PRICE_OFFSET_BPS", 5),
		SpreadMinBps:        getEnvInt("SPREAD_MIN_BPS", 2),
		LimitTimeoutSec:     getEnvInt("LIMIT_TIMEOUT_SEC", 5),
	}

	// Historical carry-over: if someone still sets BROKER=X, we may still
	// want to validate it's present, but we no longer use it to select knobs.
	_ = strings.TrimSpace(getEnv("BROKER", ""))

	return cfg
}

// ---- cfg helpers (getter methods) ----
// These fetch from env at call-time (falling back to the struct's initial values).
// This lets you tweak knobs live IF your process env is refreshed.

func (c *Config) UseLiveEquity() bool {
	return getEnvBool("USE_LIVE_EQUITY", c.LiveEquity)
}

func (c *Config) UseTick() bool {
	return getEnvBool("USE_TICK_PRICE", c.UseTickPrice)
}

func (c *Config) TickInterval() int {
	// Default to initial value; clamp to >=1
	ti := getEnvInt("TICK_INTERVAL_SEC", c.TickIntervalSec)
	if ti <= 0 {
		ti = 1
	}
	return ti
}

func (c *Config) CandleResync() int {
	cr := getEnvInt("CANDLE_RESYNC_SEC", c.CandleResyncSec)
	if cr <= 0 {
		cr = 60
	}
	return cr
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

// --- trailing env accessors (unchanged; universal) ---
func (c *Config) TrailActivatePct() float64 { return getEnvFloat("TRAIL_ACTIVATE_PCT", 1.2) }
func (c *Config) TrailDistancePct() float64 { return getEnvFloat("TRAIL_DISTANCE_PCT", 0.6) }

}} with only the necessary minimal changes to implement {{add EXCHANGE_MIN_NOTIONAL_USD environment variable (default 10.00) in env.go/config.go}}. Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()). Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline. Only apply the minimal edits required to implement {{add EXCHANGE_MIN_NOTIONAL_USD environment variable (default 10.00) in env.go/config.go}}. Return the complete file, copy-paste ready, in IDE.
