// FILE: broker_binance.go
// Package main — HTTP broker against the Binance FastAPI sidecar.
// NOTE: This is a minimal clone of broker_bridge.go with only base URL and Name() changed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type BinanceBridge struct {
	base string
	hc   *http.Client
}

func NewBinanceBridge(base string) *BinanceBridge {
	if strings.TrimSpace(base) == "" {
		// default to the docker-compose service for Binance bridge
		base = "http://bridge_binance:8789"
	}
	base = strings.TrimRight(base, "/")
	b := &BinanceBridge{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
	return b
}

func (b *BinanceBridge) Name() string { return "binance-bridge" }

// --- Price / Product ---

func (b *BinanceBridge) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/product/%s", b.base, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := b.hc.Do(req)
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

func (b *BinanceBridge) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 350
	}
	u := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d",
		b.base, url.QueryEscape(product), url.QueryEscape(granularity), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.hc.Do(req)
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

// --- Live balances / equity helpers (mirror broker_bridge.go) ---

func (b *BinanceBridge) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	u := fmt.Sprintf("%s/balance/base?product_id=%s", b.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, 0, err
	}
	resp, err := b.hc.Do(req)
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

func (b *BinanceBridge) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	u := fmt.Sprintf("%s/balance/quote?product_id=%s", b.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, 0, err
	}
	resp, err := b.hc.Do(req)
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

// --- Orders (market by quote), exact body/shape as broker_bridge.go expects ---

func (b *BinanceBridge) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	body := map[string]any{
		"product_id": product,
		"side":       side, // IMPORTANT: mirror broker_bridge.go (no .String())
		"quote_size": fmt.Sprintf("%.8f", quoteUSD),
	}
	data, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/market", b.base)

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order/market %d: %s", resp.StatusCode, string(xb))
	}

	// Decode into stringly-typed JSON and then build PlacedOrder to tolerate string-number fields.
	var ordJSON placedOrderJSON
	if err := json.NewDecoder(resp.Body).Decode(&ordJSON); err != nil {
		return nil, err
	}
	ord := toPlacedOrder(ordJSON)

	// Enrich via GET /order/{order_id}, identical to broker_bridge.go
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o2, err := b.fetchOrder(ctx, product, ord.ID)
		if err == nil && (o2.BaseSize > 0 || o2.QuoteSpent > 0) {
			return o2, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ord, nil
}

