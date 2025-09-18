// FILE: broker_hitbtc.go
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HitBTCBroker implements Broker for HitBTC spot REST v3.
// Auth: Basic apiKey:secretKey (or HS256 — we use Basic for simplicity).
// Symbols: e.g. BTCUSDT (no dash). If product contains a dash, we convert
// "BTC-USD" -> "BTCUSDT" (USD≈USDT). Otherwise we use product verbatim.
type HitBTCBroker struct {
	client    *http.Client
	baseURL   string
	apiKey    string
	apiSecret string

	// lightweight cache of symbol meta (base/quote & increments)
	metaCache map[string]hitbtcSymbolMeta
}

type hitbtcSymbolMeta struct {
	Symbol       string
	Base         string
	Quote        string
	QtyIncrement float64 // base quantity step
	TickSize     float64 // price tick size
}

// ---- construction ----

func NewHitBTCBrokerFromEnv() (Broker, error) {
	apiKey := os.Getenv("HITBTC_API_KEY")
	apiSecret := os.Getenv("HITBTC_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		return nil, errors.New("HITBTC_API_KEY and HITBTC_API_SECRET must be set")
	}
	base := os.Getenv("HITBTC_API_BASE")
	if base == "" {
		base = "https://api.hitbtc.com/api/3"
	}
	if eqTrue(os.Getenv("HITBTC_USE_SANDBOX")) {
		base = "https://api.demo.hitbtc.com/api/3"
	}
	return &HitBTCBroker{
		client:    &http.Client{Timeout: 15 * time.Second},
		baseURL:   strings.TrimRight(base, "/"),
		apiKey:    apiKey,
		apiSecret: apiSecret,
		metaCache: make(map[string]hitbtcSymbolMeta),
	}, nil
}

func (b *HitBTCBroker) Name() string { return "hitbtc" }

// ---- interface methods ----

func (b *HitBTCBroker) GetNowPrice(ctx context.Context, product string) (float64, error) {
	symbol := hbNormalizeSymbol(product)

	// Primary: /public/price/ticker/{symbol}
	if _, data, err := b.doReq(ctx, http.MethodGet, "/public/price/ticker/"+symbol, nil, nil); err == nil {
		var obj struct {
			Price string `json:"price"`
		}
		if json.Unmarshal(data, &obj) == nil && obj.Price != "" {
			return hbParseDec(obj.Price)
		}
	}

	// Fallback: /public/ticker/{symbol}
	_, data, err := b.doReq(ctx, http.MethodGet, "/public/ticker/"+symbol, nil, nil)
	if err != nil {
		return 0, err
	}
	var t struct {
		Last string `json:"last"`
		Bid  string `json:"bid"`
		Ask  string `json:"ask"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return 0, fmt.Errorf("decode ticker: %w", err)
	}
	if t.Last != "" {
		return hbParseDec(t.Last)
	}
	bid, _ := hbParseDec(t.Bid)
	ask, _ := hbParseDec(t.Ask)
	if bid > 0 && ask > 0 {
		return (bid + ask) / 2, nil
	}
	return 0, errors.New("no price fields available")
}

func (b *HitBTCBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	if quoteUSD <= 0 {
		return nil, errors.New("quoteUSD must be > 0")
	}
	symbol := hbNormalizeSymbol(product)

	price, err := b.GetNowPrice(ctx, product)
	if err != nil {
		return nil, err
	}
	meta, err := b.resolveSymbolMeta(ctx, symbol)
	if err != nil {
		return nil, err
	}
	qtyStep := meta.QtyIncrement
	if qtyStep <= 0 {
		qtyStep = 1e-8 // sensible default if exchange does not return it
	}
	qty := quoteUSD / price
	qty = hbFloorStep(qty, qtyStep)
	if qty <= 0 {
		return nil, fmt.Errorf("computed qty <= 0 after step rounding (step=%g)", qtyStep)
	}

	form := url.Values{}
	form.Set("symbol", symbol)
	form.Set("side", strings.ToLower(string(side))) // "buy" | "sell"
	form.Set("type", "market")
	form.Set("quantity", hbTrimDec(qty))

	_, data, err := b.doReq(ctx, http.MethodPost, "/spot/order", strings.NewReader(form.Encode()), nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		ID                 json.Number `json:"id"`
		ClientOrderID      string      `json:"client_order_id"`
		Symbol             string      `json:"symbol"`
		Side               string      `json:"side"`
		Status             string      `json:"status"`
		Type               string      `json:"type"`
		TimeInForce        string      `json:"time_in_force"`
		Quantity           string      `json:"quantity"`
		Price              string      `json:"price"`
		QuantityCumulative string      `json:"quantity_cumulative"`
		CreatedAt          string      `json:"created_at"`
		UpdatedAt          string      `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode order: %w", err)
	}

	filledQty, _ := hbParseDec(resp.QuantityCumulative)
	if filledQty <= 0 {
		// Market orders usually fill immediately; if not reported, assume sent qty.
		filledQty = qty
	}

	return &PlacedOrder{
		ID:            firstNonEmpty(resp.ClientOrderID, resp.ID.String()),
		ProductID:     product,          // keep caller's product id shape
		Side:          side,
		Price:         price,            // approximate avg fill
		BaseSize:      filledQty,        // filled base quantity
		QuoteSpent:    price * filledQty, // approx (fees excluded)
		CommissionUSD: 0,                // trading loop applies t.cfg.FeeRatePct
		CreateTime:    time.Now(),
	}, nil
}

