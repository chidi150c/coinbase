package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"
)

const shadow30Schema = "coinbase-v2-ai-engineered-30-cluster6-all-1m-v1"
const shadow24Schema = "coinbase-v2-ai-engineered-24-all-1m-v1"

// Shadow30 never submits orders or changes Trader state. Both artifacts are
// immutable after loading. Version201 remains the sole trading model.
type shadow30 struct {
	model   shadow30Envelope
	cluster shadow30Cluster
	jobs    chan shadow30Job
	fetch   func(context.Context) ([]Candle, error)
	repaired []Candle // worker-owned; never written back to version201
	lastRepair time.Time
	lastIssue time.Time
}

type shadow30Envelope struct {
	FormatVersion int `json:"format_version"`
	Model struct {
		ModelID string
		Version string
		Mean []float64
		Scale []float64
		Weights [3][]float64
		Bias [3]float64
		BuyGate struct { Threshold float64; Validated bool }
		SellGate struct { Threshold float64; Validated bool }
	} `json:"model"`
}

type shadow30Cluster struct {
	Schema string `json:"schema"`
	FeatureCount int `json:"feature_count"`
	FeatureOrder []string `json:"feature_order"`
	Mean []float64 `json:"scaler_mean"`
	Scale []float64 `json:"scaler_scale"`
	Groups []struct {
		ID int `json:"id"`
		Center []float64 `json:"center_standardized"`
	} `json:"groups"`
}

type shadow30Job struct {
	candles []Candle
	price float64
	tick time.Time
}

func shadowFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Both paths must be explicit. A missing or malformed shadow model cannot
// silently replace the existing model or prevent version201 from trading.
func loadShadow30(modelPath, clusterPath string) (*shadow30, error) {
	mb, err := os.ReadFile(modelPath)
	if err != nil { return nil, fmt.Errorf("read shadow model: %w", err) }
	cb, err := os.ReadFile(clusterPath)
	if err != nil { return nil, fmt.Errorf("read shadow cluster: %w", err) }
	s := &shadow30{jobs: make(chan shadow30Job, 1)}
	if err := json.Unmarshal(mb, &s.model); err != nil { return nil, fmt.Errorf("decode shadow model: %w", err) }
	if err := json.Unmarshal(cb, &s.cluster); err != nil { return nil, fmt.Errorf("decode shadow cluster: %w", err) }
	m, c := s.model.Model, s.cluster
	if s.model.FormatVersion != 1 || m.ModelID != "AI30ClusterOneMinute180dLBFGS" || m.Version != shadow30Schema ||
		c.Schema != shadow24Schema || c.FeatureCount != 24 || len(c.FeatureOrder) != 24 ||
		len(c.Mean) != 24 || len(c.Scale) != 24 || len(c.Groups) != 6 ||
		len(m.Mean) != 30 || len(m.Scale) != 30 || !m.BuyGate.Validated || !m.SellGate.Validated ||
		m.BuyGate.Threshold <= 0 || m.BuyGate.Threshold > 1 || m.SellGate.Threshold <= 0 || m.SellGate.Threshold > 1 {
		return nil, fmt.Errorf("shadow artifact contract mismatch")
	}
	for j := 0; j < 30; j++ {
		if !shadowFinite(m.Mean[j]) || !shadowFinite(m.Scale[j]) || m.Scale[j] <= 0 { return nil, fmt.Errorf("invalid model scaler at %d", j) }
		for k := 0; k < 3; k++ {
			if len(m.Weights[k]) != 30 || !shadowFinite(m.Weights[k][j]) { return nil, fmt.Errorf("invalid model weights at %d", j) }
		}
	}
	for k := 0; k < 3; k++ { if !shadowFinite(m.Bias[k]) { return nil, fmt.Errorf("invalid model bias") } }
	for j := 0; j < 24; j++ {
		if !shadowFinite(c.Mean[j]) || !shadowFinite(c.Scale[j]) || c.Scale[j] <= 0 { return nil, fmt.Errorf("invalid cluster scaler at %d", j) }
	}
	for i, g := range c.Groups {
		if g.ID != i || len(g.Center) != 24 { return nil, fmt.Errorf("invalid group %d", i) }
		for _, v := range g.Center { if !shadowFinite(v) { return nil, fmt.Errorf("invalid group center %d", i) } }
	}
	return s, nil
}

// Submit copies the candle slice so subsequent live resyncs cannot race the
// worker. A busy worker drops shadow work instead of delaying a trading tick.
func (s *shadow30) Submit(candles []Candle, price float64, tick time.Time) {
	if s == nil || len(candles) < 52 || !shadowFinite(price) || price <= 0 { return }
	if len(s.jobs) != 0 { return }
	copyOfCandles := append([]Candle(nil), candles...)
	select {
	case s.jobs <- shadow30Job{candles:copyOfCandles, price:price, tick:tick}:
	default:
		log.Printf("[AI_SHADOW] dropped tick=%s reason=worker_busy", tick.Format(time.RFC3339Nano))
	}
}

