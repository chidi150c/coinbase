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

	for {
		select {
		case <-ctx.Done():
			log.Println("shutdown")
			return

		case <-ticker.C:
			// Pull the freshest candle(s)
			cs, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 2)
			if err != nil || len(cs) == 0 {
				if err != nil {
					log.Printf("candles err: %v", err)
				}
				continue
			}
			latest := cs[len(cs)-1]

			// Append or replace the last bucket if we're still in the same minute
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
		}
	}
}

// --- time helpers for model.go indirection ---

func monotonicNowSeconds() float64 {
	// wall-clock is fine for our use; true monotonic not required here
	return float64(time.Now().UnixNano()) / 1e9
}
