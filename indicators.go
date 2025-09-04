// FILE: indicators.go
// Package main – Technical indicators for the trading bot.
//
// This file implements lightweight TA helpers used by the strategy/model:
//   • SMA(c, n)     – Simple Moving Average of Close
//   • RSI(c, n)     – Relative Strength Index (Wilder’s smoothing)
//   • ZScore(c, n)  – Rolling Z-Score of Close
//
// Notes
//   - All functions accept a slice of Candle (defined in strategy.go).
//   - Outputs are aligned to input length; unavailable lookbacks emit NaN/0 as noted.
//   - Keep these fast and allocation-light; they’re called frequently in the live loop.
package main

import (
	"math"
)

// SMA returns the n-period simple moving average of Close, aligned to c.
// For indices < n-1, the function returns NaN.
func SMA(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if n <= 0 || len(c) == 0 {
		for i := range out {
			out[i] = math.NaN()
		}
		return out
	}
	var sum float64
	for i := range c {
		sum += c[i].Close
		if i >= n {
			sum -= c[i-n].Close
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}

// RSI returns the n-period Relative Strength Index using Wilder’s smoothing.
// Indices before the first full window are zero (0).
func RSI(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if n <= 0 || len(c) == 0 {
		return out
	}
	var gain, loss float64
	for i := 1; i < len(c); i++ {
		d := c[i].Close - c[i-1].Close
		if i <= n {
			if d > 0 {
				gain += d
			} else {
				loss -= d
			}
			if i == n {
				avgGain := gain / float64(n)
				avgLoss := loss / float64(n)
				rs := 0.0
				if avgLoss != 0 {
					rs = avgGain / avgLoss
				}
				out[i] = 100.0 - (100.0 / (1.0 + rs))
			}
		} else {
			// Wilder smoothing
			if d > 0 {
				gain = (gain*float64(n-1) + d) / float64(n)
				loss = (loss*float64(n-1) + 0) / float64(n)
			} else {
				gain = (gain*float64(n-1) + 0) / float64(n)
				loss = (loss*float64(n-1) - d) / float64(n)
			}
			rs := 0.0
			if loss != 0 {
				rs = gain / loss
			}
			out[i] = 100.0 - (100.0 / (1.0 + rs))
		}
	}
	return out
}

// ZScore returns the rolling z-score of Close over window n, aligned to c.
// For indices < n-1, the function returns 0.
func ZScore(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if n <= 1 || len(c) == 0 {
		return out
	}
	var sum, sumSq float64
	for i := range c {
		x := c[i].Close
		sum += x
		sumSq += x * x
		if i >= n {
			y := c[i-n].Close
			sum -= y
			sumSq -= y * y
		}
		if i >= n-1 {
			mean := sum / float64(n)
			variance := (sumSq / float64(n)) - (mean * mean)
			std := math.Sqrt(math.Max(variance, 1e-12))
			out[i] = (x - mean) / std
		} else {
			out[i] = 0
		}
	}
	return out
}

// ---- Advanced indicators (append-only) ----

// EMA returns the n-period exponential moving average of the given values.
// For n <= 1 or empty input, returns zeros aligned to vals.
func EMA(vals []float64, n int) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 || n <= 1 {
		return out
	}
	k := 2.0 / (float64(n) + 1.0)
	out[0] = vals[0]
	for i := 1; i < len(vals); i++ {
		out[i] = vals[i]*k + out[i-1]*(1-k)
	}
	return out
}

// ATR returns the n-period Average True Range using Wilder smoothing.
// Output is aligned to c; early indices ramp up until the first full window.
func ATR(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if len(c) == 0 || n <= 1 {
		return out
	}
	tr := make([]float64, len(c))
	for i := range c {
		if i == 0 {
			tr[i] = c[i].High - c[i].Low
			continue
		}
		h, l, pc := c[i].High, c[i].Low, c[i-1].Close
		a := h - l
		b := math.Abs(h - pc)
		d := math.Abs(l - pc)
		tr[i] = math.Max(a, math.Max(b, d))
	}
	var sum float64
	for i := range c {
		if i < n {
			sum += tr[i]
			if i == n-1 {
				out[i] = sum / float64(n)
			}
		} else {
			out[i] = (out[i-1]*(float64(n)-1) + tr[i]) / float64(n)
		}
	}
	return out
}

// MACD computes MACD (fast EMA - slow EMA), its signal line, and histogram.
func MACD(close []float64, fast, slow, signal int) (macd, signalLine, hist []float64) {
	emaFast := EMA(close, fast)
	emaSlow := EMA(close, slow)
	macd = make([]float64, len(close))
	for i := range close {
		macd[i] = emaFast[i] - emaSlow[i]
	}
	signalLine = EMA(macd, signal)
	hist = make([]float64, len(close))
	for i := range close {
		hist[i] = macd[i] - signalLine[i]
	}
	return
}

// OBV computes On-Balance Volume over candles and returns a cumulative series.
func OBV(c []Candle) []float64 {
	out := make([]float64, len(c))
	for i := 1; i < len(c); i++ {
		switch {
		case c[i].Close > c[i-1].Close:
			out[i] = out[i-1] + c[i].Volume
		case c[i].Close < c[i-1].Close:
			out[i] = out[i-1] - c[i].Volume
		default:
			out[i] = out[i-1]
		}
	}
	return out
}

// RollingStd returns the rolling standard deviation over window n for vals.
// For indices < n-1, the function returns NaN to indicate insufficient data.
func RollingStd(vals []float64, n int) []float64 {
	out := make([]float64, len(vals))
	if n <= 1 {
		for i := range out {
			out[i] = math.NaN()
		}
		return out
	}
	var sum, sumSq float64
	for i := 0; i < len(vals); i++ {
		x := vals[i]
		sum += x
		sumSq += x * x
		if i >= n {
			sum -= vals[i-n]
			sumSq -= vals[i-n] * vals[i-n]
		}
		if i >= n-1 {
			mean := sum / float64(n)
			variance := (sumSq / float64(n)) - mean*mean
			if variance < 0 {
				variance = 0
			}
			out[i] = math.Sqrt(variance)
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}

// EMAOnSeries computes EMA over an arbitrary series (no candles needed).
// Returns a slice of same length (NaNs for early warm-up where appropriate).
func EMAOnSeries(x []float64, period int) []float64 {
	n := len(x)
	out := make([]float64, n)
	if n == 0 || period <= 1 {
		for i := range out {
			out[i] = x[i]
		}
		return out
	}
	alpha := 2.0 / (float64(period) + 1.0)

	// seed with first non-NaN value
	j := 0
	for j < n && math.IsNaN(x[j]) {
		out[j] = math.NaN()
		j++
	}
	if j < n {
		out[j] = x[j]
		j++
	}
	for ; j < n; j++ {
		prev := out[j-1]
		if math.IsNaN(prev) {
			out[j] = x[j]
			continue
		}
		out[j] = alpha*x[j] + (1.0-alpha)*prev
	}
	return out
}

// EMA2OnCloses does EMA( EMA(close, p1), p2 )
func EMA2OnCloses(c []Candle, p1, p2 int) []float64 {
	n := len(c)
	cl := make([]float64, n)
	for i := range c {
		cl[i] = c[i].Close
	}
	e1 := EMAOnSeries(cl, p1)
	return EMAOnSeries(e1, p2)
}

