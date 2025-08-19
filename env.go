// FILE: env.go
// Package main – Environment helpers and safe .env loading for the trading bot.
//
// This file provides:
//   1) Small helpers to read environment variables with sane defaults
//      (strings, ints, floats, bools).
//   2) A dependency-free .env loader (loadBotEnv) that reads ./ .env (and ../.env)
//      and injects ONLY the keys the Go bot needs into the process environment.
//      It intentionally ignores secrets not used by the Go process (e.g., the
//      multi-line Coinbase PEM used by the Python sidecar) to avoid shell-export issues.
//   3) Strategy threshold knobs (buyThreshold, sellThreshold, useMAFilter) and an
//      initializer (initThresholdsFromEnv) so you can tune behavior via .env without
//      recompiling.
//
// The Python FastAPI sidecar continues to read its own .env (including the PEM).
// The Go bot never requires `export $(cat .env ...)`; just run `go run .`.
package main

import (
	"bufio"
	"os"
	"path/filepath"
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

// --------- Lightweight .env loader (no external deps) ---------

// loadBotEnv reads .env from "." and ".." and sets ONLY the keys the Go bot needs.
// It won't override variables already in the environment and ignores multi-line PEMs.
func loadBotEnv() {
	needed := map[string]struct{}{
		"PRODUCT_ID": {}, "GRANULARITY": {}, "DRY_RUN": {}, "MAX_DAILY_LOSS_PCT": {},
		"RISK_PER_TRADE_PCT": {}, "USD_EQUITY": {}, "TAKE_PROFIT_PCT": {},
		"STOP_LOSS_PCT": {}, "ORDER_MIN_USD": {}, "LONG_ONLY": {}, "PORT": {}, "BRIDGE_URL": {},
		"BUY_THRESHOLD": {}, "SELL_THRESHOLD": {}, "USE_MA_FILTER": {},
	}
	try := func(path string) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// allow optional "export KEY=VAL"
			if strings.HasPrefix(line, "export ") {
				line = strings.TrimSpace(line[len("export "):])
			}
			eq := strings.Index(line, "=")
			if eq <= 0 {
				continue
			}
			key := strings.TrimSpace(line[:eq])
			if _, ok := needed[key]; !ok {
				continue // ignore secrets (e.g., PEM) we don't need
			}
			val := strings.TrimSpace(line[eq+1:])
			// strip surrounding quotes if present
			if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
				val = val[1 : len(val)-1]
			}
			// drop inline comments after value (e.g., KEY=http://... # note)
			if idx := strings.IndexAny(val, "#"); idx >= 0 {
				val = strings.TrimSpace(val[:idx])
			}
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
	for _, base := range []string{".", ".."} {
		try(filepath.Join(base, ".env"))
	}
}

// --------- Tunable strategy thresholds (initialized in main) ---------

var (
	buyThreshold  float64 // set by initThresholdsFromEnv()
	sellThreshold float64 // set by initThresholdsFromEnv()
	useMAFilter   bool    // set by initThresholdsFromEnv()
)

func initThresholdsFromEnv() {
	buyThreshold = getEnvFloat("BUY_THRESHOLD", 0.55)
	sellThreshold = getEnvFloat("SELL_THRESHOLD", 0.45)
	useMAFilter = getEnvBool("USE_MA_FILTER", true)
}
