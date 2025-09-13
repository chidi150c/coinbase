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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
		log.Fatalf("GetAvailableBase: failed calling %s/balance/base?product_id=%s", bb.base, product)
		// unreachable after Fatalf, but return to satisfy compiler
		return "", 0, 0, fmt.Errorf("fatal: GetAvailableBase failed")
	}
	return asset, avail, step, nil
}

func (bb *BridgeBroker) GetAvailableQuote(ctx context.Context, product string) (string, float64, float64, error) {
	asset, avail, step, ok := bb.tryBalanceEndpoint(ctx, "/balance/quote", product)
	if !ok {
		log.Fatalf("GetAvailableQuote: failed calling %s/balance/quote?product_id=%s", bb.base, product)
		// unreachable after Fatalf, but return to satisfy compiler
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

// --- Orders ---

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
