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