func (b *HitBTCBroker) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 100
	}
	symbol := hbNormalizeSymbol(product)
	period := hbMapGranularity(granularity) // e.g., ONE_MINUTE -> M1
	q := url.Values{}
	q.Set("period", period)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("sort", "DESC")

	path := "/public/candles/" + symbol + "?" + q.Encode()
	_, data, err := b.doReq(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}

	var arr []struct {
		Timestamp    string `json:"timestamp"`
		Open         string `json:"open"`
		Close        string `json:"close"`
		Min          string `json:"min"`
		Max          string `json:"max"`
		Volume       string `json:"volume"`
		VolumeQuote  string `json:"volume_quote"`
	}
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("decode candles: %w", err)
	}

	candles := make([]Candle, 0, len(arr))
	for _, c := range arr {
		ts, err := time.Parse(time.RFC3339, c.Timestamp)
		if err != nil {
			continue
		}
		open, _ := hbParseDec(c.Open)
		high, _ := hbParseDec(c.Max)
		low, _ := hbParseDec(c.Min)
		closep, _ := hbParseDec(c.Close)
		vol, _ := hbParseDec(c.Volume)
		candles = append(candles, Candle{
			Time:   ts,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closep,
			Volume: vol,
		})
	}
	// Ensure chronological ascending, most bots expect ascending.
	sort.Slice(candles, func(i, j int) bool { return candles[i].Time.Before(candles[j].Time) })
	return candles, nil
}

func (b *HitBTCBroker) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	symbol := hbNormalizeSymbol(product)
	meta, err := b.resolveSymbolMeta(ctx, symbol)
	if err != nil {
		return "", 0, 0, err
	}
	bals, err := b.fetchSpotBalances(ctx)
	if err != nil {
		// still return asset & step if balances call fails
		return meta.Base, 0, meta.QtyIncrement, err
	}
	return meta.Base, bals[strings.ToUpper(meta.Base)], meta.QtyIncrement, nil
}

func (b *HitBTCBroker) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	symbol := hbNormalizeSymbol(product)
	meta, err := b.resolveSymbolMeta(ctx, symbol)
	if err != nil {
		return "", 0, 0, err
	}
	bals, err := b.fetchSpotBalances(ctx)
	if err != nil {
		// HitBTC does not expose a quote increment; use a sensible default
		return meta.Quote, 0, 0.01, err
	}
	return meta.Quote, bals[strings.ToUpper(meta.Quote)], 0.01, nil
}

// ---- optional helpers (not part of interface) ----

func (b *HitBTCBroker) CloseOrder(ctx context.Context, clientOrderID string) error {
	if clientOrderID == "" {
		return errors.New("clientOrderID required")
	}
	path := "/spot/order/" + url.PathEscape(clientOrderID)
	_, _, err := b.doReq(ctx, http.MethodDelete, path, nil, nil)
	return err
}

func (b *HitBTCBroker) CancelAll(ctx context.Context, product string) error {
	symbol := hbNormalizeSymbol(product)
	path := "/spot/order?symbol=" + url.QueryEscape(symbol)
	_, _, err := b.doReq(ctx, http.MethodDelete, path, nil, nil)
	return err
}

