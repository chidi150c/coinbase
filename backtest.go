// FILE: backtest.go
// Package main – CSV loader and simple backtest runner.
//
// What’s here:
//   • loadCSV(path) -> []Candle   : reads time,open,high,low,close,volume
//   • runBacktest(ctx, csvPath, trader, model)
//       - trains the micro-model on 70% of data
//       - runs a simple walk-forward on the remaining 30%
//       - logs periodic progress and updates bot_equity_usd gauge
//
// Notes:
//   • Time column accepts RFC3339 or UNIX seconds.
//   • Unknown columns are ignored; headers are case-insensitive.

package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// loadCSV reads a generic candle CSV with headers:
// time|timestamp, open, high, low, close, volume
func loadCSV(path string) ([]Candle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1

	var out []Candle
	var headers []string
	rowIdx := 0

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if rowIdx == 0 {
			headers = rec
			rowIdx++
			continue
		}
		row := map[string]string{}
		for j, h := range headers {
			k := strings.ToLower(strings.TrimSpace(h))
			if j < len(rec) {
				row[k] = strings.TrimSpace(rec[j])
			}
		}
		ts := first(row, "time", "timestamp")
		op := first(row, "open")
		hp := first(row, "high")
		lp := first(row, "low")
		cp := first(row, "close")
		vp := first(row, "volume", "vol")
		if ts == "" || op == "" || cp == "" {
			continue
		}
		tt, err := parseTimeFlexible(ts)
		if err != nil {
			continue
		}
		o, _ := strconv.ParseFloat(op, 64)
		h, _ := strconv.ParseFloat(hp, 64)
		l, _ := strconv.ParseFloat(lp, 64)
		c, _ := strconv.ParseFloat(cp, 64)
		v, _ := strconv.ParseFloat(vp, 64)
		out = append(out, Candle{Time: tt, Open: o, High: h, Low: l, Close: c, Volume: v})
		rowIdx++
	}

	sortCandles(out)
	return out, nil
}

// parseTimeFlexible supports RFC3339 or UNIX seconds.
func parseTimeFlexible(s string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts, nil
	}
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("bad time: %s", s)
}

// sortCandles ensures ascending time.
func sortCandles(c []Candle) {
	sort.Slice(c, func(i, j int) bool { return c[i].Time.Before(c[j].Time) })
}

// first returns the first non-empty value for keys in m.
func first(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// runBacktest trains the model and simulates decisions on the test split.
func runBacktest(ctx context.Context, csvPath string, trader *Trader, model *AIMicroModel) {
	candles, err := loadCSV(csvPath)
	if err != nil {
		log.Fatalf("backtest load: %v", err)
	}
	if len(candles) < 200 {
		log.Fatalf("need >=200 candles, have %d", len(candles))
	}

	// Train/test split
	split := int(0.7 * float64(len(candles)))
	if split < 100 {
		split = len(candles) / 2
	}
	train := candles[:split]
	test := candles[split:]

	// Train the tiny model
	model.fit(train, 0.05, 4)

	// Force paper for backtest accounting
	trader.cfg.DryRun = true

	win, loss := 0, 0
	for i := 50; i < len(test); i++ {
		select {
		case <-ctx.Done():
			log.Println("backtest canceled")
			return
		default:
		}
		msg, _ := trader.step(ctx, test[:i+1])
		if strings.HasPrefix(msg, "EXIT") {
			if idx := strings.LastIndex(msg, "P/L="); idx >= 0 {
				pl, _ := strconv.ParseFloat(msg[idx+4:], 64)
				if pl > 0 {
					win++
				} else if pl < 0 {
					loss++
				}
			}
		}
		if i%100 == 0 {
			log.Printf("[BT] i=%d msg=%s", i, msg)
		}
	}

	log.Printf("Backtest complete. Wins=%d Losses=%d Equity=%.2f", win, loss, trader.EquityUSD())
	mtxPnL.Set(trader.EquityUSD())
}
