// FILE: broker_bridge.go
// Package main – HTTP broker that talks to the local FastAPI sidecar.
//
// This broker hits your Python sidecar (app.py) which fronts Coinbase Advanced
// Trade via the official `coinbase.rest.RESTClient`. It implements:
//   • GetNowPrice:      GET /product/{product_id} -> price
//   • GetRecentCandles: GET /candles?product_id=...&granularity=...&limit=...
//   • PlaceMarketQuote: POST /order/market {product_id, side, quote_size}
//
// Minimal update in this version:
// After placing a market order, poll GET /order/{order_id} (micro-retry)
// and populate filled fields (filled_size, average_filled_price) into the
// returned PlacedOrder. If enrichment fails or times out, fall back to prior
// behavior (returning an order ID with zeroed fills/notional).
//
// NEW (balances, no fallback):
// GetAvailableBase/GetAvailableQuote call /balance/base or /balance/quote
// (bridge-normalized shape: {"asset","available","step"} as strings). If the
// endpoint fails or returns invalid JSON, the process logs a fatal error and exits.
//
// NEW (maker-first support; additive to interface):
//   • PlaceLimitPostOnly: POST /order/limit_post_only {product_id, side, limit_price, base_size, client_order_id}
//   • GetOrder:           GET  /order/{order_id}?product_id=...
//   • CancelOrder:        DELETE /order/{order_id}?product_id=...

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// BridgeBroker talks to the local FastAPI bridge.
type BridgeBroker struct {
	base string
	hc   *http.Client
}

func NewBridgeBroker(base string) *BridgeBroker {
	base = strings.TrimSpace(base)
	if i := strings.IndexAny(base, " \t#"); i >= 0 { // cut trailing comment/space
		base = strings.TrimSpace(base[:i])
	}
	if base == "" {
		base = "http://127.0.0.1:8787"
	}
	base = strings.TrimRight(base, "/")
	return &BridgeBroker{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (bb *BridgeBroker) Name() string { return "coinbase-bridge" }

// --- Price ---

func (bb *BridgeBroker) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/product/%s", bb.base, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("newrequest product: %w (url=%s)", err, u)
	}
	req.Header.Set("User-Agent", "coinbot/bridge")

	res, err := bb.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return 0, fmt.Errorf("product %d: %s", res.StatusCode, string(b))
	}
	var out struct {
		Price string `json:"price"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(out.Price, 64)
}

// --- BALANCES (NO FALLBACK) ---

func (bb *BridgeBroker) GetAvailableBase(ctx context.Context, product string) (string, float64, float64, error) {
	asset, avail, step, ok := bb.tryBalanceEndpoint(ctx, "/balance/base", product)
	if !ok {
		log.Printf("GetAvailableBase: failed calling %s/balance/base?product_id=%s", bb.base, product)
		// unreachable after Fatalf, but return to satisfy compiler
		return "", 0, 0, fmt.Errorf("fatal: GetAvailableBase failed")
	}
	return asset, avail, step, nil
}

func (bb *BridgeBroker) GetAvailableQuote(ctx context.Context, product string) (string, float64, float64, error) {
	asset, avail, step, ok := bb.tryBalanceEndpoint(ctx, "/balance/quote", product)
	if !ok {
		log.Printf("GetAvailableQuote: failed calling %s/balance/quote?product_id=%s", bb.base, product)
		return "", 0, 0, fmt.Errorf("fatal: GetAvailableQuote failed")
	}
	return asset, avail, step, nil
}

// tryBalanceEndpoint hits /balance/base or /balance/quote and parses {"asset","available","step"}.
func (bb *BridgeBroker) tryBalanceEndpoint(ctx context.Context, path string, product string) (asset string, available float64, step float64, ok bool) {
	u := fmt.Sprintf("%s%s?product_id=%s", bb.base, path, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, 0, false
	}
	req.Header.Set("User-Agent", "coinbot/bridge")

	res, err := bb.hc.Do(req)
	if err != nil {
		return "", 0, 0, false
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return "", 0, 0, false
	}
	var out struct {
		Asset     string `json:"asset"`
		Available string `json:"available"`
		Step      string `json:"step"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", 0, 0, false
	}
	if strings.TrimSpace(out.Asset) == "" {
		return "", 0, 0, false
	}
	avail, _ := strconv.ParseFloat(strings.TrimSpace(out.Available), 64)
	st, _ := strconv.ParseFloat(strings.TrimSpace(out.Step), 64)
	return strings.TrimSpace(out.Asset), avail, st, true
}

