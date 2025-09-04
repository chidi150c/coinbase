// FILE: env.go
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
		"FEE_RATE_PCT":                 {},
		"MAX_CONCURRENT_LOTS":          {},
		"STATE_FILE":                   {},
		// ---- NEW: optional trailing knobs (safe defaults elsewhere if absent) ----
		"TRAIL_ACTIVATE_PCT":    {},
		"TRAIL_DISTANCE_PCT":    {},
		"SCALP_TP_DECAY_ENABLE": {},
		"SCALP_TP_DEC_MODE":     {},
		"SCALP_TP_DEC_PCT":      {},
		"SCALP_TP_DECAY_FACTOR": {},
		"SCALP_TP_MIN_PCT":      {},
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
