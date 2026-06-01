// FILE: config.go
// Package main – Runtime configuration model and loader.
//
// This file defines the Config struct (all the knobs your bot uses) and a
// helper to populate it from environment variables. The .env file is read
// by loadBotEnv() (see env.go), so you can tune behavior without exports.
//
// Typical flow (see main.go):
//
//	loadBotEnv()
//	cfg := loadConfigFromEnv()
package main

// NOTE: All knobs here are now read from UNPREFIXED env keys. Broker-specific
// API creds remain broker-prefixed (e.g., BINANCE_API_KEY/SECRET, HITBTC_API_KEY/SECRET)
// and are consumed by the respective broker clients, not here.

import (
	"log"
	"strings"
)

// Config holds all runtime knobs for trading and operations.
type Config struct {
	// Trading target
	ProductID string // e.g., "BTC-USD" or "BTCUSDT"
	GateTF    string // e.g., "ONE_MINUTE"

	// Safety & sizing
	DryRun              bool
	MaxDailyLossPct     float64
	RiskPerTradeUSD     float64
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
	OrderType           string  // "market" or "limit"
	LimitPriceOffsetBps float64 // maker price offset from mid in bps
	SpreadMinBps        float64 // minimum spread (bps) to attempt maker entry
	LimitTimeoutSec     int     // cancel-and-market fallback timeout (seconds)

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
	TrailActivateUSDRunner float64
	TrailActivateUSDScalp  float64
	TrailDistancePctRunner float64
	TrailDistancePctScalp  float64
	ProfitGateUSD          float64
	TPMakerOffsetBps       float64 // maker offset for fixed-TP exits

	// Ramping
	RampEnable   bool
	RampMode     string
	RampStartPct float64
	RampStepPct  float64
	RampGrowth   float64
	RampMaxPct   float64

	// Equity/reporting/runtime
	ExitHistorySize   int
	PersistState      bool
	MaxConcurrentLots int

	// Paper/override helpers
	PaperBaseBalance  float64
	BaseAsset         string
	PaperQuoteBalance float64

	// Repricer (maker-chase) guardrails
	RepriceEnable         bool
	RepriceIntervalMs     int
	RepriceMinImprovTicks int
	RepriceMinEdgeUSD     float64
	RepriceMaxDriftBps    float64
	RepriceMaxCount       int

	// Strategy thresholds
	BuyThreshold     float64
	SellThreshold    float64
	UseMAFilter      bool
	UseMACDSlopeGate bool
	MACDLineEPS      float64
	AIFeatureDim     int

	// Unified AI — fee-aware horizon labels
	AILabelHorizon int
	AIMinEdgePct   float64

	// Runtime optional toggles retained from the old Extended() holder, but no model mode.
	WalkForwardMin int  // minutes between live refits; 0 disables
	VolRiskAdjust  bool // enable volatility-aware risk sizing
	UseDirectSlack bool // true if SLACK_WEBHOOK is set

	// Mirror (Gate2 bypass) ramping
	MirrorEnabled      bool    // turn on/off
	MirrorGateUSD      float64 // base gate, e.g., 0.020
	MirrorGateSlopeUSD float64 // per-index increment, e.g., 0.003
	MirrorGateStartIdx int     // start ramping at this nearest idx, e.g., 2
	MirrorGateMaxUSD   float64 // cap the gate, e.g., 0.050
}