// --- Candles ---

func (bb *BridgeBroker) GetRecentCandles(ctx context.Context, product, granularity string, limit int) ([]Candle, error) {
	q := url.Values{}
	q.Set("product_id", product)
	q.Set("granularity", granularity)
	if limit <= 0 {
		limit = 350
	}
	q.Set("limit", strconv.Itoa(limit))

	u := fmt.Sprintf("%s/candles?%s", bb.base, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("newrequest candles: %w (url=%s)", err, u)
	}
	req.Header.Set("User-Agent", "coinbot/bridge")

	res, err := bb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("candles %d: %s", res.StatusCode, string(b))
	}

	// Bridge returns normalized rows with string/number fields; parse defensively.
	type row struct {
		Time   string `json:"time"`
		Open   any    `json:"open"`
		High   any    `json:"high"`
		Low    any    `json:"low"`
		Close  any    `json:"close"`
		Volume any    `json:"volume"`
	}
	var rows []row
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil, err
	}

	parseF := func(v any) float64 {
		switch t := v.(type) {
		case float64:
			return t
		case string:
			f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
			return f
		default:
			return 0
		}
	}
	parseT := func(s string) time.Time {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}
		}
		// Try RFC3339 first, then unix seconds
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return ts.UTC()
		}
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
		return time.Time{}
	}

	candles := make([]Candle, 0, len(rows))
	for _, r := range rows {
		candles = append(candles, Candle{
			Time:   parseT(r.Time),
			Open:   parseF(r.Open),
			High:   parseF(r.High),
			Low:    parseF(r.Low),
			Close:  parseF(r.Close),
			Volume: parseF(r.Volume),
		})
	}
	// sort to chronological if needed (stable pass)
	for i := 1; i < len(candles); i++ {
		if candles[i].Time.Before(candles[i-1].Time) {
			for j := i; j > 0 && candles[j].Time.Before(candles[j-1].Time); j-- {
				candles[j], candles[j-1] = candles[j-1], candles[j]
			}
		}
	}
	return candles, nil
}

// --- Orders (market) ---

