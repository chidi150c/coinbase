package main

// Shadow-only indicator math matching the trained v2 feature contract.
// These names are isolated from version201 indicator functions.
import "math"

func shadowRSI(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if n <= 0 || len(c) <= n {
		return out
	}

	var avgGain, avgLoss float64
	for i := 1; i <= n; i++ {
		d := c[i].Close - c[i-1].Close
		if d > 0 {
			avgGain += d
		} else {
			avgLoss -= d
		}
	}
	avgGain /= float64(n)
	avgLoss /= float64(n)
	out[n] = shadowindicatorRSIValue(avgGain, avgLoss)

	for i := n + 1; i < len(c); i++ {
		d := c[i].Close - c[i-1].Close
		gain, loss := 0.0, 0.0
		if d > 0 {
			gain = d
		} else {
			loss = -d
		}
		avgGain = (avgGain*float64(n-1) + gain) / float64(n)
		avgLoss = (avgLoss*float64(n-1) + loss) / float64(n)
		out[i] = shadowindicatorRSIValue(avgGain, avgLoss)
	}
	return out
}

// shadowZScore returns the rolling z-score of Close over window n, aligned to c.
// For indices < n-1, the function returns 0.
func shadowZScore(c []Candle, n int) []float64 {
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

// shadowEMA returns the n-period exponential moving average of the given values.
// For n <= 1 or empty input, returns zeros aligned to vals.
func shadowEMA(vals []float64, n int) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 || n <= 0 {
		return out
	}
	if n == 1 {
		copy(out, vals)
		return out
	}
	k := 2.0 / (float64(n) + 1.0)
	out[0] = vals[0]
	for i := 1; i < len(vals); i++ {
		out[i] = vals[i]*k + out[i-1]*(1-k)
	}
	return out
}

// shadowATR returns the n-period Average True Range using Wilder smoothing.
// Output is aligned to c; early indices ramp up until the first full window.
func shadowATR(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if len(c) == 0 || n <= 0 {
		return out
	}
	var sum float64
	for i := range c {
		var tr float64
		if i == 0 {
			tr = c[i].High - c[i].Low
		} else {
			h, l, pc := c[i].High, c[i].Low, c[i-1].Close
			tr = math.Max(h-l, math.Max(math.Abs(h-pc), math.Abs(l-pc)))
		}
		if i < n {
			sum += tr
			if i == n-1 {
				out[i] = sum / float64(n)
			}
		} else {
			out[i] = (out[i-1]*float64(n-1) + tr) / float64(n)
		}
	}
	return out
}

// shadowMACD computes shadowMACD (fast shadowEMA - slow shadowEMA), its signal line, and histogram.
func shadowMACD(close []float64, fast, slow, signal int) (macd, signalLine, hist []float64) {
	emaFast := shadowEMA(close, fast)
	emaSlow := shadowEMA(close, slow)
	macd = make([]float64, len(close))
	for i := range close {
		macd[i] = emaFast[i] - emaSlow[i]
	}
	signalLine = shadowEMA(macd, signal)
	hist = make([]float64, len(close))
	for i := range close {
		hist[i] = macd[i] - signalLine[i]
	}
	return
}

// MACDLineHistAndSlopesAt returns shadowMACD line, shadowMACD histogram, and the last
// three causal histogram slope deltas ending at idx.
//
// Important: the slopes are calculated at idx, not at the end of the slice,
// so training rows do not leak future candles into historical feature vectors.
func shadowMFI(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if n <= 0 || len(c) < 2 {
		return out
	}

	positive := make([]float64, len(c))
	negative := make([]float64, len(c))
	for i := 1; i < len(c); i++ {
		currentTypical := (c[i].High + c[i].Low + c[i].Close) / 3.0
		previousTypical := (c[i-1].High + c[i-1].Low + c[i-1].Close) / 3.0
		flow := currentTypical * c[i].Volume
		switch {
		case currentTypical > previousTypical:
			positive[i] = flow
		case currentTypical < previousTypical:
			negative[i] = flow
		}
	}

	var positiveSum, negativeSum float64
	for i := range c {
		positiveSum += positive[i]
		negativeSum += negative[i]
		if i >= n {
			positiveSum -= positive[i-n]
			negativeSum -= negative[i-n]
		}
		if i < n {
			continue
		}
		switch {
		case positiveSum == 0 && negativeSum == 0:
			out[i] = 50
		case negativeSum == 0:
			out[i] = 100
		default:
			out[i] = 100.0 - 100.0/(1.0+positiveSum/negativeSum)
		}
	}
	return out
}