// --- NEW: Post-only limit (LIMIT_MAKER) with size/price snapping to exchange filters ---
// Returns the created order ID (best-effort), or an error.
func (b *BinanceBridge) PlaceLimitPostOnly(ctx context.Context, product string, side OrderSide, limitPrice, baseSize float64) (string, error) {
	// Snap base size to step (floor) using the bridge's balance endpoint (same as broker_bridge.go style).
	_, _, baseStep, err := b.GetAvailableBase(ctx, product)
	if err == nil && baseStep > 0 {
		steps := math.Floor(baseSize/baseStep + 1e-12)
		baseSize = steps * baseStep
	}
	if baseSize <= 0 {
		return "", fmt.Errorf("base size after snap is zero")
	}

	// --- MINIMAL ADD: use cache-only exchange filters (LOT_SIZE.StepSize, PRICE_FILTER.TickSize) and apply snapping ---
	f := b.getExchangeFiltersCached(product) // zero-latency cache lookup
	if f.StepSize > 0 {
		baseSize = snapDownBinance(baseSize, f.StepSize)
	}
	if f.TickSize > 0 {
		limitPrice = snapDownBinance(limitPrice, f.TickSize)
	}
	if baseSize <= 0 {
		return "", fmt.Errorf("base size after exchange step snap is zero")
	}
	if limitPrice <= 0 {
		return "", fmt.Errorf("limit price after tick snap is zero")
	}

	// Format numbers with decimals derived from step/tick (falls back to previous behavior when unknown).
	priceStr := formatWithStepBinance(limitPrice, f.TickSize, 12)
	sizeStr := formatWithStepBinance(baseSize, f.StepSize, 12)

	body := map[string]any{
		"product_id":  product,
		"side":        side, // "BUY" or "SELL"
		"limit_price": priceStr,
		"base_size":   sizeStr,
	}
	data, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/limit_post_only", b.base)

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("bridge order/limit_post_only %d: %s", resp.StatusCode, string(xb))
	}
	var out struct {
		OrderID string `json:"order_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.OrderID, nil
}

// --- Interface-conforming public methods for polling/cancel ---

func (b *BinanceBridge) GetOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	return b.fetchOrder(ctx, product, orderID)
}

func (b *BinanceBridge) CancelOrder(ctx context.Context, product, orderID string) error {
	u := fmt.Sprintf("%s/order/%s", b.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}
	resp, err := b.hc.Do(req)
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

func (b *BinanceBridge) fetchOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	u := fmt.Sprintf("%s/order/%s", b.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order get %d: %s", resp.StatusCode, string(xb))
	}

	// Decode into tolerant JSON (string fields) then convert to PlacedOrder.
	var ordJSON placedOrderJSON
	if err := json.NewDecoder(resp.Body).Decode(&ordJSON); err != nil {
		return nil, err
	}
	ord := toPlacedOrder(ordJSON)
	return ord, nil
}

// --- helpers (EXACTLY like broker_bridge.go) ---

func toCandles(rows []struct {
	Start  string
	Open   string
	High   string
	Low    string
	Close  string
	Volume string
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

// --- MINIMAL ADD: exchange filters cache & formatting helpers (best-effort; no behavior changes if unavailable) ---

var (
	fMuBinance    sync.Mutex
	fCacheBinance = map[string]ExFilters{} // key: product/symbol
)

// helper: derive quote step from precision (e.g., precision=2 -> 0.01)
func quoteStepFromPrecision(p int) float64 {
	if p <= 0 {
		return 0
	}
	f := 1.0
	for i := 0; i < p; i++ {
		f /= 10.0
	}
	return f
}

// getExchangeFilters calls a best-effort sidecar endpoint to fetch Binance filters.
// If the endpoint is missing or fails, it returns zero values so baseline behavior is preserved.
func (b *BinanceBridge) GetExchangeFilters(ctx context.Context, product string) (ExFilters, error) {
	key := strings.TrimSpace(product)

	// cache hit
	fMuBinance.Lock()
	if v, ok := fCacheBinance[key]; ok && (v.StepSize > 0 || v.TickSize > 0 || v.PriceTick > 0 || v.BaseStep > 0 || v.QuoteStep > 0 || v.MinNotional > 0) {
		fMuBinance.Unlock()
		return v, nil
	}
	fMuBinance.Unlock()

	// try: GET /exchange/filters?product_id=<product>
	u := fmt.Sprintf("%s/exchange/filters?product_id=%s", b.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return ExFilters{}, err
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return ExFilters{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// best-effort: do not treat as fatal
		io.Copy(io.Discard, resp.Body)
		return ExFilters{}, fmt.Errorf("filters %d", resp.StatusCode)
	}

	// Accept BOTH legacy keys (step_size/tick_size) and normalized keys (price_tick/base_step/quote_step/min_notional).
	var payload struct {
		// legacy (already supported)
		StepSize string `json:"step_size"` // LOT_SIZE.StepSize
		TickSize string `json:"tick_size"` // PRICE_FILTER.TickSize

		// normalized (preferred)
		PriceTick   string `json:"price_tick"`   // normalized price tick
		BaseStep    string `json:"base_step"`    // normalized base step
		QuoteStep   string `json:"quote_step"`   // normalized quote step
		MinNotional string `json:"min_notional"` // normalized min notional

		// optional hint if sidecar exposes precision for quoteOrderQty fallback
		QuotePrecision int `json:"quote_precision"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ExFilters{}, err
	}

	// Build ExFilters with robust fallbacks.
	f := ExFilters{
		// legacy
		StepSize: parseFloat(payload.StepSize),
		TickSize: parseFloat(payload.TickSize),

		// normalized (preferred)
		PriceTick:   parseFloat(payload.PriceTick),
		BaseStep:    parseFloat(payload.BaseStep),
		QuoteStep:   parseFloat(payload.QuoteStep),
		MinNotional: parseFloat(payload.MinNotional),
	}

	// If normalized fields are missing, alias from legacy where it makes sense.
	if f.PriceTick <= 0 && f.TickSize > 0 {
		f.PriceTick = f.TickSize
	}
	if f.BaseStep <= 0 && f.StepSize > 0 {
		f.BaseStep = f.StepSize
	}

	// Derive quote step from precision if not present.
	if f.QuoteStep <= 0 && payload.QuotePrecision > 0 {
		f.QuoteStep = quoteStepFromPrecision(payload.QuotePrecision)
	}

	// As a last resort, apply a conservative default for common stablecoin quotes (USDT, USDC, USD, BUSD, FDUSD).
	if f.QuoteStep <= 0 {
		upper := strings.ToUpper(strings.TrimSpace(product))
		for _, q := range []string{"USDT", "USDC", "USD", "BUSD", "FDUSD"} {
			if strings.HasSuffix(upper, q) {
				f.QuoteStep = 0.01
				break
			}
		}
	}

	// cache store
	fMuBinance.Lock()
	fCacheBinance[key] = f
	fMuBinance.Unlock()

	return f, nil
}

