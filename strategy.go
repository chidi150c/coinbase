// FILE: strategy.go
// Package main – Core trading abstractions and decision logic.
//
// This file declares the market data types used across the bot (Candle),
// the signal enums (Buy/Sell/Flat), metadata about a decision, and the
// `decide` function that turns recent candles into a trading intent.
//
// The decision blends:
//   • A tiny ML “micro-model” probability pUp (see model.go)
//   • A moving-average regime filter (MA10 vs MA30), optionally enabled
//     via USE_MA_FILTER (see env.go thresholds).
//
// Thresholds are tunable via .env (no exports):
//   BUY_THRESHOLD (default 0.55), SELL_THRESHOLD (default 0.45),
//   USE_MA_FILTER (default true)

package main

import (
	"fmt"
	"math"
	"time"
)

// Candle is the normalized OHLCV row the bot uses everywhere.
type Candle struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// Signal is the high-level intent.
type Signal int

// String implements fmt.Stringer for pretty logging.
func (s Signal) String() string {
	switch s {
	case Buy:
		return "BUY"
	case Sell:
		return "SELL"
	default:
		return "FLAT"
	}
}


const (
	Flat Signal = iota
	Buy
	Sell
)

// Decision captures what to do and why.
type Decision struct {
	Signal     Signal
	Confidence float64
	Reason     string
}

// SignalToSide converts the intent into a broker side.
func (d Decision) SignalToSide() OrderSide {
	switch d.Signal {
	case Buy:
		return SideBuy
	case Sell:
		return SideSell
	default:
		return SideBuy
	}
}

// decide computes a trading decision from recent candles and the model.
func decide(c []Candle, m *AIMicroModel) Decision {
	if len(c) < 40 {
		return Decision{Signal: Flat, Confidence: 0, Reason: "not_enough_data"}
	}
	i := len(c) - 1

	// Features
	rsis := RSI(c, 14)
	zs := ZScore(c, 20)
	ret1 := (c[i].Close - c[i-1].Close) / c[i-1].Close
	ret5 := (c[i].Close - c[i-5].Close) / c[i-5].Close
	features := []float64{ret1, ret5, rsis[i] / 100.0, zs[i]}
	pUp := m.predict(features)

	// Regime filter
	smaFast := SMA(c, 10)
	smaSlow := SMA(c, 30)
	filterOK := !math.IsNaN(smaFast[i]) && !math.IsNaN(smaSlow[i]) && smaFast[i] > smaSlow[i]

	reason := fmt.Sprintf("pUp=%.3f, ma10=%.2f vs ma30=%.2f", pUp, smaFast[i], smaSlow[i])

	// BUY if pUp clears threshold and (optionally) MA filter
	if pUp > buyThreshold && (!useMAFilter || filterOK) {
		return Decision{Signal: Buy, Confidence: pUp, Reason: reason}
	}
	// SELL if pUp below threshold and (optionally) MA filter is bearish
	if pUp < sellThreshold && (!useMAFilter || !filterOK) {
		return Decision{Signal: Sell, Confidence: 1 - pUp, Reason: reason}
	}
	return Decision{Signal: Flat, Confidence: 0.5, Reason: reason}
}