// STOCH returns the stochastic oscillator K and D series. K is the Close's
// position inside the n-period High-Low range; D is a three-period SMA of K.
func shadowWilliamsR(c []Candle, n int) []float64 {
	out := make([]float64, len(c))
	if n <= 0 {
		return out
	}
	for i := n - 1; i < len(c); i++ {
		low, high := shadowindicatorHighLow(c, i-n+1, i)
		if high > low {
			out[i] = -100.0 * (high - c[i].Close) / (high - low)
		}
	}
	return out
}

// WR is the conventional short name for Williams %R.
func shadowDMI(c []Candle, n int) (plusDI, minusDI, adx []float64) {
	plusDI = make([]float64, len(c))
	minusDI = make([]float64, len(c))
	adx = make([]float64, len(c))
	if n <= 0 || len(c) <= n {
		return plusDI, minusDI, adx
	}

	trueRange := make([]float64, len(c))
	plusMovement := make([]float64, len(c))
	minusMovement := make([]float64, len(c))
	for i := 1; i < len(c); i++ {
		up := c[i].High - c[i-1].High
		down := c[i-1].Low - c[i].Low
		if up > down && up > 0 {
			plusMovement[i] = up
		}
		if down > up && down > 0 {
			minusMovement[i] = down
		}
		trueRange[i] = math.Max(c[i].High-c[i].Low,
			math.Max(math.Abs(c[i].High-c[i-1].Close), math.Abs(c[i].Low-c[i-1].Close)))
	}

	var trSmooth, plusSmooth, minusSmooth float64
	for i := 1; i <= n; i++ {
		trSmooth += trueRange[i]
		plusSmooth += plusMovement[i]
		minusSmooth += minusMovement[i]
	}
	dx := make([]float64, len(c))
	for i := n; i < len(c); i++ {
		if i > n {
			trSmooth = trSmooth - trSmooth/float64(n) + trueRange[i]
			plusSmooth = plusSmooth - plusSmooth/float64(n) + plusMovement[i]
			minusSmooth = minusSmooth - minusSmooth/float64(n) + minusMovement[i]
		}
		if trSmooth > 0 {
			plusDI[i] = 100.0 * plusSmooth / trSmooth
			minusDI[i] = 100.0 * minusSmooth / trSmooth
		}
		totalDI := plusDI[i] + minusDI[i]
		if totalDI > 0 {
			dx[i] = 100.0 * math.Abs(plusDI[i]-minusDI[i]) / totalDI
		}
	}

	firstADX := 2*n - 1
	if firstADX >= len(c) {
		return plusDI, minusDI, adx
	}
	for i := n; i <= firstADX; i++ {
		adx[firstADX] += dx[i]
	}
	adx[firstADX] /= float64(n)
	for i := firstADX + 1; i < len(c); i++ {
		adx[i] = (adx[i-1]*float64(n-1) + dx[i]) / float64(n)
	}
	return plusDI, minusDI, adx
}

// MTM returns n-period price momentum: current Close minus Close n candles ago.
// Feature builders may normalize this series by Close or shadowATR as appropriate.
func shadowindicatorHighLow(c []Candle, start, end int) (low, high float64) {
	low, high = c[start].Low, c[start].High
	for i := start + 1; i <= end; i++ {
		low = math.Min(low, c[i].Low)
		high = math.Max(high, c[i].High)
	}
	return low, high
}

func shadowindicatorRSIValue(avgGain, avgLoss float64) float64 {
	switch {
	case avgGain == 0 && avgLoss == 0:
		return 50
	case avgLoss == 0:
		return 100
	default:
		return 100.0 - 100.0/(1.0+avgGain/avgLoss)
	}
}