// Cache-only lookup used by the hot path (no network).
func (b *BinanceBridge) getExchangeFiltersCached(product string) ExFilters {
	key := strings.TrimSpace(product)
	fMuBinance.Lock()
	v := fCacheBinance[key]
	fMuBinance.Unlock()
	return v
}

func snapDownBinance(x, step float64) float64 {
	if step <= 0 {
		return x
	}
	n := math.Floor(x/step + 1e-12)
	return n * step
}

// formatWithStepBinance formats x using the decimal precision implied by step.
// If step <= 0, it falls back to a fixed maximum precision given by fallbackDec.
func formatWithStepBinance(x, step float64, fallbackDec int) string {
	if step > 0 {
		// derive decimals from step (e.g., 0.001000 -> 3)
		s := strconv.FormatFloat(step, 'f', -1, 64)
		dec := 0
		if i := strings.IndexByte(s, '.'); i >= 0 {
			dec = len(s) - i - 1
			// trim trailing zeros
			for dec > 0 && s[len(s)-1] == '0' {
				s = s[:len(s)-1]
				dec--
			}
		}
		if dec < 0 {
			dec = 0
		}
		return strconv.FormatFloat(x, 'f', dec, 64)
	}
	if fallbackDec < 0 {
		fallbackDec = 0
	}
	return strconv.FormatFloat(x, 'f', fallbackDec, 64)
}

// --- MINIMAL ADD: tolerant JSON types for order/fill, and converter to PlacedOrder ---

type fillJSON struct {
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
}

type placedOrderJSON struct {
	ID            string     `json:"id"`
	Price         string     `json:"price"`
	BaseSize      string     `json:"base_size"`
	QuoteSpent    string     `json:"quote_spent"`
	CommissionUSD string     `json:"commission_usd"`
	Fills         []fillJSON `json:"fills"`
}

// toPlacedOrder converts a tolerant JSON struct (string fields) into the strongly-typed PlacedOrder.
func toPlacedOrder(j placedOrderJSON) *PlacedOrder {
	out := &PlacedOrder{
		ID:            j.ID,
		Price:         parseFloat(j.Price),
		BaseSize:      parseFloat(j.BaseSize),
		QuoteSpent:    parseFloat(j.QuoteSpent),
		CommissionUSD: parseFloat(j.CommissionUSD),
	}
	// If the API provided only fills, sum commission best-effort if CommissionUSD is empty.
	if out.CommissionUSD == 0 && len(j.Fills) > 0 {
		var sum float64
		for _, f := range j.Fills {
			sum += parseFloat(f.Commission)
		}
		out.CommissionUSD = sum
	}
	return out
}