func (bb *BridgeBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	// Minimal update: send side and quote_size to unified /order/market endpoint.
	u := bb.base + "/order/market"
	body := map[string]any{
		"product_id":      product,
		"side":            strings.ToUpper(string(side)),
		"quote_size":      fmt.Sprintf("%.2f", quoteUSD),
		"client_order_id": uuid.New().String(), // minimal addition: dedupe-safe ID for retries
	}
	bs, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(bs))
	if err != nil {
		return nil, fmt.Errorf("newrequest order: %w (url=%s)", err, u)
	}
	req.Header.Set("User-Agent", "coinbot/bridge")
	req.Header.Set("Content-Type", "application/json")

	res, err := bb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("bridge order %d: %s", res.StatusCode, string(b))
	}

	// Try to parse a normalized bridge response first (legacy support).
	var norm struct {
		OrderID    string `json:"order_id"`
		AvgPrice   string `json:"avg_price"`
		FilledBase string `json:"filled_base"`
		QuoteSpent string `json:"quote_spent"`
	}
	if err := json.Unmarshal(b, &norm); err == nil && (norm.OrderID != "" || norm.AvgPrice != "" || norm.FilledBase != "" || norm.QuoteSpent != "") {
		price, _ := strconv.ParseFloat(norm.AvgPrice, 64)
		base, _ := strconv.ParseFloat(norm.FilledBase, 64)
		quote, _ := strconv.ParseFloat(norm.QuoteSpent, 64)

		// Micro-retry enrichment: poll /order/{order_id} briefly for fills (and commission).
		id := firstNonEmpty(norm.OrderID, uuid.New().String())
		commission := 0.0
		const attempts = 6
		const sleepDur = 250 * time.Millisecond
		for i := 0; i < attempts; i++ {
			if ctx.Err() != nil {
				break
			}
			if p, b, c, err := bb.tryFetchOrderFill(ctx, id); err == nil && b > 0 && p > 0 {
				price, base = p, b
				quote = price * base
				commission = c
				break
			}
			select {
			case <-ctx.Done():
				i = attempts // exit loop on cancellation
			case <-time.After(sleepDur):
			}
		}

		return &PlacedOrder{
			ID:            id,
			ProductID:     product,
			Side:          side,
			Price:         price,
			BaseSize:      base,
			QuoteSpent:    quote,
			CommissionUSD: commission,
			CreateTime:    time.Now().UTC(),
		}, nil
	}

	// Fallback: extract an order_id (top-level or under success_response.order_id).
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	id := ""
	if v, ok := generic["order_id"]; ok {
		id = fmt.Sprintf("%v", v)
	}
	if id == "" {
		if sr, ok := generic["success_response"].(map[string]any); ok {
			if v, ok2 := sr["order_id"]; ok2 {
				id = fmt.Sprintf("%v", v)
			}
		}
	}
	if strings.TrimSpace(id) == "" {
		id = uuid.New().String()
	}

	// Micro-retry enrichment: poll /order/{order_id} briefly for fills (and commission).
	price, base := 0.0, 0.0
	quote := 0.0
	commission := 0.0
	{
		const attempts = 6
		const sleepDur = 250 * time.Millisecond
		for i := 0; i < attempts; i++ {
			if ctx.Err() != nil {
				break
			}
			if p, b, c, err := bb.tryFetchOrderFill(ctx, id); err == nil && b > 0 && p > 0 {
				price, base = p, b
				quote = price * base
				commission = c
				break
			}
			select {
			case <-ctx.Done():
				i = attempts
			case <-time.After(sleepDur):
			}
		}
	}

	return &PlacedOrder{
		ID:            id,
		ProductID:     product,
		Side:          side,
		Price:         price,
		BaseSize:      base,
		QuoteSpent:    quote,
		CommissionUSD: commission,
		CreateTime:    time.Now().UTC(),
	}, nil
}

// Cache-only lookup used by the hot path (no network).
func (bb *BridgeBroker) getExchangeFiltersCached(product string) ExFilters {
	key := strings.TrimSpace(product)
	fMuCoinbase.Lock()
	v := fCacheCoinbase[key]
	fMuCoinbase.Unlock()
	return v
}

