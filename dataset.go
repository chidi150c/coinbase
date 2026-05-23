// FILE: dataset.go
package main

import (
	"log"
	"math"
)

type FeatureLabelConfig struct {
	Horizon    int
	FeeRatePct float64
	MinEdgePct float64
	MinRows    int

	// Path-based net-profit labeling.
	ProfitGateUSD float64
	BaseSizeUSD   float64
}

func BuildFeaturesAndLabels(c []Candle, cfg FeatureLabelConfig) ([][]float64, []float64) {
	if len(c) < 100 {
		log.Printf("[WARN] dataset: insufficient candles=%d", len(c))
		return nil, nil
	}

	horizon := cfg.Horizon
	if horizon <= 0 {
		horizon = 15
	}

	feeRatePct := cfg.FeeRatePct
	if feeRatePct <= 0 {
		feeRatePct = 0.10
	}

	minEdgePct := cfg.MinEdgePct
	if minEdgePct < 0 {
		minEdgePct = 0.05
	}

	minRows := cfg.MinRows
	if minRows <= 0 {
		minRows = 100
	}

	profitUSD := cfg.ProfitGateUSD
	if profitUSD <= 0 {
		profitUSD = 1.0
	}

	baseUSD := cfg.BaseSizeUSD
	if baseUSD <= 0 {
		baseUSD = 80.0
	}

	// Kept for logging/comparison. This is the old final-close edge threshold.
	edge := (feeRatePct*2.0 + minEdgePct) / 100.0

	// Round-trip fee fraction used by the path-based target calculation.
	fee := (feeRatePct * 2.0) / 100.0

	var X [][]float64
	var y []float64

	total := len(c)
	up := 0
	down := 0
	skipped := 0
	bad := 0

	start := 50
	end := len(c) - horizon

	if end <= start {
		log.Printf("[WARN] dataset: not enough candles after horizon adjustment")
		return nil, nil
	}

	for i := start; i < end; i++ {
		curClose := c[i].Close
		if curClose <= 0 {
			bad++
			continue
		}

		size := baseUSD / curClose
		if size <= 0 {
			bad++
			continue
		}

		buyTarget := curClose * (1.0 + (profitUSD / (curClose * size)) + fee)

		sellTarget := curClose * (1.0 - (profitUSD / (curClose * size)) - fee)

		var label float64
		keep := false

		for j := i + 1; j <= i+horizon; j++ {
			if c[j].High >= buyTarget {
				label = 1.0
				up++
				keep = true
				break
			}

			if c[j].Low <= sellTarget {
				label = 0.0
				down++
				keep = true
				break
			}
		}

		if !keep {
			skipped++
			continue
		}

		features, ok := BuildFeatures(c, i)
		if !ok || len(features) == 0 {
			bad++
			continue
		}

		good := true
		for _, v := range features {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				good = false
				break
			}
		}

		if !good {
			bad++
			continue
		}

		X = append(X, features)
		y = append(y, label)
	}

	featDim := 0
	if len(X) > 0 {
		featDim = len(X[0])
	}

	log.Printf(
		"[DATASET] total=%d labeled=%d up=%d down=%d skipped=%d bad=%d edge=%.4f horizon=%d feat_dim=%d profitUSD=%.2f baseUSD=%.2f",
		total, len(X), up, down, skipped, bad, edge, horizon, featDim, profitUSD, baseUSD,
	)

	if len(X) < minRows {
		log.Printf("[WARN] dataset rows too small rows=%d min=%d; skipping train", len(X), minRows)
		return nil, nil
	}

	if len(X) != len(y) {
		log.Printf("[ERROR] dataset shape mismatch rows=%d labels=%d", len(X), len(y))
		return nil, nil
	}

	return X, y
}