func (s *shadow30) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done(): return
		case job := <-s.jobs:
			history, err := s.historyForTick(ctx, job)
			if err != nil { s.logIssue(job.tick, err); continue }
			prediction, group, confidence, probs, err := s.evaluate(history, job.price)
			if err != nil { s.logIssue(job.tick, err); continue }
			last := history[len(history)-1]
			log.Printf("[AI_SHADOW] model=%s decision_id=AI30Shadow_%d tick=%s candle=%s group=%d raw=%s confidence=%.6f p_buy=%.6f p_sell=%.6f p_flat=%.6f no_orders=true",
				s.model.Model.ModelID, job.tick.UnixNano(), job.tick.Format(time.RFC3339Nano), last.Time.Format(time.RFC3339Nano),
				group, prediction, confidence, probs[0], probs[1], probs[2])
		}
	}
}

// Version201 fetches one execution candle at each resync and can therefore
// skip a minute. Repair only the shadow worker's private history. The fetch
// runs in this worker, never in Submit or the trading tick goroutine.
func (s *shadow30) historyForTick(ctx context.Context, job shadow30Job) ([]Candle, error) {
	history := job.candles
	if len(s.repaired) > 0 && (len(history) == 0 || !history[len(history)-1].Time.After(s.repaired[len(s.repaired)-1].Time)) {
		history = s.repaired
	}
	check := func(c []Candle) error {
		if len(c) < 52 { return fmt.Errorf("insufficient one-minute candles") }
		last := len(c)-1
		for i := last-50; i <= last; i++ {
			if !c[i].Time.Equal(c[i-1].Time.Add(time.Minute)) { return fmt.Errorf("one-minute candle gap at %s", c[i].Time.Format(time.RFC3339)) }
		}
		age := job.tick.Sub(c[last].Time)
		if age < 0 || age > 2*time.Minute { return fmt.Errorf("stale one-minute candle at %s (age=%s)", c[last].Time.Format(time.RFC3339), age.Round(time.Second)) }
		return nil
	}
	if err := check(history); err == nil { return history, nil }
	if s.fetch == nil || job.tick.Sub(s.lastRepair) < 15*time.Second {
		return nil, check(history)
	}
	s.lastRepair = job.tick
	fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	batch, err := s.fetch(fetchCtx)
	if err != nil { return nil, fmt.Errorf("shadow candle repair: %w", err) }
	if err := check(batch); err != nil { return nil, fmt.Errorf("shadow candle repair: %w", err) }
	// Keep the earlier warmup candles so EMA initialization stays close to
	// version201's full execution history; replace the overlapping tail only.
	base := job.candles
	if len(s.repaired) > len(base) { base = s.repaired }
	prefix := 0
	for prefix < len(base) && base[prefix].Time.Before(batch[0].Time) { prefix++ }
	s.repaired = append(append([]Candle(nil), base[:prefix]...), batch...)
	if err := check(s.repaired); err != nil { s.repaired = nil; return nil, err }
	log.Printf("[AI_SHADOW] repaired one-minute history candles=%d latest=%s", len(batch), batch[len(batch)-1].Time.Format(time.RFC3339))
	return s.repaired, nil
}

func (s *shadow30) logIssue(tick time.Time, err error) {
	if tick.Sub(s.lastIssue) >= time.Minute {
		log.Printf("[AI_SHADOW] waiting tick=%s reason=%v", tick.Format(time.RFC3339Nano), err)
		s.lastIssue = tick
	}
}

func (s *shadow30) evaluate(history []Candle, price float64) (string, int, float64, [3]float64, error) {
	var probabilities [3]float64
	if len(history) < 52 || !shadowFinite(price) || price <= 0 { return "", -1, 0, probabilities, fmt.Errorf("insufficient history or invalid price") }
	// Only the worker's private snapshot is changed. This mirrors the old live
	// tick convention; the trading model and historical candles are untouched.
	last := len(history)-1
	originalLast := history[last]
	defer func() { history[last] = originalLast }()
	if price > history[last].High { history[last].High = price }
	if price < history[last].Low { history[last].Low = price }
	history[last].Close = price
	for i := last-51; i <= last; i++ {
		if history[i].Close <= 0 || !shadowFinite(history[i].Close) || !shadowFinite(history[i].Volume) ||
			history[i].Volume < 0 || history[i].High < history[i].Low { return "", -1, 0, probabilities, fmt.Errorf("invalid candle at %d", i) }
		if i > last-51 && !history[i].Time.Equal(history[i-1].Time.Add(time.Minute)) { return "", -1, 0, probabilities, fmt.Errorf("one-minute candle gap at %s", history[i].Time.Format(time.RFC3339Nano)) }
	}
	features, err := shadow24Features(history)
	if err != nil { return "", -1, 0, probabilities, err }
	group := -1
	best := math.Inf(1)
	for i, g := range s.cluster.Groups {
		distance := 0.0
		for j, v := range features {
			delta := (v-s.cluster.Mean[j])/s.cluster.Scale[j]-g.Center[j]
			distance += delta*delta
		}
		if distance < best { group, best = i, distance }
	}
	if group < 0 || !shadowFinite(best) { return "", -1, 0, probabilities, fmt.Errorf("cluster assignment failed") }
	features = append(features, make([]float64, 6)...)
	features[24+group] = 1
	m := s.model.Model
	var logits [3]float64
	peak := math.Inf(-1)
	for k := 0; k < 3; k++ {
		logits[k] = m.Bias[k]
		for j, v := range features { logits[k] += m.Weights[k][j]*(v-m.Mean[j])/m.Scale[j] }
		if logits[k] > peak { peak = logits[k] }
	}
	total := 0.0
	for k := 0; k < 3; k++ { probabilities[k] = math.Exp(logits[k]-peak); total += probabilities[k] }
	if !shadowFinite(total) || total <= 0 { return "", -1, 0, probabilities, fmt.Errorf("invalid softmax") }
	for k := 0; k < 3; k++ { probabilities[k] /= total }
	winner := 2
	for _, k := range []int{0,1} { if probabilities[k] > probabilities[winner] { winner = k } }
	if winner == 0 && probabilities[0] < m.BuyGate.Threshold || winner == 1 && probabilities[1] < m.SellGate.Threshold { winner = 2 }
	return []string{"BUY", "SELL", "FLAT"}[winner], group, probabilities[winner], probabilities, nil
}

