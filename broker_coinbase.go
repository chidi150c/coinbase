// FILE: broker_coinbase.go
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/golang-jwt/jwt/v5"
)

type CoinbaseBroker struct {
	apiBase        string // default https://api.coinbase.com
	hc             *http.Client
	// Auth modes:
	//  - If bearerToken set, use it
	//  - Else if keyName + privateKeyPEM set, mint per-request JWT
	keyName       string
	privateKeyPEM string
	bearerToken   string
}

func NewCoinbaseBroker() *CoinbaseBroker {
	return &CoinbaseBroker{
		apiBase: strings.TrimRight(getEnv("COINBASE_API_BASE", "https://api.coinbase.com"), "/"),
		hc:      &http.Client{Timeout: 15 * time.Second},
		keyName: strings.TrimSpace(getEnv("COINBASE_API_KEY_NAME", "")),
		privateKeyPEM: normalizeMultiline(getEnv("COINBASE_API_PRIVATE_KEY", getEnv("COINBASE_API_SECRET", ""))),
		bearerToken:   strings.TrimSpace(getEnv("COINBASE_BEARER_TOKEN", "")),
	}
}

func (cb *CoinbaseBroker) Name() string { return "coinbase" }

// ---------- Price ----------

func (cb *CoinbaseBroker) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/api/v3/brokerage/products/%s", cb.apiBase, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "coinbot/coinbase-go")
	cb.addAuthIfAvailable(req) // product is often public, but allow auth too

	res, err := cb.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return 0, fmt.Errorf("product %d: %s", res.StatusCode, string(b))
	}
	var j map[string]any
	if err := json.NewDecoder(res.Body).Decode(&j); err != nil {
		return 0, err
	}
	// Try common numeric fields
	for _, k := range []string{"price", "mid_market_price", "best_ask", "best_bid"} {
		if v, ok := j[k]; ok {
			switch t := v.(type) {
			case string:
				f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
				if f > 0 {
					return f, nil
				}
			case float64:
				if t > 0 {
					return t, nil
				}
			}
		}
	}
	return 0, errors.New("no usable price in product payload")
}

// ---------- Candles ----------

