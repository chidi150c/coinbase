// FILE: model.go
// Package main – Tiny in-memory ML “micro-model” for directional bias.
//
// Minimal logistic-regression–style model used to produce pUp from
// hand-crafted features. Kept simple and fast.

package main

import (
	"math"
	"math/rand"
	"time"
)

type AIMicroModel struct {
	W []float64 // weights
	B float64   // bias
}

func newModel() *AIMicroModel {
	rand.Seed(time.Now().UnixNano())
	w := make([]float64, 4) // features: ret1, ret5, rsi14/100, zscore20
	for i := range w {
		w[i] = rand.NormFloat64() * 0.01
	}
	return &AIMicroModel{W: w}
}

// sigmoid returns 1/(1+e^-x) with simple clamping for numerical stability.
func sigmoid(x float64) float64 {
	if x > 20 {
		return 1
	}
	if x < -20 {
		return 0
	}
	return 1 / (1 + math.Exp(-x))
}

// predict expects exactly len(W) features; otherwise returns 0.5.
func (m *AIMicroModel) predict(features []float64) float64 {
	if len(features) != len(m.W) {
		return 0.5
	}
	z := m.B
	for i := range features {
		z += m.W[i] * features[i]
	}
	return sigmoid(z)
}

// fit performs a simple gradient step on cross-entropy loss.
func (m *AIMicroModel) fit(c []Candle, lr float64, epochs int) {
	if len(c) < 40 {
		return
	}
	feats, labels := buildDataset(c)
	for e := 0; e < epochs; e++ {
		for i := range feats {
			p := m.predict(feats[i])
			y := labels[i]
			grad := p - y
			for j := range m.W {
				m.W[j] -= lr * grad * feats[i][j]
			}
			m.B -= lr * grad
		}
	}
}

// buildDataset creates (features, labels) from candles.
func buildDataset(c []Candle) ([][]float64, []float64) {
	var feats [][]float64
	var labels []float64
	rsis := RSI(c, 14)
	zs := ZScore(c, 20)
	for i := 21; i < len(c)-1; i++ {
		ret1 := (c[i].Close - c[i-1].Close) / c[i-1].Close
		ret5 := (c[i].Close - c[i-5].Close) / c[i-5].Close
		f := []float64{ret1, ret5, rsis[i] / 100.0, zs[i]}
		up := 0.0
		if c[i+1].Close > c[i].Close {
			up = 1.0
		}
		feats = append(feats, f)
		labels = append(labels, up)
	}
	return feats, labels
}
