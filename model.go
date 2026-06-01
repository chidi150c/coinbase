// FILE: model.go
package main

import (
	"log"
	"math"
	"math/rand"
)

type LogisticModel struct {
	W       []float64 `json:"W"`
	B       float64   `json:"B"`
	L2      float64   `json:"L2"`
	FeatDim int       `json:"FeatDim"`
}

func newModel(featureDim int) *LogisticModel {
	return NewLogisticModel(featureDim)
}

func NewLogisticModel(featDim int) *LogisticModel {
	// Logistic regression does not require random initialization.
	// We intentionally start from zeros so training is deterministic
	// and reproducible across restarts / experiments.
	w := make([]float64, featDim)

	return &LogisticModel{
		W:       w,
		B:       0,
		L2:      1e-3,
		FeatDim: featDim,
	}
}

func sigmoid(x float64) float64 {
	if x > 20 {
		return 1
	}
	if x < -20 {
		return 0
	}
	return 1 / (1 + math.Exp(-x))
}

func (m *LogisticModel) Predict(x []float64) float64 {
	if m == nil {
		return 0.5
	}
	if len(x) == 0 {
		return 0.5
	}
	if m.FeatDim <= 0 || len(m.W) == 0 {
		return 0.5
	}
	if len(x) != m.FeatDim || len(m.W) != m.FeatDim {
		return 0.5
	}

	z := m.B
	for i := 0; i < m.FeatDim; i++ {
		z += m.W[i] * x[i]
	}
	return sigmoid(z)
}

// predict is kept as a small compatibility wrapper.
func (m *LogisticModel) predict(x []float64) float64 {
	return m.Predict(x)
}

// fit keeps the old call style alive while using the new unified dataset path.
func (m *LogisticModel) fit(c []Candle, lr float64, epochs int) {
	cfgObj := loadConfigFromEnv()
	cfg := cfgObj.FeatureLabelConfig()

	rollingFeats, rollingLabels := BuildFeaturesAndLabels(c, cfg)

	feats := rollingFeats
	labels := rollingLabels

	mined := loadMinedLabels(cfg.MinedLabelsFile)

	var minedUp, minedDown int
	for _, r := range mined {
		if r.Y >= 0.5 {
			minedUp++
		} else {
			minedDown++
		}
	}

	const minedMinRows = 500

	if len(mined) >= minedMinRows &&
		minedUp > 0 &&
		minedDown > 0 {

		for _, r := range mined {
			if len(r.X) == 0 {
				continue
			}

			feats = append(feats, r.X)
			labels = append(labels, r.Y)
		}

		log.Printf(
			"[MINED_LABELS] loaded rows=%d up=%d down=%d",
			len(mined),
			minedUp,
			minedDown,
		)
	} else {
		log.Printf(
			"[MINED_LABELS] skipped rows=%d up=%d down=%d min_rows=%d",
			len(mined),
			minedUp,
			minedDown,
			minedMinRows,
		)
	}

	if len(feats) == 0 || len(labels) == 0 {
		log.Printf("TRACE model.train.skip reason=no_dataset")
		return
	}

	m.FitMiniBatch(feats, labels, lr, epochs, 32)
}

func (m *LogisticModel) FitMiniBatch(feats [][]float64, labels []float64, lr float64, epochs int, batch int) {
	if m == nil {
		return
	}
	if len(feats) == 0 || len(labels) == 0 {
		return
	}
	if len(feats) != len(labels) {
		log.Printf("[ERROR] model.fit shape mismatch rows=%d labels=%d", len(feats), len(labels))
		return
	}

	featDim := len(feats[0])
	if featDim == 0 {
		log.Printf("[WARN] model.fit empty feature dimension")
		return
	}

	for i := range feats {
		if len(feats[i]) != featDim {
			log.Printf("[ERROR] model.fit inconsistent feature dimension row=%d got=%d want=%d", i, len(feats[i]), featDim)
			return
		}
	}

	if m.FeatDim != featDim || len(m.W) != featDim {
		log.Printf("[INFO] model.init feat_dim=%d old_feat_dim=%d", featDim, m.FeatDim)
		nm := NewLogisticModel(featDim)
		m.W = nm.W
		m.B = nm.B
		if m.L2 <= 0 {
			m.L2 = nm.L2
		}
		m.FeatDim = featDim
	}

	if batch <= 0 {
		batch = 32
	}
	if lr <= 0 {
		lr = 0.05
	}
	if epochs <= 0 {
		epochs = 10
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

			for j := 0; j < m.FeatDim; j++ {
				gW[j] += m.L2 * m.W[j]
			}

			eta := lr / float64(end-off)

			for j := 0; j < m.FeatDim; j++ {
				m.W[j] -= eta * gW[j]
			}
			m.B -= eta * gB
		}

		loss := m.loss(feats, labels)

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

	m.W = bestW
	m.B = bestB

	log.Printf(
		"[MODEL] trained rows=%d feat_dim=%d loss=%.6f",
		len(feats),
		m.FeatDim,
		bestLoss,
	)
	m.logFitReport(feats, labels)
}

func (m *LogisticModel) logFitReport(feats [][]float64, labels []float64) {
	if m == nil || len(feats) == 0 || len(labels) == 0 || len(feats) != len(labels) {
		return
	}

	var correct, tp, tn, fp, fn int
	var upSum, downSum float64
	var upN, downN int

	for i := range feats {
		p := m.Predict(feats[i])

		pred := 0.0
		if p >= 0.5 {
			pred = 1.0
		}

		y := labels[i]

		if pred == y {
			correct++
		}

		switch {
		case pred == 1 && y == 1:
			tp++
		case pred == 0 && y == 0:
			tn++
		case pred == 1 && y == 0:
			fp++
		case pred == 0 && y == 1:
			fn++
		}

		if y == 1 {
			upSum += p
			upN++
		} else {
			downSum += p
			downN++
		}
	}

	acc := float64(correct) / float64(len(labels))

	precision := 0.0
	if tp+fp > 0 {
		precision = float64(tp) / float64(tp+fp)
	}

	recall := 0.0
	if tp+fn > 0 {
		recall = float64(tp) / float64(tp+fn)
	}

	avgUp := 0.0
	if upN > 0 {
		avgUp = upSum / float64(upN)
	}

	avgDown := 0.0
	if downN > 0 {
		avgDown = downSum / float64(downN)
	}

	log.Printf(
		"[MODEL_FIT] rows=%d feat_dim=%d acc=%.4f precision=%.4f recall=%.4f tp=%d tn=%d fp=%d fn=%d avg_up=%.5f avg_down=%.5f separation=%.5f",
		len(labels),
		m.FeatDim,
		acc,
		precision,
		recall,
		tp,
		tn,
		fp,
		fn,
		avgUp,
		avgDown,
		avgUp-avgDown,
	)
}

func (m *LogisticModel) loss(feats [][]float64, labels []float64) float64 {
	if m == nil || len(feats) == 0 || len(labels) == 0 {
		return math.MaxFloat64
	}

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

	return loss + reg
}