// shadow24Features follows the frozen training feature order, with three
// chronological values per indicator followed by six current-candle values.
func shadow24Features(c []Candle) ([]float64, error) {
	i := len(c)-1
	if i < 51 { return nil, fmt.Errorf("insufficient one-minute feature history") }
	close := make([]float64, len(c))
	for j := range c { close[j] = c[j].Close }
	ema4, ema8 := shadowEMA(close, 4), shadowEMA(close, 8)
	ema20, ema50 := shadowEMA(close, 20), shadowEMA(close, 50)
	atr14, atr50 := shadowATR(c, 14), shadowATR(c, 50)
	rsi := shadowRSI(c, 14)
	plus, minus, adx := shadowDMI(c, 14)
	_, _, histogram := shadowMACD(close, 12, 26, 9)
	mfi, zscore, wr := shadowMFI(c, 14), shadowZScore(c, 20), shadowWilliamsR(c, 14)
	ratio := func(n, d float64) float64 { if d == 0 { return 0 }; return n/d }
	v := make([]float64, 0, 24)
	for j := i-2; j <= i; j++ { v = append(v, ratio(ema4[j]-ema8[j],atr14[j])) }
	for j := i-2; j <= i; j++ { v = append(v, ratio(ema20[j]-ema50[j],atr14[j])) }
	for j := i-2; j <= i; j++ { v = append(v, (rsi[j]-50)/50) }
	for j := i-2; j <= i; j++ { v = append(v, ratio(plus[j]-minus[j],plus[j]+minus[j])) }
	for j := i-2; j <= i; j++ { v = append(v, adx[j]/100) }
	for j := i-2; j <= i; j++ { v = append(v, ratio(histogram[j],atr14[j])) }
	v = append(v, atr14[i]/close[i], ratio(atr14[i],atr50[i])-1, (mfi[i]-50)/50, zscore[i], (wr[i]+50)/50)
	volumeMean := 0.0
	for j := i-19; j <= i; j++ { volumeMean += c[j].Volume }
	volumeMean /= 20
	variance := 0.0
	for j := i-19; j <= i; j++ { delta := c[j].Volume-volumeMean; variance += delta*delta }
	v = append(v, ratio(c[i].Volume-volumeMean,math.Sqrt(variance/20)))
	for j, x := range v { if !shadowFinite(x) { return nil, fmt.Errorf("nonfinite feature at %d", j) } }
	if len(v) != 24 { return nil, fmt.Errorf("invalid feature width %d", len(v)) }
	return v, nil
}

func startShadow30(ctx context.Context, broker Broker, productID, gateTF string) *shadow30 {
	modelPath, clusterPath := strings.TrimSpace(os.Getenv("AI_SHADOW_30_MODEL")), strings.TrimSpace(os.Getenv("AI_SHADOW_30_CLUSTER"))
	if modelPath == "" && clusterPath == "" { return nil }
	if modelPath == "" || clusterPath == "" { log.Printf("[AI_SHADOW] disabled: both artifact paths required"); return nil }
	product := strings.NewReplacer("-", "", "/", "").Replace(strings.ToUpper(productID))
	if product != "BTCUSDT" || strings.ToUpper(gateTF) != "ONE_MINUTE" {
		log.Printf("[AI_SHADOW] disabled: requires BTCUSDT ONE_MINUTE execution candles")
		return nil
	}
	s, err := loadShadow30(modelPath, clusterPath)
	if err != nil { log.Printf("[AI_SHADOW] disabled: %v", err); return nil }
	s.fetch = func(fetchCtx context.Context) ([]Candle, error) {
		return broker.GetRecentCandles(fetchCtx, productID, gateTF, 120)
	}
	go s.Run(ctx)
	log.Printf("[AI_SHADOW] started model=%s schema=%s no_orders=true", s.model.Model.ModelID, s.model.Model.Version)
	return s
}
