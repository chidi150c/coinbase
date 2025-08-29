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

// ---- Phase-7: tiny L2-regularized logistic head (opt-in; append-only) ----

// ExtendedLogit is an optional, regularized logistic model used when the
// extended path is enabled. Baseline behavior remains unchanged.
type ExtendedLogit struct {
	W       []float64
	B       float64
	L2      float64
	FeatDim int
}

func NewExtendedLogit(featDim int) *ExtendedLogit {
	w := make([]float64, featDim)
	rand.Seed(time.Now().UnixNano())
	for i := range w {
		w[i] = rand.NormFloat64() * 0.01
	}
	return &ExtendedLogit{W: w, B: 0, L2: 1e-3, FeatDim: featDim}
}

func (m *ExtendedLogit) Predict(x []float64) float64 {
	if len(x) != m.FeatDim {
		return 0.5
	}
	z := m.B
	for i := 0; i < m.FeatDim; i++ {
		z += m.W[i] * x[i]
	}
	// numeric clamps
	if z > 20 {
		return 1.0
	}
	if z < -20 {
		return 0.0
	}
	return 1.0 / (1.0 + math.Exp(-z))
}

func (m *ExtendedLogit) FitMiniBatch(feats [][]float64, labels []float64, lr float64, epochs int, batch int) {
	if len(feats) == 0 || len(labels) == 0 {
		return
	}
	bestW := append([]float64(nil), m.W...)
	bestB := m.B
	bestLoss := math.MaxFloat64
	patience := 3
	wait := 0

	for e := 0; e < epochs; e++ {
		perm := rand.Perm(len(feats))
		for off := 0; off < len(feats); off += batch {
			end := off + batch
			if end > len(feats) {
				end = len(feats)
			}
			gW := make([]float64, m.FeatDim)
			var gB float64
			for k := off; k < end; k++ {
				i := perm[k]
				p := m.Predict(feats[i])
				y := labels[i]
				grad := p - y
				for j := 0; j < m.FeatDim; j++ {
					gW[j] += grad * feats[i][j]
				}
				gB += grad
			}
			// L2 regularization
			for j := 0; j < m.FeatDim; j++ {
				gW[j] += m.L2 * m.W[j]
			}
			eta := lr / float64(end-off)
			for j := 0; j < m.FeatDim; j++ {
				m.W[j] -= eta * gW[j]
			}
			m.B -= eta * gB
		}
		// evaluate simple loss + L2
		loss := 0.0
		for i := range feats {
			p := m.Predict(feats[i])
			if p < 1e-8 {
				p = 1e-8
			}
			if p > 1-1e-8 {
				p = 1 - 1e-8
			}
			y := labels[i]
			loss += -(y*math.Log(p) + (1-y)*math.Log(1-p))
		}
		reg := 0.0
		for j := 0; j < m.FeatDim; j++ {
			reg += 0.5 * m.L2 * m.W[j] * m.W[j]
		}
		loss += reg

		if loss < bestLoss-1e-3 {
			bestLoss = loss
			copy(bestW, m.W)
			bestB = m.B
			wait = 0
		} else {
			wait++
			if wait >= patience {
				break
			}
		}
	}
	m.W, m.B = bestW, bestB
}

// ---- Minimal additions to support decide() extended-branch wiring ----

// currentExtModel holds the active extended model when available.
var currentExtModel *ExtendedLogit

// SetCurrentExtendedModel stores the active extended model (no side effects).
func SetCurrentExtendedModel(m *ExtendedLogit) {
	currentExtModel = m
}

// CurrentExtendedModel returns the active extended model, or nil if unset.
func CurrentExtendedModel() *ExtendedLogit {
	return currentExtModel
}