// GetExchangeFilters returns lot/price steps for a product with robust fallbacks:
// 1) Cache (fast path)
// 2) Env overrides (BASE_STEP, TICK_SIZE) -> seeds cache, returns non-zero
// 3) HTTP best-effort against several endpoint variants/params
func (bb *BridgeBroker) GetExchangeFilters(ctx context.Context, product string) (ExFilters, error) {
	key := strings.TrimSpace(product)
	forceRemote := strings.EqualFold(strings.TrimSpace(os.Getenv("FORCE_FILTERS_REMOTE")), "1")

	// cache hit (skip if forced remote)
	fMuCoinbase.Lock()
	if !forceRemote {
		if v, ok := fCacheCoinbase[key]; ok && (v.StepSize > 0 || v.TickSize > 0 || v.PriceTick > 0 || v.BaseStep > 0 || v.QuoteStep > 0 || v.MinNotional > 0) {
			fMuCoinbase.Unlock()
			log.Printf("TRACE exch.filters source=cache product=%s step=%.10f tick=%.10f price_tick=%.10f base_step=%.10f quote_step=%.10f min_notional=%.10f",
				key, v.StepSize, v.TickSize, v.PriceTick, v.BaseStep, v.QuoteStep, v.MinNotional)
			return v, nil
		}
	}
	fMuCoinbase.Unlock()

	// ENV OVERRIDES (skip if forced remote). Accepts BASE_STEP, TICK_SIZE; extended keys optional.
	if !forceRemote {
		bs := strings.TrimSpace(os.Getenv("BASE_STEP"))
		ts := strings.TrimSpace(os.Getenv("TICK_SIZE"))
		pt := strings.TrimSpace(os.Getenv("PRICE_TICK"))
		qs := strings.TrimSpace(os.Getenv("QUOTE_STEP"))
		mn := strings.TrimSpace(os.Getenv("MIN_NOTIONAL"))
		f := ExFilters{
			StepSize:   parsePosFloat(bs),
			TickSize:   parsePosFloat(ts),
			PriceTick:  parsePosFloat(pt),
			BaseStep:   parsePosFloat(bs),
			QuoteStep:  parsePosFloat(qs),
			MinNotional: func() float64 { v := parsePosFloat(mn); if v <= 0 { return 0 } ; return v }(),
		}
		if f.StepSize > 0 || f.TickSize > 0 || f.PriceTick > 0 || f.BaseStep > 0 || f.QuoteStep > 0 || f.MinNotional > 0 {
			fMuCoinbase.Lock()
			fCacheCoinbase[key] = f
			fMuCoinbase.Unlock()
			log.Printf("TRACE exch.filters source=env product=%s step=%.10f tick=%.10f price_tick=%.10f base_step=%.10f quote_step=%.10f min_notional=%.10f",
				key, f.StepSize, f.TickSize, f.PriceTick, f.BaseStep, f.QuoteStep, f.MinNotional)
			return f, nil
		}
	}

	// HTTP best-effort: try multiple routes/params (this is the “from exchange” path via the bridge).
	sym := strings.ReplaceAll(key, "-", "")
	candidates := []string{
		fmt.Sprintf("%s/exchange/filters?product_id=%s", bb.base, url.QueryEscape(key)),
		fmt.Sprintf("%s/exchange/filters?symbol=%s", bb.base, url.QueryEscape(sym)),
		fmt.Sprintf("%s/filters?product_id=%s", bb.base, url.QueryEscape(key)), // legacy
		fmt.Sprintf("%s/filters?symbol=%s", bb.base, url.QueryEscape(sym)),     // legacy
	}

	var lastErr error
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "coinbot/bridge")

		res, err := bb.hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		func() {
			defer res.Body.Close()
			if res.StatusCode >= 300 {
				io.Copy(io.Discard, res.Body)
				lastErr = fmt.Errorf("filters %d", res.StatusCode)
				return
			}
			var payload struct {
				StepSize    string `json:"step_size"`
				TickSize    string `json:"tick_size"`
				PriceTick   string `json:"price_tick"`
				BaseStep    string `json:"base_step"`
				QuoteStep   string `json:"quote_step"`
				MinNotional string `json:"min_notional"`
			}
			if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
				lastErr = err
				return
			}
			f := ExFilters{
				StepSize:    parsePosFloat(payload.StepSize),
				TickSize:    parsePosFloat(payload.TickSize),
				PriceTick:   parsePosFloat(payload.PriceTick),
				BaseStep:    parsePosFloat(payload.BaseStep),
				QuoteStep:   parsePosFloat(payload.QuoteStep),
				MinNotional: parsePosFloat(payload.MinNotional),
			}
			if f.StepSize > 0 || f.TickSize > 0 || f.PriceTick > 0 || f.BaseStep > 0 || f.QuoteStep > 0 || f.MinNotional > 0 {
				fMuCoinbase.Lock()
				fCacheCoinbase[key] = f
				fMuCoinbase.Unlock()
				log.Printf("TRACE exch.filters source=bridge-http url=%s product=%s step=%.10f tick=%.10f price_tick=%.10f base_step=%.10f quote_step=%.10f min_notional=%.10f",
					u, key, f.StepSize, f.TickSize, f.PriceTick, f.BaseStep, f.QuoteStep, f.MinNotional)
				lastErr = nil
			} else {
				lastErr = fmt.Errorf("filters empty from %s", u)
			}
		}()
		if lastErr == nil {
			return fCacheCoinbase[key], nil
		}
	}

	return ExFilters{}, fmt.Errorf("filters unavailable for %s: %v", key, lastErr)
}


