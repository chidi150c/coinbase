package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func shadowTestPaths() (string, string) {
	return filepath.Join("shadow_models", "AI30ClusterOneMinute180dLBFGS.json"),
		filepath.Join("shadow_models", "btc_24feature_six_groups.json")
}

func TestShadow30ParityWithFrozenTrainingFeatures(t *testing.T) {
	modelPath, clusterPath := shadowTestPaths()
	s, err := loadShadow30(modelPath, clusterPath)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string, dst any) {
		t.Helper()
		b, e := os.ReadFile(filepath.Join("testdata", "ai_shadow", name))
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, dst); e != nil {
			t.Fatal(e)
		}
	}
	var candles []Candle
	var expected struct {
		TickPrice     float64     `json:"tick_price"`
		Features      [24]float64 `json:"features_24"`
		Group         int         `json:"group"`
		Probabilities [3]float64  `json:"probabilities"`
		Class         string      `json:"class"`
		Confidence    float64     `json:"confidence"`
	}
	read("candles_120.json", &candles)
	read("expected.json", &expected)
	copyOfCandles := append([]Candle(nil), candles...)
	last := len(copyOfCandles) - 1
	if expected.TickPrice > copyOfCandles[last].High {
		copyOfCandles[last].High = expected.TickPrice
	}
	if expected.TickPrice < copyOfCandles[last].Low {
		copyOfCandles[last].Low = expected.TickPrice
	}
	copyOfCandles[last].Close = expected.TickPrice
	actualFeatures, err := shadow24Features(copyOfCandles)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range expected.Features {
		if math.Abs(actualFeatures[i]-want) > 1e-6 {
			t.Fatalf("feature %d: got %.12g, want %.12g", i, actualFeatures[i], want)
		}
	}
	gotClass, gotGroup, gotConfidence, gotProbs, err := s.evaluate(candles, expected.TickPrice)
	if err != nil {
		t.Fatal(err)
	}
	if gotClass != expected.Class || gotGroup != expected.Group {
		t.Fatalf("got class=%s group=%d, want %s %d", gotClass, gotGroup, expected.Class, expected.Group)
	}
	for i, want := range expected.Probabilities {
		if math.Abs(gotProbs[i]-want) > 1e-6 {
			t.Fatalf("probability %d: got %.9f, want %.9f", i, gotProbs[i], want)
		}
	}
	if math.Abs(gotConfidence-expected.Confidence) > 1e-6 {
		t.Fatalf("confidence=%f, want=%f", gotConfidence, expected.Confidence)
	}
	if candles[last].Close == expected.TickPrice {
		t.Fatal("shadow mutated caller candle history")
	}
}

func TestShadow30RejectsGapWithoutChangingLegacy(t *testing.T) {
	modelPath, clusterPath := shadowTestPaths()
	s, err := loadShadow30(modelPath, clusterPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "ai_shadow", "candles_120.json"))
	if err != nil {
		t.Fatal(err)
	}
	var candles []Candle
	if err := json.Unmarshal(b, &candles); err != nil {
		t.Fatal(err)
	}
	candles[len(candles)-3].Time = candles[len(candles)-3].Time.Add(2 * time.Minute)
	if _, _, _, _, err := s.evaluate(candles, candles[len(candles)-1].Close); err == nil {
		t.Fatal("gap accepted")
	}
}

func TestShadow30RequiresBothExplicitPaths(t *testing.T) {
	t.Setenv("AI_SHADOW_30_MODEL", "")
	t.Setenv("AI_SHADOW_30_CLUSTER", "")
	if s := startShadow30(t.Context(), "BTCUSDT", "ONE_MINUTE"); s != nil {
		t.Fatal("shadow enabled without explicit artifacts")
	}
	modelPath, _ := shadowTestPaths()
	t.Setenv("AI_SHADOW_30_MODEL", modelPath)
	if s := startShadow30(t.Context(), "BTCUSDT", "ONE_MINUTE"); s != nil {
		t.Fatal("shadow enabled with missing cluster")
	}
}
