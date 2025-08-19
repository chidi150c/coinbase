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
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

	// Expected: array of rows with string/float fields
	type row struct {
		Start  any `json:"start"`
		Open   any `json:"open"`
		High   any `json:"high"`
		Low    any `json:"low"`
		Close  any `json:"close"`
		Volume any `json:"volume"`
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
			f, _ := strconv.ParseFloat(t, 64)
			return f
		default:
			return math.NaN()
		}
	}
	parseT := func(v any) time.Time {
		switch t := v.(type) {
		case string:
			if tt, err := time.Parse(time.RFC3339, t); err == nil {
				return tt
			}
			if sec, err := strconv.ParseInt(t, 10, 64); err == nil {
				return time.Unix(sec, 0).UTC()
			}
		case float64:
			return time.Unix(int64(t), 0).UTC()
		}
		return time.Time{}
	}

	out := make([]Candle, 0, len(rows))
	for _, r := range rows {
		out = append(out, Candle{
			Time:   parseT(r.Start),
			Open:   parseF(r.Open),
			High:   parseF(r.High),
			Low:    parseF(r.Low),
			Close:  parseF(r.Close),
			Volume: parseF(r.Volume),
		})
	}
	// Ensure ascending time
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// --- Orders ---

func (bb *BridgeBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
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
		return nil, fmt.Errorf("order %d: %s", res.StatusCode, string(b))
	}

	// Try normalized sidecar shape first
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

	// Fallback: flexible parsing of SDK response
	var m map[string]any
	_ = json.Unmarshal(b, &m)

	readStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch t := v.(type) {
				case string:
					if strings.TrimSpace(t) != "" {
						return t
					}
				case float64:
					return strconv.FormatFloat(t, 'f', -1, 64)
				case map[string]any:
					if vv, ok := t["value"]; ok {
						if s, ok := vv.(string); ok && s != "" {
							return s
						}
					}
				}
			}
		}
		return ""
	}

	orderID := readStr("order_id", "orderId", "id")
	priceStr := readStr("avg_price", "average_price", "price", "executed_price", "filled_average_price")
	baseStr := readStr("filled_base", "filled_size", "filled_base_size", "size", "executed_size")
	quoteStr := readStr("quote_spent", "filled_value", "quote_size", "cost", "notional")

	price, _ := strconv.ParseFloat(priceStr, 64)
	var base, quote float64
	if q, err := strconv.ParseFloat(quoteStr, 64); err == nil {
		quote = q
	}
	if bsz, err := strconv.ParseFloat(baseStr, 64); err == nil {
		base = bsz
	}

	// Best-effort derivations
	if price == 0 {
		if p, err := bb.GetNowPrice(ctx, product); err == nil {
			price = p
		}
	}
	if base == 0 && price > 0 && quote > 0 {
		base = quote / price
	}
	if quote == 0 && price > 0 && base > 0 {
		quote = base * price
	}

	return &PlacedOrder{
		ID:         firstNonEmpty(orderID, uuid.New().String()),
		ProductID:  product,
		Side:       side,
		Price:      price,
		BaseSize:   base,
		QuoteSpent: quote,
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
