// FILE: broker_hitbtc.go
// Package main — HTTP broker against the HitBTC WS/REST FastAPI sidecar.
// Mirrors broker_bridge.go behavior and contract; only Name() and defaults differ.
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
		base = "http://bridge_hitbtc:8788"
	}
	base = strings.TrimRight(base, "/")
	return &HitbtcBridge{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (hb *HitbtcBridge) Name() string { return "hitbtc-bridge" }

// --- Price ---

func (hb *HitbtcBridge) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/product/%s", hb.base, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := hb.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("bridge product %d: %s", resp.StatusCode, string(b))
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

func (hb *HitbtcBridge) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 350
	}
	u := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d", hb.base, url.QueryEscape(product), url.QueryEscape(granularity), limit)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge candles %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Candles []struct {
			Start  string `json:"start"`
			Open   string `json:"open"`
			High   string `json:"high"`
			Low    string `json:"low"`
			Close  string `json:"close"`
			Volume string `json:"volume"`
		} `json:"candles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return toCandles(out.Candles), nil
}

// --- Balances ---

func (hb *HitbtcBridge) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	base, _ := splitProductID(product)
	u := fmt.Sprintf("%s/balance/base?product_id=%s", hb.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return base, 0, 0, err
	}
	resp, err := hb.hc.Do(req)
	if err != nil {
		return base, 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return base, 0, 0, fmt.Errorf("bridge balance/base %d: %s", resp.StatusCode, string(b))
	}
	var payload struct {
		Asset     string `json:"asset"`
		Available string `json:"available"`
		Step      string `json:"step"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return base, 0, 0, err
	}
	asset = payload.Asset
	available = parseFloat(payload.Available)
	step = parseFloat(payload.Step)
	return
}

func (hb *HitbtcBridge) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	_, quote := splitProductID(product)
	u := fmt.Sprintf("%s/balance/quote?product_id=%s", hb.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return quote, 0, 0, err
	}
	resp, err := hb.hc.Do(req)
	if err != nil {
		return quote, 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return quote, 0, 0, fmt.Errorf("bridge balance/quote %d: %s", resp.StatusCode, string(b))
	}
	var payload struct {
		Asset     string `json:"asset"`
		Available string `json:"available"`
		Step      string `json:"step"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return quote, 0, 0, err
	}
	asset = payload.Asset
	available = parseFloat(payload.Available)
	step = parseFloat(payload.Step)
	return
}

// --- Orders ---

func (hb *HitbtcBridge) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	body := map[string]any{
		"product_id": product,
		"side":       side.String(),
		"quote_size": fmt.Sprintf("%.8f", quoteUSD),
	}
	b, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/market", hb.base)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hb.hc.Do(req)
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

	// Enrich with fills via GET /order/{order_id} (micro-retry)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o2, err := hb.fetchOrder(ctx, product, ord.OrderID)
		if err == nil && (o2.FilledSize > 0 || o2.ExecutedValue > 0) {
			return o2, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return &ord, nil
}

func (hb *HitbtcBridge) fetchOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	u := fmt.Sprintf("%s/order/%s", hb.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order get %d: %s", resp.StatusCode, string(b))
	}
	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}
	return &ord, nil
}
