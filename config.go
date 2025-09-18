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

import (
	"os"
	"strings"
)

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
	LongOnly        bool    // prevent SELL entries when flat on spot
	FeeRatePct      float64 // % fee applied on entry/exit trades

	// Ops
	Port              int
	BridgeURL         string // e.g., http://127.0.0.1:8787
	MaxHistoryCandles int    // plural: loaded from MAX_HISTORY_CANDLES
	StateFile         string // path to persist bot state (configurable via env)
}

// loadConfigFromEnv reads the process env (already hydrated by loadBotEnv())
// and returns a Config with sane defaults if keys are missing.
func loadConfigFromEnv() Config {
	cfg := Config{
		ProductID:         getEnv("PRODUCT_ID", "BTC-USD"),
		Granularity:       getEnv("GRANULARITY", "ONE_MINUTE"),
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
		BridgeURL:         getEnv("BRIDGE_URL", "http://127.0.0.1:8787"),
		MaxHistoryCandles: getEnvInt("MAX_HISTORY_CANDLES", 5000),
		StateFile:         getEnv("STATE_FILE", "/opt/coinbase/state/bot_state.json"),
	}

	// Broker-scoped overrides (e.g., BINANCE_*). If not present, keep existing values.
	broker := strings.ToUpper(strings.TrimSpace(getEnv("BROKER", "")))
	if broker != "" {
		cfg.FeeRatePct = getEnvFloat(broker+"_FEE_RATE_PCT", cfg.FeeRatePct)
		cfg.OrderMinUSD = getEnvFloat(broker+"_ORDER_MIN_USD", cfg.OrderMinUSD)
		cfg.DryRun = getEnvBool(broker+"_DRY_RUN", cfg.DryRun)
	}

	return cfg
}

// helpers to check presence without changing defaults
func hasEnv(key string) bool { return strings.TrimSpace(os.Getenv(key)) != "" }

// UseLiveEquity returns true if live balances should rebase equity.
// Per-broker override (e.g., BINANCE_USE_LIVE_EQUITY), falling back to global.
func (c *Config) UseLiveEquity() bool {
	b := strings.ToUpper(strings.TrimSpace(getEnv("BROKER", "")))
	if b != "" {
		return getEnvBool(b+"_USE_LIVE_EQUITY", getEnvBool("USE_LIVE_EQUITY", false))
	}
	return getEnvBool("USE_LIVE_EQUITY", false)
}

// Per-broker tick loop toggles (fallback to global if provided)
func (c *Config) UseTickPrice() bool {
	b := strings.ToUpper(strings.TrimSpace(getEnv("BROKER", "")))
	if b != "" {
		return getEnvBool(b+"_USE_TICK_PRICE", getEnvBool("USE_TICK_PRICE", false))
	}
	return getEnvBool("USE_TICK_PRICE", false)
}
func (c *Config) TickIntervalSec() int {
	b := strings.ToUpper(strings.TrimSpace(getEnv("BROKER", "")))
	if b != "" {
		return getEnvInt(b+"_TICK_INTERVAL_SEC", getEnvInt("TICK_INTERVAL_SEC", 1))
	}
	return getEnvInt("TICK_INTERVAL_SEC", 1)
}
func (c *Config) CandleResyncSec() int {
	b := strings.ToUpper(strings.TrimSpace(getEnv("BROKER", "")))
	if b != "" {
		return getEnvInt(b+"_CANDLE_RESYNC_SEC", getEnvInt("CANDLE_RESYNC_SEC", 60))
	}
	return getEnvInt("CANDLE_RESYNC_SEC", 60)
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

// --- NEW (minimal, optional): trailing environment accessors ---
// Safe defaults ensure no behavior change unless the envs are set.
func (c *Config) TrailActivatePct() float64 { return getEnvFloat("TRAIL_ACTIVATE_PCT", 1.2) }
func (c *Config) TrailDistancePct() float64 { return getEnvFloat("TRAIL_DISTANCE_PCT", 0.6) }