// loadConfigFromEnv reads the process env (already hydrated by loadBotEnv())
// and returns a Config with sane defaults if keys are missing.
func loadConfigFromEnv() Config {
	cfg := Config{
		ProductID: getEnv("PRODUCT_ID", "BTC-USD"),
		GateTF:    getEnv("GATE_TF", "ONE_MINUTE"),

		// Universal, unprefixed knobs
		DryRun:              getEnvBool("DRY_RUN", true),
		MaxDailyLossPct:     getEnvFloat("MAX_DAILY_LOSS_PCT", 1.0),
		RiskPerTradeUSD:     getEnvFloat("RISK_PER_TRADE_USD", 80.0),
		USDEquity:           getEnvFloat("USD_EQUITY", 1000.0),
		TakeProfitPct:       getEnvFloat("TAKE_PROFIT_PCT", 0.8),
		StopLossPct:         getEnvFloat("STOP_LOSS_PCT", 0.4),
		OrderMinUSD:         getEnvFloat("ORDER_MIN_USD", 5.00),
		MinNotional:         getEnvFloat("MIN_NOTIONAL", 10.0), // preferred when > 0
		LongOnly:            getEnvBool("LONG_ONLY", true),
		RequireBaseForShort: getEnvBool("REQUIRE_BASE_FOR_SHORT", true),
		FeeRatePct:          getEnvFloat("FEE_RATE_PCT", 0.10),

		// Venue filters (can be hydrated by bridge and/or env file)
		PriceTick: getEnvFloat("PRICE_TICK", 0.01),
		BaseStep:  getEnvFloat("BASE_STEP", 0.000001),
		QuoteStep: getEnvFloat("QUOTE_STEP", 0.01),

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
		SpreadMinBps:        getEnvFloat("SPREAD_MIN_BPS", 2.0),
		LimitTimeoutSec:     getEnvInt("LIMIT_TIMEOUT_SEC", 60),

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
		RampEnable:   getEnvBool("RAMP_ENABLE", false),
		RampMode:     getEnv("RAMP_MODE", "linear"),
		RampStartPct: getEnvFloat("RAMP_START_PCT", 0.0),
		RampStepPct:  getEnvFloat("RAMP_STEP_PCT", 0.0),
		RampGrowth:   getEnvFloat("RAMP_GROWTH", 1.0),
		RampMaxPct:   getEnvFloat("RAMP_MAX_PCT", 0.0),

		// Equity/reporting/runtime
		ExitHistorySize:   getEnvInt("EXIT_HISTORY_SIZE", 8),
		PersistState:      getEnvBool("PERSIST_STATE", true),
		MaxConcurrentLots: getEnvInt("MAX_CONCURRENT_LOTS", 8),

		// Paper/override helpers
		PaperBaseBalance:  getEnvFloat("PAPER_BASE_BALANCE", 0.0),
		BaseAsset:         getEnv("BASE_ASSET", ""),
		PaperQuoteBalance: getEnvFloat("PAPER_QUOTE_BALANCE", 0.0),

		// Repricer (maker-chase) guardrails
		RepriceEnable:         getEnvBool("REPRICE_ENABLE", true),
		RepriceIntervalMs:     getEnvInt("REPRICE_INTERVAL_MS", 1000),
		RepriceMinImprovTicks: getEnvInt("REPRICE_MIN_IMPROV_TICKS", 0),
		RepriceMinEdgeUSD:     getEnvFloat("REPRICE_MIN_EDGE_USD", 0.0),
		RepriceMaxDriftBps:    getEnvFloat("REPRICE_MAX_DRIFT_BPS", 1.5),
		RepriceMaxCount:       getEnvInt("REPRICE_MAX_COUNT", 0),

		// Strategy thresholds
		BuyThreshold:     getEnvFloat("BUY_THRESHOLD", 0.55),
		SellThreshold:    getEnvFloat("SELL_THRESHOLD", 0.45),
		UseMAFilter:      getEnvBool("USE_MA_FILTER", true),
		UseMACDSlopeGate: getEnvBool("USE_MACD_SLOPE_GATE", false),
		MACDLineEPS:      getEnvFloat("MACD_LINE_EPS", 0.0),
		AIFeatureDim:     getEnvInt("AI_FEATURE_DIM", 18),

		// Unified AI — fee-aware labels
		AILabelHorizon: getEnvInt("AI_LABEL_HORIZON", 15),
		AIMinEdgePct:   getEnvFloat("AI_MIN_EDGE_PCT", 0.05),

		// Runtime optional toggles
		WalkForwardMin: getEnvInt("WALK_FORWARD_MIN", 0),
		VolRiskAdjust:  getEnvBool("VOL_RISK_ADJUST", false),
		UseDirectSlack: getEnv("SLACK_WEBHOOK", "") != "",

		// Mirror (Gate2 bypass) ramping
		MirrorEnabled:      getEnvBool("MIRROR_ENABLED", true),
		MirrorGateUSD:      getEnvFloat("MIRROR_GATE_USD", 0.0),
		MirrorGateSlopeUSD: getEnvFloat("MIRROR_GATE_SLOPE_USD", 0.0),
		MirrorGateStartIdx: getEnvInt("MIRROR_GATE_START_IDX", 0),
		MirrorGateMaxUSD:   getEnvFloat("MIRROR_GATE_MAX_USD", 0.0),
	}

	// sensible defaults if unset
	if cfg.MirrorGateUSD == 0 {
		cfg.MirrorGateUSD = cfg.ProfitGateUSD
	}
	if cfg.MirrorGateSlopeUSD == 0 {
		cfg.MirrorGateSlopeUSD = 0.25 * cfg.MirrorGateUSD
	}
	if cfg.MirrorGateStartIdx == 0 {
		cfg.MirrorGateStartIdx = 1
	}
	if cfg.MirrorGateMaxUSD == 0 {
		cfg.MirrorGateMaxUSD = cfg.ProfitGateUSD + cfg.MirrorGateSlopeUSD*8
	}

	// dependency defaults
	if cfg.TrailActivateUSDRunner == 0 {
		cfg.TrailActivateUSDRunner = 8.0 * cfg.ProfitGateUSD
	}
	if cfg.TrailActivateUSDScalp == 0 {
		cfg.TrailActivateUSDScalp = 4.0 * cfg.ProfitGateUSD
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

func (c *Config) SignalTF() string {
	tf := strings.ToLower(
		strings.TrimSpace(
			getEnv("AI_SIGNAL_TF", "5m"),
		),
	)

	if timeframeToGranularity(tf) == "" {
		log.Printf(
			"[WARN] invalid AI_SIGNAL_TF=%s fallback=5m",
			tf,
		)
		return "5m"
	}

	return tf
}

func (c *Config) SetPriceTick() {
	pt := getEnvFloat("PRICE_TICK", 0.01)
	c.PriceTick = pt
}

func (c *Config) SetBaseStep() {
	pt := getEnvFloat("BASE_STEP", 0.000001)
	c.BaseStep = pt
}

func (c *Config) SetQuoteStep() {
	pt := getEnvFloat("QUOTE_STEP", 0.01)
	c.QuoteStep = pt
}

func (c *Config) SetMinNotional() {
	pt := getEnvFloat("MIN_NOTIONAL", 10.0)
	c.MinNotional = pt
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

func (c *Config) WalkForwardCadenceMin() int {
	return getEnvInt("WALK_FORWARD_MIN", c.WalkForwardMin)
}

func (c *Config) EnableVolRiskAdjust() bool {
	return getEnvBool("VOL_RISK_ADJUST", c.VolRiskAdjust)
}

func (c *Config) DirectSlackEnabled() bool {
	return getEnv("SLACK_WEBHOOK", "") != "" || c.UseDirectSlack
}

func (c *Config) FeatureLabelConfig() FeatureLabelConfig {
	return FeatureLabelConfig{
		Horizon:         getEnvInt("AI_LABEL_HORIZON", c.AILabelHorizon),
		FeeRatePct:      getEnvFloat("FEE_RATE_PCT", c.FeeRatePct),
		MinEdgePct:      getEnvFloat("AI_MIN_EDGE_PCT", c.AIMinEdgePct),
		MinRows:         100,
		ProfitGateUSD:   getEnvFloat("PROFIT_GATE_USD", c.ProfitGateUSD),
		BaseSizeUSD:     getEnvFloat("RISK_PER_TRADE_USD", c.RiskPerTradeUSD),
		MinedLabelsFile: getEnv("AI_MINED_LABELS_FILE", "/opt/coinbase/state/mined_labels_binance.jsonl"),
		MinedMaxRows:    getEnvInt("AI_MINED_MAX_ROWS", 10000),
		Symbol:          getEnv("PRODUCT_ID", c.ProductID),
		MACDLineEPS:     c.MACDLineEPS,
		AIFeatureDim:    c.AIFeatureDim,
	}
}

// --- trailing env accessors (unchanged; universal) ---
func (c *Config) TrailActivatePct() float64 { return getEnvFloat("TRAIL_ACTIVATE_PCT", 1.2) }
func (c *Config) TrailDistancePct() float64 { return getEnvFloat("TRAIL_DISTANCE_PCT", 0.6) }
