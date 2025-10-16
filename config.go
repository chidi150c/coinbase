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
	DryRun              bool
	MaxDailyLossPct     float64
	RiskPerTradePct     float64
	USDEquity           float64
	TakeProfitPct       float64
	StopLossPct         float64
	OrderMinUSD         float64 // legacy floor; still honored if MinNotional <= 0
	MinNotional         float64 // preferred exchange min notional (QUOTE); if >0, use this instead of OrderMinUSD
	LongOnly            bool    // prevent SELL entries when flat on spot
	RequireBaseForShort bool    // spot safety: require base inventory to short (default true)
	FeeRatePct          float64 // % fee applied on entry/exit trades

	// Normalized venue filters (optionally populated via env or bridge)
	PriceTick float64 // price tick size (QUOTE)
	BaseStep  float64 // base asset step (BASE)
	QuoteStep float64 // quote asset step (QUOTE)

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
	OrderType           string // "market" or "limit"
	LimitPriceOffsetBps float64    // maker price offset from mid in bps
	SpreadMinBps        int    // minimum spread (bps) to attempt maker entry
	LimitTimeoutSec     int    // cancel-and-market fallback timeout (seconds)

	// ---------- Migrated (Bucket B) knobs ----------

	// Pyramiding
	AllowPyramiding          bool
	PyramidMinSecondsBetween int
	PyramidMinAdversePct     float64
	PyramidDecayLambda       float64 // per-minute
	PyramidDecayMinPct       float64

	// TP-decay
	ScalpTPDecayEnable bool
	ScalpTPDecMode     string
	ScalpTPDecPct      float64
	ScalpTPDecayFactor float64
	ScalpTPMinPct      float64

	// USD trailing / profit gates
	TrailActivateUSDRunner  float64
	TrailActivateUSDScalp   float64
	TrailDistancePctRunner  float64
	TrailDistancePctScalp   float64
	ProfitGateUSD           float64
	TPMakerOffsetBps        float64 // maker offset for fixed-TP exits

	// Ramping
	RampEnable  bool
	RampMode    string
	RampStartPct float64
	RampStepPct  float64
	RampGrowth   float64
	RampMaxPct   float64

	// Equity/reporting/runtime
	ExitHistorySize  int
	PersistState     bool
	MaxConcurrentLots int

	// Paper/override helpers
	PaperBaseBalance float64
	BaseAsset        string
	PaperQuoteBalance float64
}

// loadConfigFromEnv reads the process env (already hydrated by loadBotEnv())
// and returns a Config with sane defaults if keys are missing.
func loadConfigFromEnv() Config {
	cfg := Config{
		ProductID:   getEnv("PRODUCT_ID", "BTC-USD"),
		Granularity: getEnv("GRANULARITY", "ONE_MINUTE"),

		// Universal, unprefixed knobs
		DryRun:              getEnvBool("DRY_RUN", true),
		MaxDailyLossPct:     getEnvFloat("MAX_DAILY_LOSS_PCT", 1.0),
		RiskPerTradePct:     getEnvFloat("RISK_PER_TRADE_PCT", 0.25),
		USDEquity:           getEnvFloat("USD_EQUITY", 1000.0),
		TakeProfitPct:       getEnvFloat("TAKE_PROFIT_PCT", 0.8),
		StopLossPct:         getEnvFloat("STOP_LOSS_PCT", 0.4),
		OrderMinUSD:         getEnvFloat("ORDER_MIN_USD", 5.00),
		MinNotional:         getEnvFloat("MIN_NOTIONAL", 0.0), // preferred when > 0
		LongOnly:            getEnvBool("LONG_ONLY", true),
		RequireBaseForShort: getEnvBool("REQUIRE_BASE_FOR_SHORT", true),
		FeeRatePct:          getEnvFloat("FEE_RATE_PCT", 0.3),

		// Venue filters (can be hydrated by bridge and/or env file)
		PriceTick: getEnvFloat("PRICE_TICK", 0.0),
		BaseStep:  getEnvFloat("BASE_STEP", 0.0),
		QuoteStep: getEnvFloat("QUOTE_STEP", 0.0),

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
		LimitPriceOffsetBps: getEnvFloat("LIMIT_PRICE_OFFSET_BPS", 5.0),
		SpreadMinBps:        getEnvInt("SPREAD_MIN_BPS", 2),
		LimitTimeoutSec:     getEnvInt("LIMIT_TIMEOUT_SEC", 5),

		// ---------- Migrated (Bucket B) defaults ----------
		// Pyramiding
		AllowPyramiding:          getEnvBool("ALLOW_PYRAMIDING", false),
		PyramidMinSecondsBetween: getEnvInt("PYRAMID_MIN_SECONDS_BETWEEN", 0),
		PyramidMinAdversePct:     getEnvFloat("PYRAMID_MIN_ADVERSE_PCT", 0.0),
		PyramidDecayLambda:       getEnvFloat("PYRAMID_DECAY_LAMBDA", 0.0),
		PyramidDecayMinPct:       getEnvFloat("PYRAMID_DECAY_MIN_PCT", 0.0),

		// TP-decay
		ScalpTPDecayEnable: getEnvBool("SCALP_TP_DECAY_ENABLE", false),
		ScalpTPDecMode:     getEnv("SCALP_TP_DEC_MODE", "linear"),
		ScalpTPDecPct:      getEnvFloat("SCALP_TP_DEC_PCT", 0.0),
		ScalpTPDecayFactor: getEnvFloat("SCALP_TP_DECAY_FACTOR", 1.0),
		ScalpTPMinPct:      getEnvFloat("SCALP_TP_MIN_PCT", 0.0),

		// USD trailing / profit gates
		TrailActivateUSDRunner: getEnvFloat("TRAIL_ACTIVATE_USD_RUNNER", 1.00),
		TrailActivateUSDScalp:  getEnvFloat("TRAIL_ACTIVATE_USD_SCALP", 0.50),
		TrailDistancePctRunner: getEnvFloat("TRAIL_DISTANCE_PCT_RUNNER", 0.40),
		TrailDistancePctScalp:  getEnvFloat("TRAIL_DISTANCE_PCT_SCALP", 0.25),
		ProfitGateUSD:          getEnvFloat("PROFIT_GATE_USD", 0.50),
		TPMakerOffsetBps:       getEnvFloat("TP_MAKER_OFFSET_BPS", 0.0),

		// Ramping
		RampEnable:  getEnvBool("RAMP_ENABLE", false),
		RampMode:    getEnv("RAMP_MODE", "linear"),
		RampStartPct: getEnvFloat("RAMP_START_PCT", 0.0),
		RampStepPct:  getEnvFloat("RAMP_STEP_PCT", 0.0),
		RampGrowth:   getEnvFloat("RAMP_GROWTH", 1.0),
		RampMaxPct:   getEnvFloat("RAMP_MAX_PCT", 0.0),

		// Equity/reporting/runtime
		ExitHistorySize:   getEnvInt("EXIT_HISTORY_SIZE", 8),
		PersistState:      getEnvBool("PERSIST_STATE", true),
		MaxConcurrentLots: getEnvInt("MAX_CONCURRENT_LOTS", 1_000_000),

		// Paper/override helpers
		PaperBaseBalance:  getEnvFloat("PAPER_BASE_BALANCE", 0.0),
		BaseAsset:         getEnv("BASE_ASSET", ""),
		PaperQuoteBalance: getEnvFloat("PAPER_QUOTE_BALANCE", 0.0),
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
		return 60
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