func (cb *CoinbaseBroker) GetRecentCandles(ctx context.Context, product, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 350
	}
	if limit > 350 {
		limit = 350
	}
	sec := granularitySeconds(granularity)
	if sec <= 0 {
		return nil, fmt.Errorf("unsupported granularity: %s", granularity)
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration((limit+2)*sec) * time.Second)

	qs := url.Values{
		"granularity": []string{strings.ToUpper(granularity)},
		"start":       []string{strconv.FormatInt(start.Unix(), 10)},
		"end":         []string{strconv.FormatInt(end.Unix(), 10)},
		"limit":       []string{strconv.Itoa(limit)},
	}
	u := fmt.Sprintf("%s/api/v3/brokerage/products/%s/candles?%s",
		cb.apiBase, url.PathEscape(product), qs.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "coinbot/coinbase-go")
	cb.addAuthIfAvailable(req) // some deployments require auth for candles

	res, err := cb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("candles %d: %s", res.StatusCode, string(b))
	}

	var raw any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}
	rows := normalizeCandlesCBS(raw)

	out := make([]Candle, 0, len(rows))
	for _, r := range rows {
		ts, _ := strconv.ParseInt(strings.TrimSpace(r.Start), 10, 64)
		if ts <= 0 {
			continue
		}
		o, _ := strconv.ParseFloat(r.Open, 64)
		h, _ := strconv.ParseFloat(r.High, 64)
		l, _ := strconv.ParseFloat(r.Low, 64)
		c, _ := strconv.ParseFloat(r.Close, 64)
		v, _ := strconv.ParseFloat(r.Volume, 64)
		out = append(out, Candle{
			Time:   time.Unix(ts, 0).UTC(),
			Open:   o, High: h, Low: l, Close: c, Volume: v,
		})
	}
	// ensure ascending
	for i := 1; i < len(out); i++ {
		if out[i].Time.Before(out[i-1].Time) {
			for j := i; j > 0 && out[j].Time.Before(out[j-1].Time); j-- {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
	}
	return out, nil
}

type candleRow struct {
	Start  string `json:"start"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}

func normalizeCandlesCBS(raw any) []candleRow {
	switch v := raw.(type) {
	case []any:
		return toCandleRows(v)
	case map[string]any:
		if c, ok := v["candles"]; ok {
			if arr, ok := c.([]any); ok {
				return toCandleRows(arr)
			}
		}
	}
	return nil
}
func toCandleRows(arr []any) []candleRow {
	out := make([]candleRow, 0, len(arr))
	for _, it := range arr {
		switch m := it.(type) {
		case map[string]any:
			out = append(out, candleRow{
				Start:  asStr(m["start"]),
				Open:   asStr(m["open"]),
				High:   asStr(m["high"]),
				Low:    asStr(m["low"]),
				Close:  asStr(m["close"]),
				Volume: asStr(m["volume"]),
			})
		case []any:
			if len(m) >= 6 {
				out = append(out, candleRow{
					Start:  asStr(m[0]),
					Open:   asStr(m[1]),
					High:   asStr(m[2]),
					Low:    asStr(m[3]),
					Close:  asStr(m[4]),
					Volume: asStr(m[5]),
				})
			}
		}
	}
	return out
}
func asStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ---------- Orders (market by quote) + enrichment ----------

func (cb *CoinbaseBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	if quoteUSD <= 0 {
		return nil, fmt.Errorf("invalid quote USD: %.2f", quoteUSD)
	}
	body := map[string]any{
		"client_order_id": uuid.New().String(),
		"product_id":      product,
		"side":            strings.ToUpper(string(side)),
		"order_configuration": map[string]any{
			"market_market_ioc": map[string]string{
				"quote_size": fmt.Sprintf("%.2f", quoteUSD),
			},
		},
	}
	u := cb.apiBase + "/api/v3/brokerage/orders"
	bs, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(bs))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "coinbot/coinbase-go")
	req.Header.Set("Content-Type", "application/json")
	if err := cb.addAuth(req); err != nil {
		return nil, err
	}

	res, err := cb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("order %d: %s", res.StatusCode, string(rb))
	}

	// Parse flexible response shapes
	var generic map[string]any
	_ = json.Unmarshal(rb, &generic)

	orderID := firstString(
		generic["order_id"],
		// At times wrapped like {"success_response":{"order_id":"..."}, ...}
		nested(generic, "success_response", "order_id"),
	)
	if strings.TrimSpace(orderID) == "" {
		// Fallback to client id if nothing else
		orderID = body["client_order_id"].(string)
	}

	// Micro-retry fills enrichment (avg price, base size, commission USD)
	price, base, commission := 0.0, 0.0, 0.0
	{
		const attempts = 6
		const sleep = 250 * time.Millisecond
		for i := 0; i < attempts; i++ {
			if ctx.Err() != nil {
				break
			}
			if p, b, c, err := cb.fetchOrderFill(ctx, orderID); err == nil && b > 0 && p > 0 {
				price, base, commission = p, b, c
				break
			}
			select {
			case <-ctx.Done():
				i = attempts
			case <-time.After(sleep):
			}
		}
	}

	quote := price * base
	return &PlacedOrder{
		ID:            orderID,
		ProductID:     product,
		Side:          side,
		Price:         price,
		BaseSize:      base,
		QuoteSpent:    quote,
		CommissionUSD: commission,
		CreateTime:    time.Now().UTC(),
	}, nil
}

func (cb *CoinbaseBroker) fetchOrderFill(ctx context.Context, orderID string) (avgPrice, filledBase, commissionUSD float64, err error) {
	if strings.TrimSpace(orderID) == "" {
		return 0, 0, 0, fmt.Errorf("empty order id")
	}
	qs := url.Values{"order_id": []string{orderID}}
	u := fmt.Sprintf("%s/api/v3/brokerage/orders/historical/fills?%s", cb.apiBase, qs.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("User-Agent", "coinbot/coinbase-go")
	if err := cb.addAuth(req); err != nil {
		return 0, 0, 0, err
	}

	res, err := cb.hc.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return 0, 0, 0, fmt.Errorf("fills %d: %s", res.StatusCode, string(b))
	}
	var j map[string]any
	if err := json.NewDecoder(res.Body).Decode(&j); err != nil {
		return 0, 0, 0, err
	}
	// Common shapes: {"fills":[...]}, {"data":[...]}, {"results":[...]}
	arr := anyFirst(j["fills"], j["data"], j["results"])
	list, _ := arr.([]any)
	if len(list) == 0 {
		return 0, 0, 0, nil
	}

	var totBase, totNotional, totCommission float64
	for _, it := range list {
		m, _ := it.(map[string]any)
		priceF := parseFloat(firstString(m["price"], m["average_filled_price"]))
		// Coinbase sometimes sets size_in_quote; if so size is quote units
		sizeF := parseFloat(firstString(m["size"], m["filled_size"]))
		commissionF := parseFloat(m["commission"])
		sizeInQuote := false
		if sv, ok := m["size_in_quote"].(bool); ok {
			sizeInQuote = sv
		}
		var base, notional float64
		if sizeInQuote {
			if priceF > 0 {
				base = sizeF / priceF
			}
			notional = sizeF
		} else {
			base = sizeF
			notional = sizeF * priceF
		}
		totBase += base
		totNotional += notional
		totCommission += commissionF
	}
	var avg float64
	if totBase > 0 {
		avg = totNotional / totBase
	}
	return avg, totBase, totCommission, nil
}

// ---------- Balances / Steps ----------

func (cb *CoinbaseBroker) GetAvailableBase(ctx context.Context, product string) (string, float64, float64, error) {
	base, _, baseStep, _, err := cb.productSymbolsAndSteps(ctx, product)
	if err != nil {
		return "", 0, 0, err
	}
	avail, err := cb.sumAvailable(ctx, base)
	if err != nil {
		return "", 0, 0, err
	}
	return base, avail, baseStep, nil
}

func (cb *CoinbaseBroker) GetAvailableQuote(ctx context.Context, product string) (string, float64, float64, error) {
	_, quote, _, quoteStep, err := cb.productSymbolsAndSteps(ctx, product)
	if err != nil {
		return "", 0, 0, err
	}
	avail, err := cb.sumAvailable(ctx, quote)
	if err != nil {
		return "", 0, 0, err
	}
	return quote, avail, quoteStep, nil
}

func (cb *CoinbaseBroker) productSymbolsAndSteps(ctx context.Context, product string) (baseSym string, quoteSym string, baseStep float64, quoteStep float64, err error) {
	u := fmt.Sprintf("%s/api/v3/brokerage/products/%s", cb.apiBase, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", 0, 0, err
	}
	req.Header.Set("User-Agent", "coinbot/coinbase-go")
	cb.addAuthIfAvailable(req)

	res, err := cb.hc.Do(req)
	if err != nil {
		return "", "", 0, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return "", "", 0, 0, fmt.Errorf("product %d: %s", res.StatusCode, string(b))
	}
	var p map[string]any
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		return "", "", 0, 0, err
	}
	baseSym = strings.ToUpper(firstString(p["base_currency"], p["base_currency_id"], p["base"], p["base_display_symbol"]))
	quoteSym = strings.ToUpper(firstString(p["quote_currency"], p["quote_currency_id"], p["quote"], p["quote_display_symbol"]))
	baseStep = parseFloat(firstString(p["base_increment"], p["base_increment_value"]))
	quoteStep = parseFloat(firstString(p["quote_increment"], p["quote_increment_value"]))
	if baseSym == "" || quoteSym == "" {
		// derive from product as last resort
		b, q := splitProductID(product)
		if baseSym == "" {
			baseSym = b
		}
		if quoteSym == "" {
			quoteSym = q
		}
	}
	if baseStep <= 0 {
		baseStep = getEnvFloat("BASE_STEP", 0.0)
	}
	if quoteStep <= 0 {
		quoteStep = getEnvFloat("QUOTE_STEP", 0.0)
	}
	return
}

func (cb *CoinbaseBroker) sumAvailable(ctx context.Context, currency string) (float64, error) {
	if strings.TrimSpace(currency) == "" {
		return 0, fmt.Errorf("empty currency")
	}
	u := fmt.Sprintf("%s/api/v3/brokerage/accounts?limit=200", cb.apiBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "coinbot/coinbase-go")
	if err := cb.addAuth(req); err != nil {
		return 0, err
	}

	res, err := cb.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return 0, fmt.Errorf("accounts %d: %s", res.StatusCode, string(b))
	}

	var j map[string]any
	if err := json.NewDecoder(res.Body).Decode(&j); err != nil {
		return 0, err
	}
	accs := anyFirst(j["accounts"], j["data"]).([]any)
	total := 0.0
	for _, a := range accs {
		m, _ := a.(map[string]any)
		ab, _ := m["available_balance"].(map[string]any)
		if ab == nil {
			continue
		}
		cur := strings.ToUpper(firstString(ab["currency"]))
		if cur != strings.ToUpper(currency) {
			continue
		}
		v := parseFloat(ab["value"])
		total += v
	}
	return total, nil
}

// ---------- auth helpers ----------

func (cb *CoinbaseBroker) addAuthIfAvailable(req *http.Request) {
	if cb.bearerToken != "" || (cb.keyName != "" && cb.privateKeyPEM != "") {
		_ = cb.addAuth(req)
	}
}

func (cb *CoinbaseBroker) addAuth(req *http.Request) error {
	// Prefer fixed bearer if provided (useful if you supply externally-minted tokens)
	if cb.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cb.bearerToken)
		return nil
	}
	// Else mint short-lived JWT using key name + RSA private key (RS256).
	if cb.keyName == "" || cb.privateKeyPEM == "" {
		return errors.New("coinbase auth not configured (set COINBASE_BEARER_TOKEN or COINBASE_API_KEY_NAME + COINBASE_API_PRIVATE_KEY)")
	}
	token, err := mintCoinbaseJWT(cb.keyName, cb.privateKeyPEM, 25*time.Second)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Many clients include the key name as an extra header:
	req.Header.Set("CB-ACCESS-KEY", cb.keyName)
	return nil
}

func mintCoinbaseJWT(keyName, privatePEM string, ttl time.Duration) (string, error) {
	block, _ := pem.Decode([]byte(privatePEM))
	if block == nil {
		return "", errors.New("invalid private key (no PEM block)")
	}
	var priv *rsa.PrivateKey
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", err
		}
		var ok bool
		priv, ok = k.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("not RSA private key")
		}
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return "", err
		}
		priv = k
	default:
		return "", fmt.Errorf("unsupported key type: %s", block.Type)
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub": keyName,          // API key name
		"aud": "retail_rest_api",// audience per Advanced Trade docs
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
		"nbf": now.Add(-5 * time.Second).Unix(),
		"jti": uuid.New().String(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return t.SignedString(priv)
}

// ---------- small utils ----------

func firstString(vals ...any) string {
	for _, v := range vals {
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case fmt.Stringer:
			s := strings.TrimSpace(t.String())
			if s != "" {
				return s
			}
		}
	}
	return ""
}
func anyFirst(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}
func parseFloat(v any) float64 {
	switch t := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}
func normalizeMultiline(s string) string {
	if strings.Contains(s, `\n`) {
		return strings.ReplaceAll(s, `\n`, "\n")
	}
	return s
}