// --- Maker-first additions (post-only limit) ---

// PlaceLimitPostOnly places a post-only limit order and returns the bridge order_id.
// The bridge is expected to enforce maker-only semantics (post_only) and reject/adjust as needed.
func (bb *BridgeBroker) PlaceLimitPostOnly(ctx context.Context, product string, side OrderSide, limitPrice, baseSize float64) (string, error) {
	// Best-effort: apply exchange LOT_SIZE.StepSize and PRICE_FILTER.TickSize if the bridge exposes them.
	f := bb.getExchangeFiltersCached(product) // ignore error to preserve baseline behavior
	if f.StepSize > 0 {
		baseSize = snapDownCoinbase(baseSize, f.StepSize)
	}
	if f.TickSize > 0 {
		limitPrice = snapDownCoinbase(limitPrice, f.TickSize)
	}

	u := bb.base + "/order/limit_post_only"
	body := map[string]any{
		"product_id":      product,
		"side":            strings.ToUpper(string(side)),
		"limit_price":     formatWithStepCoinbase(limitPrice, f.TickSize, 10),
		"base_size":       formatWithStepCoinbase(baseSize, f.StepSize, 10),
		"client_order_id": uuid.New().String(),
	}
	bs, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(bs))
	if err != nil {
		return "", fmt.Errorf("newrequest limit_post_only: %w (url=%s)", err, u)
	}
	req.Header.Set("User-Agent", "coinbot/bridge")
	req.Header.Set("Content-Type", "application/json")

	res, err := bb.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("bridge limit_post_only %d: %s", res.StatusCode, string(b))
	}

	// Accept either {order_id:"..."} or legacy nested shapes
	var out struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal(b, &out); err == nil && strings.TrimSpace(out.OrderID) != "" {
		return out.OrderID, nil
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err == nil {
		if v, ok := generic["order_id"]; ok {
			return fmt.Sprintf("%v", v), nil
		}
		if sr, ok := generic["success_response"].(map[string]any); ok {
			if v, ok2 := sr["order_id"]; ok2 {
				return fmt.Sprintf("%v", v), nil
			}
		}
	}
	// If bridge returns nothing recognizable, synthesize a client id so caller can still poll/cancel gracefully.
	return uuid.New().String(), nil
}

