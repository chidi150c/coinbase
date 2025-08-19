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

	// Ops
	Port      int
	BridgeURL string // e.g., http://127.0.0.1:8787
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
		Port:            getEnvInt("PORT", 8080),
		BridgeURL:       getEnv("BRIDGE_URL", "http://127.0.0.1:8787"),
	}
}
