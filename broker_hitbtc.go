// FILE: broker_hitbtc.go
// Package main — HTTP broker against the HitBTC FastAPI sidecar.
// NOTE: This is a minimal clone of broker_bridge.go with only base URL and Name() changed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HitbtcBridge struct {
	base string
	hc   *http.Client
}

func NewHitbtcBridge(base string) *HitbtcBridge {
	if strings.TrimSpace(base) == "" {
		// default to the docker-compose service for HitBTC bridge
		base = "http://bridge_hitbtc:8788"
	}
	base = strings.TrimRight(base, "/")
	return &HitbtcBridge{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (h *HitbtcBridge) Name() string { return "hitbtc-bridge" }

// --- Price / Product ---

func (h *HitbtcBridge) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/product/%s", h.base, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("bridge product %d: %s", resp.StatusCode, string(xb))
	}
	var payload struct {
		ProductID string  `json:"product_id"`
		Price     float64 `json:"price"`
		TS        any     `json:"ts"`
		Stale     bool    `json:"stale"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	return payload.Price, nil
}

// --- Candles ---

func (h *HitbtcBridge) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 350
	}
	u := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d",
		h.base, url.QueryEscape(product), url.QueryEscape(granularity), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge candles %d: %s", resp.StatusCode, string(xb))
	}

	// IMPORTANT: keep this anonymous struct EXACTLY as in broker_bridge.go
	var out struct {
		Candles []struct {
			Start  string
			Open   string
			High   string
			Low    string
			Close  string
			Volume string
		} `json:"candles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return toCandles(out.Candles), nil
}

// --- Live balances / equity helpers ---

func (h *HitbtcBridge) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	u := fmt.Sprintf("%s/balance/base?product_id=%s", h.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, 0, err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return "", 0, 0, fmt.Errorf("bridge balance/base %d: %s", resp.StatusCode, string(xb))
	}
	var payload struct {
		Asset     string `json:"asset"`
		Available string `json:"available"`
		Step      string `json:"step"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", 0, 0, err
	}
	return payload.Asset, parseFloat(payload.Available), parseFloat(payload.Step), nil
}

func (h *HitbtcBridge) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	u := fmt.Sprintf("%s/balance/quote?product_id=%s", h.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, 0, err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return "", 0, 0, fmt.Errorf("bridge balance/quote %d: %s", resp.StatusCode, string(xb))
	}
	var payload struct {
		Asset     string `json:"asset"`
		Available string `json:"available"`
		Step      string `json:"step"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", 0, 0, err
	}
	return payload.Asset, parseFloat(payload.Available), parseFloat(payload.Step), nil
}

// --- Orders (market by quote), identical body/shape to broker_bridge.go ---

func (h *HitbtcBridge) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	body := map[string]any{
		"product_id": product,
		"side":       side, // IMPORTANT: no .String(); mirror broker_bridge.go
		"quote_size": fmt.Sprintf("%.8f", quoteUSD),
	}
	data, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/market", h.base)

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order/market %d: %s", resp.StatusCode, string(xb))
	}

	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}

	// Enrich via GET /order/{order_id}, identical to broker_bridge.go
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o2, err := h.fetchOrder(ctx, product, ord.ID)
		if err == nil && (o2.BaseSize > 0 || o2.QuoteSpent > 0) {
			return o2, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return &ord, nil
}

func (h *HitbtcBridge) fetchOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	u := fmt.Sprintf("%s/order/%s", h.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order get %d: %s", resp.StatusCode, string(xb))
	}
	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}
	return &ord, nil
}

// --- helpers (EXACTLY like broker_bridge.go) ---

// func toCandles(rows []struct {
// 	Start  string
// 	Open   string
// 	High   string
// 	Low    string
// 	Close  string
// 	Volume string
// }) []Candle {
// 	out := make([]Candle, 0, len(rows))
// 	for _, r := range rows {
// 		out = append(out, Candle{
// 			Time:   toUnixTime(r.Start),
// 			Open:   parseFloat(r.Open),
// 			High:   parseFloat(r.High),
// 			Low:    parseFloat(r.Low),
// 			Close:  parseFloat(r.Close),
// 			Volume: parseFloat(r.Volume),
// 		})
// 	}
// 	return out
// }

// func toUnixTime(secStr string) time.Time {
// 	sec := int64(parseFloat(secStr))
// 	return time.Unix(sec, 0).UTC()
// }

// func parseFloat(s string) float64 {
// 	s = strings.TrimSpace(s)
// 	if s == "" {
// 		return 0
// 	}
// 	var f float64
// 	fmt.Sscanf(s, "%f", &f)
// 	return f
// }
