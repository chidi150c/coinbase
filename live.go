// FILE: live.go
// Package main – Live loop, real-candle polling, and time helpers.
//
// runLive drives the trading loop in real time:
//   • Warm up by fetching ~300 recent candles from the bridge broker.
//   • Fit the tiny ML model on warmup history.
//   • Every intervalSec seconds, fetch the latest candle(s), update history,
//     ask the Trader to step (which may OPEN/HOLD/EXIT), and update metrics.
//
// Notes:
//   - We prefer real OHLCV from the /candles endpoint instead of synthesizing candles.
//   - History is capped to 1000 candles to keep memory/compute stable.
//   - The tiny indirection for monotonic time is here (monotonicNowSeconds).

package main

import (
	"context"
	"log"
	"time"

	// Minimal additions for live-equity support:
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// runLive executes the real-time loop with cadence intervalSec (seconds).
func runLive(ctx context.Context, trader *Trader, model *AIMicroModel, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	log.Printf("Starting %s — product=%s dry_run=%v",
		trader.broker.Name(), trader.cfg.ProductID, trader.cfg.DryRun)

	// Safety banner for operators (no behavior change)
	log.Printf("[SAFETY] LONG_ONLY=%v | ORDER_MIN_USD=%.2f | RISK_PER_TRADE_PCT=%.2f | MAX_DAILY_LOSS_PCT=%.2f | TAKE_PROFIT_PCT=%.2f | STOP_LOSS_PCT=%.2f",
		trader.cfg.LongOnly, trader.cfg.OrderMinUSD, trader.cfg.RiskPerTradePct,
		trader.cfg.MaxDailyLossPct, trader.cfg.TakeProfitPct, trader.cfg.StopLossPct)

	// Warmup candles
	var history []Candle
	if cs, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 300); err == nil && len(cs) > 0 {
		history = cs
	}
	if len(history) == 0 {
		// Fallback synthetic bootstrap (should be rare)
		now := time.Now().UTC().Add(-300 * time.Minute)
		for i := 0; i < 300; i++ {
			history = append(history, Candle{
				Time:   now.Add(time.Duration(i) * time.Minute),
				Open:   50000,
				High:   50000,
				Low:    50000,
				Close:  50000,
				Volume: 0,
			})
		}
	}

	// Fit the tiny model
	model.fit(history, 0.05, 4)

	// Minimal addition: one-time live-equity rebase (opt-in; no effect unless enabled)
	if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun && trader.cfg.BridgeURL != "" {
		ctxInit, cancelInit := context.WithTimeout(context.Background(), 5*time.Second)
		initLiveEquity(ctxInit, trader.cfg, trader, history[len(history)-1].Close)
		cancelInit()
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("shutdown")
			return

		case <-ticker.C:
			// Poll the latest candle(s) from the bridge; prefer real OHLCV
			latestSlice, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 1)
			if err != nil || len(latestSlice) == 0 {
				log.Printf("poll err: %v", err)
				continue
			}
			latest := latestSlice[0]

			// Append or replace the last candle if timestamps match
			if len(history) == 0 || latest.Time.After(history[len(history)-1].Time) {
				history = append(history, latest)
			} else {
				history[len(history)-1] = latest
			}
			if len(history) > 1000 {
				history = history[len(history)-1000:]
			}

			// Step the trader
			msg, err := trader.step(ctx, history)
			if err != nil {
				log.Printf("step err: %v", err)
				continue
			}
			log.Printf("%s", msg)
			mtxPnL.Set(trader.EquityUSD())

			// Minimal addition: per-tick live-equity refresh (opt-in; no effect unless enabled)
			if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun && trader.cfg.BridgeURL != "" {
				ctxEq, cancelEq := context.WithTimeout(ctx, 5*time.Second)
				if bal, err := fetchBridgeAccounts(ctxEq, trader.cfg.BridgeURL); err == nil {
					base, quote := splitProductID(trader.cfg.ProductID)
					eq := computeLiveEquity(bal, base, quote, latest.Close)
					log.Printf("[EQUITY] calc=%.2f USD=%.2f BTC=%.6f price=%.2f",
            		eq, bal["USD"], bal["BTC"], latest.Close)
					if eq > 0 {
						trader.SetEquityUSD(eq)
					}
				}
				cancelEq()
			}
		}
	}
}

// --- time helpers for model.go indirection ---

func monotonicNowSeconds() float64 {
	// wall-clock is fine for our use; true monotonic not required here
	return float64(time.Now().UnixNano()) / 1e9
}

// ===== Minimal helpers for live-equity (no public behavior change unless enabled) =====

// Matches the Bridge /accounts response shape:
type bridgeAccountsResp struct {
    Accounts []bridgeAccount `json:"accounts"`
    HasNext  bool            `json:"has_next"`
    Cursor   string          `json:"cursor"`
    Size     int             `json:"size"`
}
type bridgeAmount struct {
    Value    string `json:"value"`
    Currency string `json:"currency"`
}
type bridgeAccount struct {
    Currency         string       `json:"currency"`
    AvailableBalance bridgeAmount `json:"available_balance"` // <- use .Value
    Type             string       `json:"type"`
    Platform         string       `json:"platform"`
}

func fetchBridgeAccounts(ctx context.Context, bridgeURL string) (map[string]float64, error) {
    // ask for more to avoid pagination edge-cases
    u := strings.TrimRight(bridgeURL, "/") + "/accounts?limit=250"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge /accounts %d: %s", resp.StatusCode, string(b))
	}
	var payload bridgeAccountsResp
    if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(payload.Accounts))
    for _, r := range payload.Accounts {
        v, _ := strconv.ParseFloat(strings.TrimSpace(r.AvailableBalance.Value), 64)
        out[strings.ToUpper(strings.TrimSpace(r.Currency))] = v
	}
	return out, nil
}

func splitProductID(pid string) (base, quote string) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(pid)), "-")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	if len(pid) > 3 {
		return strings.ToUpper(pid[:len(pid)-3]), strings.ToUpper(pid[len(pid)-3:])
	}
	return strings.ToUpper(pid), "USD"
}

// Equity = quoteBalance + USD + baseBalance*lastPrice
func computeLiveEquity(bal map[string]float64, base, quote string, lastPrice float64) float64 {
     // USD account only (don't double count)
     q := bal[strings.ToUpper(quote)]       // quote is "USD" for BTC-USD
     b := bal[strings.ToUpper(base)]        // "BTC"
     return q + b*lastPrice
}

func initLiveEquity(ctx context.Context, cfg Config, trader *Trader, lastPrice float64) {
	if lastPrice <= 0 {
		return
	}
	bal, err := fetchBridgeAccounts(ctx, cfg.BridgeURL)
	if err != nil {
		return
	}
	base, quote := splitProductID(cfg.ProductID)
	eq := computeLiveEquity(bal, base, quote, lastPrice)
	if eq > 0 {
		trader.SetEquityUSD(eq)
	}
}
