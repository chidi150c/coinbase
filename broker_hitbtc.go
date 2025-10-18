// FILE: broker_hitbtc.go
// Package main — HTTP broker against the HitBTC FastAPI sidecar.
// NOTE: This is a minimal clone of broker_bridge.go with only base URL and Name() changed.
// Minimal edits added:
//   - PlaceLimitPostOnly(ctx, product, side, limitPrice, baseSize) (string, error)
//   - GetOrder(ctx, product, orderID) (*PlacedOrder, error)
//   - CancelOrder(ctx, product, orderID) error
// These enable post-only (maker) entries with a cancel-and-market fallback in trader.go.
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

func (h *HitbtcBridge) GetExchangeFilters(ctx context.Context, product string)(ExFilters, error){
	return ExFilters{}, nil
}
// --- Maker-first additions ---

// PlaceLimitPostOnly places a post-only limit order via the HitBTC bridge.
// The bridge maps this to type=limit with postOnly=true (timeInForce=PO).
func (h *HitbtcBridge) PlaceLimitPostOnly(ctx context.Context, product string, side OrderSide, limitPrice, baseSize float64) (string, error) {
	// Snap base size to the exchange base step (floor).
	_, _, step, err := h.GetAvailableBase(ctx, product)
	if err == nil && step > 0 {
		steps := mathFloorSafe(baseSize/step)
		b := steps * step
		if b > 0 {
			baseSize = b
		}
	}
	if baseSize <= 0 || limitPrice <= 0 {
		return "", fmt.Errorf("invalid limit params: base=%.10f price=%.10f", baseSize, limitPrice)
	}

	body := map[string]any{
		"product_id":  product,
		"side":        side, // "BUY" | "SELL"
		"limit_price": fmt.Sprintf("%.8f", limitPrice),
		"base_size":   fmt.Sprintf("%.8f", baseSize),
	}
	data, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/limit_post_only", h.base)

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("bridge order/limit_post_only %d: %s", resp.StatusCode, string(xb))
	}

	// Primary: expect { "order_id": "..." }
	var ok struct {
		OrderID string `json:"order_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ok); err == nil && strings.TrimSpace(ok.OrderID) != "" {
		return ok.OrderID, nil
	}

	// Fallback: some bridges may return the full order; try PlacedOrder.
	var alt PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&alt); err == nil && strings.TrimSpace(alt.ID) != "" {
		return alt.ID, nil
	}

	// If we reach here, we couldn't parse a valid ID.
	return "", fmt.Errorf("bridge returned no order_id for limit_post_only")
}

// GetOrder exposes order fetch for polling fills (exported wrapper).
func (h *HitbtcBridge) GetOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	return h.fetchOrder(ctx, product, orderID)
}

// CancelOrder cancels a resting order on the bridge (idempotent).
func (h *HitbtcBridge) CancelOrder(ctx context.Context, product, orderID string) error {
	u := fmt.Sprintf("%s/order/%s?product_id=%s", h.base, url.PathEscape(orderID), url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge order cancel %d: %s", resp.StatusCode, string(xb))
	}
	return nil
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

// --- helpers (shared) ---

// mathFloorSafe is a tiny wrapper to avoid importing math just to floor on a positive number.
func mathFloorSafe(x float64) float64 {
	if x <= 0 {
		return 0
	}
	ix := int64(x)
	f := float64(ix)
	if f > x {
		return f - 1
	}
	// adjust down if int64 cast rounded up (shouldn't happen for positive)
	for f > x {
		f -= 1
	}
	// bump up while we can (shouldn't in normal case)
	for f+1 <= x {
		f += 1
	}
	return f
}

// NOTE: Helper functions toCandles/parseFloat/toUnixTime are provided in another broker file
// (e.g., broker_binance.go). They are package-level, so we reuse them here.