// ---- internal HTTP helpers ----

func (b *HitBTCBroker) doReq(ctx context.Context, method, path string, body io.Reader, hdr map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, body)
	if err != nil {
		return nil, nil, err
	}
	cred := base64.StdEncoding.EncodeToString([]byte(b.apiKey + ":" + b.apiSecret))
	req.Header.Set("Authorization", "Basic "+cred)
	req.Header.Set("Accept", "application/json")
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp, data, fmt.Errorf("hitbtc %s %s: %s", method, path, string(data))
	}
	return resp, data, nil
}

// ---- symbol & balances ----

func (b *HitBTCBroker) resolveSymbolMeta(ctx context.Context, symbol string) (hitbtcSymbolMeta, error) {
	if m, ok := b.metaCache[symbol]; ok && m.Symbol != "" {
		return m, nil
	}
	_, data, err := b.doReq(ctx, http.MethodGet, "/public/symbol/"+symbol, nil, nil)
	if err != nil {
		return hitbtcSymbolMeta{}, err
	}
	var s struct {
		Type              string `json:"type"`
		BaseCurrency      string `json:"base_currency"`
		QuoteCurrency     string `json:"quote_currency"`
		QuantityIncrement string `json:"quantity_increment"`
		TickSize          string `json:"tick_size"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return hitbtcSymbolMeta{}, fmt.Errorf("decode symbol meta: %w", err)
	}
	qtyInc, _ := strconv.ParseFloat(s.QuantityIncrement, 64)
	tick, _ := strconv.ParseFloat(s.TickSize, 64)
	meta := hitbtcSymbolMeta{
		Symbol:       symbol,
		Base:         strings.ToUpper(s.BaseCurrency),
		Quote:        strings.ToUpper(s.QuoteCurrency),
		QtyIncrement: qtyInc,
		TickSize:     tick,
	}
	b.metaCache[symbol] = meta
	return meta, nil
}

func (b *HitBTCBroker) fetchSpotBalances(ctx context.Context) (map[string]float64, error) {
	if b.apiKey == "" || b.apiSecret == "" {
		return nil, fmt.Errorf("HITBTC_API_KEY/SECRET not set")
	}
	_, data, err := b.doReq(ctx, http.MethodGet, "/spot/balance", nil, nil)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		Currency string `json:"currency"`
		Available string `json:"available"`
	}
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(arr))
	for _, it := range arr {
		v, _ := strconv.ParseFloat(it.Available, 64)
		out[strings.ToUpper(it.Currency)] = v
	}
	return out, nil
}

// ---- small utils (file-local names to avoid collisions) ----

func hbNormalizeSymbol(product string) string {
	p := strings.ToUpper(strings.TrimSpace(product))
	if strings.Contains(p, "-") {
		parts := strings.SplitN(p, "-", 2)
		base, quote := parts[0], parts[1]
		if quote == "USD" {
			quote = "USDT"
		}
		return base + quote
	}
	return p // already like BTCUSDT
}

func hbParseDec(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	return strconv.ParseFloat(s, 64)
}

func hbTrimDec(f float64) string {
	s := fmt.Sprintf("%.12f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return "0"
	}
	return s
}

func hbFloorStep(x, step float64) float64 {
	if step <= 0 {
		return x
	}
	return math.Floor(x/step) * step
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func hbMapGranularity(g string) string {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "ONE_MINUTE", "M1", "1M", "1MIN":
		return "M1"
	case "THREE_MINUTE", "M3", "3M":
		return "M3"
	case "FIVE_MINUTE", "M5", "5M":
		return "M5"
	case "FIFTEEN_MINUTE", "M15", "15M":
		return "M15"
	case "THIRTY_MINUTE", "M30", "30M":
		return "M30"
	case "ONE_HOUR", "H1", "1H":
		return "H1"
	case "FOUR_HOUR", "H4", "4H":
		return "H4"
	case "ONE_DAY", "D1", "1D", "DAY":
		return "D1"
	case "ONE_WEEK", "D7", "1W", "W1":
		return "D7"
	case "ONE_MONTH", "1MTH", "MON":
		return "1M"
	default:
		return "M1"
	}
}

func eqTrue(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes" || v == "y"
}
