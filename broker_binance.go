// FILE: broker_binance.go
// Package main — HTTP broker against the Binance WS/REST FastAPI sidecar.
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

type BinanceBridge struct {
	base string
	hc   *http.Client
}

func NewBinanceBridge(base string) *BinanceBridge {
	if strings.TrimSpace(base) == "" {
		base = "http://bridge_binance:8789"
	}
	base = strings.TrimRight(base, "/")
	return &BinanceBridge{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (bb *BinanceBridge) Name() string { return "binance-bridge" }

// --- Price ---

func (bb *BinanceBridge) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/product/%s", bb.base, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := bb.hc.Do(req)
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

func (bb *BinanceBridge) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 350
	}
	u := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d", bb.base, url.QueryEscape(product), url.QueryEscape(granularity), limit)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := bb.hc.Do(req)
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

// --- Balances (live equity helpers) ---

func (bb *BinanceBridge) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	base, _ := splitProductID(product)
	u := fmt.Sprintf("%s/balance/base?product_id=%s", bb.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return base, 0, 0, err
	}
	resp, err := bb.hc.Do(req)
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

func (bb *BinanceBridge) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	_, quote := splitProductID(product)
	u := fmt.Sprintf("%s/balance/quote?product_id=%s", bb.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return quote, 0, 0, err
	}
	resp, err := bb.hc.Do(req)
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

// --- Orders (market by quote USD) ---

func (bb *BinanceBridge) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	body := map[string]any{
		"product_id": product,
		"side":       side.String(),
		"quote_size": fmt.Sprintf("%.8f", quoteUSD),
	}
	b, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/market", bb.base)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := bb.hc.Do(req)
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
		o2, err := bb.fetchOrder(ctx, product, ord.OrderID)
		if err == nil && (o2.FilledSize > 0 || o2.ExecutedValue > 0) {
			return o2, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Fallback to original ord (could be still-new order)
	return &ord, nil
}

func (bb *BinanceBridge) fetchOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	u := fmt.Sprintf("%s/order/%s", bb.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := bb.hc.Do(req)
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

// --- helpers shared with broker_bridge ---

func toCandles(rows []struct {
	Start, Open, High, Low, Close, Volume string
}) []Candle {
	out := make([]Candle, 0, len(rows))
	for _, r := range rows {
		out = append(out, Candle{
			Time:   toUnixTime(r.Start),
			Open:   parseFloat(r.Open),
			High:   parseFloat(r.High),
			Low:    parseFloat(r.Low),
			Close:  parseFloat(r.Close),
			Volume: parseFloat(r.Volume),
		})
	}
	return out
}

func toUnixTime(secStr string) time.Time {
	sec := int64(parseFloat(secStr))
	return time.Unix(sec, 0).UTC()
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