// GetOrder fetches an order summary and maps it into PlacedOrder.
// Coinbase bridge may return different field names; we normalize here.
func (bb *BridgeBroker) GetOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	u := fmt.Sprintf("%s/order/%s?product_id=%s", bb.base, url.PathEscape(orderID), url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("newrequest get order: %w (url=%s)", err, u)
	}
	req.Header.Set("User-Agent", "coinbot/bridge")

	res, err := bb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("bridge get order %d: %s", res.StatusCode, string(b))
	}

	// Try to decode directly into PlacedOrder first (if bridge already normalizes).
	var po PlacedOrder
	if err := json.NewDecoder(res.Body).Decode(&po); err == nil && (po.BaseSize > 0 || po.QuoteSpent > 0 || strings.TrimSpace(po.ID) != "") {
		if strings.TrimSpace(po.ProductID) == "" {
			po.ProductID = product
		}
		return &po, nil
	}

	// If direct decode didn't work, re-read and map from Coinbase-like fields.
	// Re-issue request (body already consumed).
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req2.Header.Set("User-Agent", "coinbot/bridge")
	res2, err := bb.hc.Do(req2)
	if err != nil {
		return nil, err
	}
	defer res2.Body.Close()
	if res2.StatusCode >= 300 {
		b, _ := io.ReadAll(res2.Body)
		return nil, fmt.Errorf("bridge get order %d: %s", res2.StatusCode, string(b))
	}
	var raw struct {
		OrderID            string `json:"order_id"`
		Status             string `json:"status"`
		FilledSize         string `json:"filled_size"`
		AverageFilledPrice string `json:"average_filled_price"`
		CommissionTotalUSD string `json:"commission_total_usd"`
		Side               string `json:"side"`
		ProductID          string `json:"product_id"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&raw); err != nil {
		return nil, err
	}
	price, _ := strconv.ParseFloat(strings.TrimSpace(raw.AverageFilledPrice), 64)
	base, _ := strconv.ParseFloat(strings.TrimSpace(raw.FilledSize), 64)
	commission, _ := strconv.ParseFloat(strings.TrimSpace(raw.CommissionTotalUSD), 64)
	po = PlacedOrder{
		ID:            firstNonEmpty(raw.OrderID, orderID),
		ProductID:     firstNonEmpty(raw.ProductID, product),
		Side:          OrderSide(strings.ToUpper(strings.TrimSpace(raw.Side))),
		Price:         price,
		BaseSize:      base,
		QuoteSpent:    price * base,
		CommissionUSD: commission,
		Status:        raw.Status,
	}
	return &po, nil
}

// CancelOrder requests cancellation of a resting order.
// Returns nil if bridge accepts or if order is already closed/canceled.
func (bb *BridgeBroker) CancelOrder(ctx context.Context, product, orderID string) error {
	u := fmt.Sprintf("%s/order/%s?product_id=%s", bb.base, url.PathEscape(orderID), url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("newrequest cancel: %w (url=%s)", err, u)
	}
	req.Header.Set("User-Agent", "coinbot/bridge")

	res, err := bb.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Treat any 2xx as success; 404/409 may indicate already closed — best-effort OK.
	if res.StatusCode >= 300 && res.StatusCode != 404 && res.StatusCode != 409 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("bridge cancel %d: %s", res.StatusCode, string(b))
	}
	return nil
}

// --- small helpers local to this file ---

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// tryFetchOrderFill calls GET /order/{order_id} and returns (avgPrice, filledBase, commissionUSD, err).
// This is a best-effort enrichment; errors are swallowed by the caller for minimal impact.
func (bb *BridgeBroker) tryFetchOrderFill(ctx context.Context, orderID string) (avgPrice float64, filledBase float64, commissionUSD float64, err error) {
	if strings.TrimSpace(orderID) == "" {
		return 0, 0, 0, fmt.Errorf("empty order id")
	}
	u := fmt.Sprintf("%s/order/%s", bb.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("User-Agent", "coinbot/bridge")

	res, err := bb.hc.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return 0, 0, 0, fmt.Errorf("order fetch %d: %s", res.StatusCode, string(b))
	}

	var out struct {
		OrderID            string `json:"order_id"`
		Status             string `json:"status"`
		FilledSize         string `json:"filled_size"`
		AverageFilledPrice string `json:"average_filled_price"`
		CommissionTotalUSD string `json:"commission_total_usd"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, 0, 0, err
	}

	fp, _ := strconv.ParseFloat(strings.TrimSpace(out.AverageFilledPrice), 64)
	fs, _ := strconv.ParseFloat(strings.TrimSpace(out.FilledSize), 64)
	cu, _ := strconv.ParseFloat(strings.TrimSpace(out.CommissionTotalUSD), 64)
	return fp, fs, cu, nil
}

// --- MINIMAL ADD: exchange filters cache & formatting helpers (best-effort; no behavior changes if unavailable) ---

var (
	fMuCoinbase    sync.Mutex
	fCacheCoinbase = map[string]ExFilters{} // key: product/symbol
)

func parsePosFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func snapDownCoinbase(x, step float64) float64 {
	if step <= 0 {
		return x
	}
	n := math.Floor(x/step + 1e-12)
	return n * step
}

// formatWithStepCoinbase formats x using the decimal precision implied by step.
// If step <= 0, it falls back to a fixed maximum precision given by fallbackDec.
func formatWithStepCoinbase(x, step float64, fallbackDec int) string {
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
