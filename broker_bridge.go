// FILE: broker_bridge.go
// Package main – HTTP broker that talks to the local FastAPI sidecar.
//
// This broker hits your Python sidecar (app.py) which fronts Coinbase Advanced
// Trade via the official `coinbase.rest.RESTClient`. It implements:
//   • GetNowPrice:      GET /product/{product_id} -> price
//   • GetRecentCandles: GET /candles?product_id=...&granularity=...&limit=...
//   • PlaceMarketQuote: POST /order/market {product_id, side, quote_size}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// --- Candles ---

func (bb *BridgeBroker) GetRecentCandles(ctx context.Context, product, granularity string, limit int) ([]Candle, error) {
	q := url.Values{}
	q.Set("product_id", product)
	q.Set("granularity", granularity)
	if limit <= 0 {
		limit = 300
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
		Time   string      `json:"time"`
		Open   any         `json:"open"`
		High   any         `json:"high"`
		Low    any         `json:"low"`
		Close  any         `json:"close"`
		Volume any         `json:"volume"`
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
		"product_id": product,
		"side":       strings.ToUpper(string(side)),
		"quote_size": fmt.Sprintf("%.2f", quoteUSD),
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

	// Try to parse a normalized bridge response first.
	var norm struct {
		OrderID     string `json:"order_id"`
		AvgPrice    string `json:"avg_price"`
		FilledBase  string `json:"filled_base"`
		QuoteSpent  string `json:"quote_spent"`
	}
	if err := json.Unmarshal(b, &norm); err == nil && (norm.OrderID != "" || norm.AvgPrice != "" || norm.FilledBase != "" || norm.QuoteSpent != "") {
		price, _ := strconv.ParseFloat(norm.AvgPrice, 64)
		base, _ := strconv.ParseFloat(norm.FilledBase, 64)
		quote, _ := strconv.ParseFloat(norm.QuoteSpent, 64)
		return &PlacedOrder{
			ID:         firstNonEmpty(norm.OrderID, uuid.New().String()),
			ProductID:  product,
			Side:       side,
			Price:      price,
			BaseSize:   base,
			QuoteSpent: quote,
			CreateTime: time.Now().UTC(),
		}, nil
	}

	// Fallback: just extract an order_id if present; leave price/base/quote as zeroes.
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	id := ""
	if v, ok := generic["order_id"]; ok {
		id = fmt.Sprintf("%v", v)
	}
	if strings.TrimSpace(id) == "" {
		id = uuid.New().String()
	}

	return &PlacedOrder{
		ID:         id,
		ProductID:  product,
		Side:       side,
		Price:      0,
		BaseSize:   0,
		QuoteSpent: 0,
		CreateTime: time.Now().UTC(),
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
