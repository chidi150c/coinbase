// FILE: binance_broker.go
// Package main — Binance Spot broker (direct REST/HMAC).
//
// Drop-in implementation of the existing Broker interface.
// - Maps PRODUCT_ID like "BTC-USD" -> Binance symbol "BTCUSDT" (USD≈USDT).
// - BUY uses quoteOrderQty (USDT). SELL converts quoteUSD->base qty at snapshot price and snaps to LOT_SIZE.
// - Steps/min-notional/precision are derived from /api/v3/exchangeInfo.
// - Balances come from /api/v3/account (signed).
//
// Required env (loaded via your bot.env allowlist or process env):
//   BROKER=binance
//   BINANCE_API_KEY=<key>
//   BINANCE_API_SECRET=<secret>
// Optional:
//   BINANCE_API_BASE=https://api.binance.com
//   BINANCE_RECV_WINDOW_MS=5000
//
// Notes:
// - Binance often reports commission in non-USD assets; CommissionUSD is left 0,
//   and the trader falls back to FEE_RATE_PCT (no behavior change).
// - No external deps; standard library only.

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type BinanceBroker struct {
	apiKey     string
	apiSecret  string
	baseURL    string
	recvWindow int64
	hc         *http.Client

	// per-symbol filters/steps cache
	filters map[string]*bnSymbol
}

type bnSymbol struct {
	symbol         string
	baseAsset      string
	quoteAsset     string
	baseStep       float64 // LOT_SIZE.stepSize
	tickSize       float64 // PRICE_FILTER.tickSize
	minNotional    float64 // MIN_NOTIONAL.minNotional
	quoteStep      float64 // from quote precision; fallback 0.01
	priceDigits    int     // derived from tickSize
	quantityDigits int     // derived from baseStep
}

func NewBinanceBroker() *BinanceBroker {
	base := getEnv("BINANCE_API_BASE", "https://api.binance.com")
	rw := getEnvInt("BINANCE_RECV_WINDOW_MS", 5000)
	return &BinanceBroker{
		apiKey:     getEnv("BINANCE_API_KEY", ""),
		apiSecret:  getEnv("BINANCE_API_SECRET", ""),
		baseURL:    strings.TrimRight(base, "/"),
		recvWindow: int64(rw),
		hc:         &http.Client{Timeout: 10 * time.Second},
		filters:    map[string]*bnSymbol{},
	}
}

func (bb *BinanceBroker) Name() string { return "binance" }

// ----- Helpers -----

func mapProductToSymbol(product string) string {
	p := strings.ToUpper(strings.TrimSpace(product))
	// "BTC-USD" -> "BTCUSDT"
	if strings.HasSuffix(p, "-USD") {
		return strings.ReplaceAll(p[:len(p)-4], "-", "") + "USDT"
	}
	// "ETH-USDT" -> "ETHUSDT"
	return strings.ReplaceAll(p, "-", "")
}

func (bb *BinanceBroker) interval(granularity string) string {
	switch strings.ToUpper(strings.TrimSpace(granularity)) {
	case "ONE_MINUTE":
		return "1m"
	case "FIVE_MINUTE":
		return "5m"
	case "FIFTEEN_MINUTE":
		return "15m"
	case "THIRTY_MINUTE":
		return "30m"
	case "ONE_HOUR":
		return "1h"
	case "FOUR_HOUR":
		return "4h"
	case "ONE_DAY":
		return "1d"
	default:
		return "1m"
	}
}

func (bb *BinanceBroker) sign(q url.Values) string {
	mac := hmac.New(sha256.New, []byte(bb.apiSecret))
	_, _ = io.WriteString(mac, q.Encode())
	return hex.EncodeToString(mac.Sum(nil))
}

func (bb *BinanceBroker) get(ctx context.Context, path string, q url.Values, signed bool) ([]byte, error) {
	if q == nil {
		q = url.Values{}
	}
	if signed {
		q.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
		if bb.recvWindow > 0 {
			q.Set("recvWindow", strconv.FormatInt(bb.recvWindow, 10))
		}
		q.Set("signature", bb.sign(q))
	}
	u := bb.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	// Binance requires API key header on signed and most /api/ endpoints
	if bb.apiKey != "" {
		req.Header.Set("X-MBX-APIKEY", bb.apiKey)
	}
	res, err := bb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	bs, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("binance GET %s: %s", path, string(bs))
	}
	return bs, nil
}

func (bb *BinanceBroker) post(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	if bb.recvWindow > 0 {
		q.Set("recvWindow", strconv.FormatInt(bb.recvWindow, 10))
	}
	q.Set("signature", bb.sign(q))
	u := bb.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(q.Encode()))
	if err != nil {
		return nil, err
	}
	if bb.apiKey != "" {
		req.Header.Set("X-MBX-APIKEY", bb.apiKey)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := bb.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	bs, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("binance POST %s: %s", path, string(bs))
	}
	return bs, nil
}

func (bb *BinanceBroker) ensureSymbol(ctx context.Context, symbol string) (*bnSymbol, error) {
	if s, ok := bb.filters[symbol]; ok {
		return s, nil
	}
	q := url.Values{}
	q.Set("symbol", symbol)
	bs, err := bb.get(ctx, "/api/v3/exchangeInfo", q, false)
	if err != nil {
		return nil, err
	}
	var ex struct {
		Symbols []struct {
			Symbol              string `json:"symbol"`
			BaseAsset           string `json:"baseAsset"`
			QuoteAsset          string `json:"quoteAsset"`
			QuoteAssetPrecision int    `json:"quoteAssetPrecision"`
			Filters             []struct {
				FilterType  string `json:"filterType"`
				StepSize    string `json:"stepSize"`
				TickSize    string `json:"tickSize"`
				MinNotional string `json:"minNotional"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(bs, &ex); err != nil {
		return nil, err
	}
	if len(ex.Symbols) == 0 {
		return nil, fmt.Errorf("exchangeInfo: symbol %s not found", symbol)
	}
	e := ex.Symbols[0]
	sf := &bnSymbol{
		symbol:     e.Symbol,
		baseAsset:  e.BaseAsset,
		quoteAsset: e.QuoteAsset,
		quoteStep:  math.Pow10(-e.QuoteAssetPrecision),
	}
	for _, f := range e.Filters {
		switch f.FilterType {
		case "LOT_SIZE":
			if f.StepSize != "" {
				sf.baseStep, _ = strconv.ParseFloat(f.StepSize, 64)
			}
		case "PRICE_FILTER":
			if f.TickSize != "" {
				sf.tickSize, _ = strconv.ParseFloat(f.TickSize, 64)
			}
		case "MIN_NOTIONAL":
			if f.MinNotional != "" {
				sf.minNotional, _ = strconv.ParseFloat(f.MinNotional, 64)
			}
		}
	}
	if sf.baseStep <= 0 {
		sf.baseStep = 0.000001 // conservative fallback
	}
	if sf.quoteStep <= 0 {
		sf.quoteStep = 0.01 // conservative fallback
	}
	sf.priceDigits = digitsFromStep(sf.tickSize, 2)
	sf.quantityDigits = digitsFromStep(sf.baseStep, 6)

	bb.filters[symbol] = sf
	return sf, nil
}

func digitsFromStep(step float64, def int) int {
	if step <= 0 {
		return def
	}
	s := fmt.Sprintf("%.12f", step)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		n := len(strings.TrimRight(s[i+1:], "0"))
		if n < 0 {
			return def
		}
		if n > 10 {
			n = 10
		}
		return n
	}
	return def
}

func formatWithDigits(v float64, digits int) string {
	if digits <= 0 {
		return fmt.Sprintf("%.0f", v)
	}
	if digits > 10 {
		digits = 10
	}
	return fmt.Sprintf("%."+strconv.Itoa(digits)+"f", v)
}

func toStr(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// ----- Broker methods -----

func (bb *BinanceBroker) GetNowPrice(ctx context.Context, product string) (float64, error) {
	symbol := mapProductToSymbol(product)
	q := url.Values{}
	q.Set("symbol", symbol)
	bs, err := bb.get(ctx, "/api/v3/ticker/price", q, false)
	if err != nil {
		return 0, err
	}
	var p struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(bs, &p); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(p.Price, 64)
}

func (bb *BinanceBroker) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	symbol := mapProductToSymbol(product)
	interval := bb.interval(granularity)
	if limit <= 0 || limit > 1000 {
		limit = 500
	}

	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))

	bs, err := bb.get(ctx, "/api/v3/klines", q, false)
	if err != nil {
		return nil, err
	}

	// kline: [ openTime, open, high, low, close, volume, closeTime, ... ]
	var raw [][]interface{}
	if err := json.Unmarshal(bs, &raw); err != nil {
		return nil, err
	}
	out := make([]Candle, 0, len(raw))
	for _, row := range raw {
		if len(row) < 6 {
			continue
		}
		openTime := time.UnixMilli(int64(row[0].(float64))).UTC()
		open, _ := strconv.ParseFloat(toStr(row[1]), 64)
		high, _ := strconv.ParseFloat(toStr(row[2]), 64)
		low, _ := strconv.ParseFloat(toStr(row[3]), 64)
		close, _ := strconv.ParseFloat(toStr(row[4]), 64)
		vol, _ := strconv.ParseFloat(toStr(row[5]), 64)

		out = append(out, Candle{
			Time:   openTime,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: vol,
		})
	}
	return out, nil
}

func (bb *BinanceBroker) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	symbol := mapProductToSymbol(product)
	sf, err := bb.ensureSymbol(ctx, symbol)
	if err != nil {
		return "", 0, 0, err
	}
	bal, err := bb.accountBalance(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	avail := bal[strings.ToUpper(sf.baseAsset)]
	return sf.baseAsset, avail, sf.baseStep, nil
}

func (bb *BinanceBroker) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	symbol := mapProductToSymbol(product)
	sf, err := bb.ensureSymbol(ctx, symbol)
	if err != nil {
		return "", 0, 0, err
	}
	bal, err := bb.accountBalance(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	avail := bal[strings.ToUpper(sf.quoteAsset)]
	return sf.quoteAsset, avail, sf.quoteStep, nil
}

func (bb *BinanceBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	symbol := mapProductToSymbol(product)
	sf, err := bb.ensureSymbol(ctx, symbol)
	if err != nil {
		return nil, err
	}

	// Fresh price snapshot for SELL sizing and fallback pricing.
	price, err := bb.GetNowPrice(ctx, product)
	if err != nil || price <= 0 {
		return nil, fmt.Errorf("price snapshot failed: %v", err)
	}

	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("side", strings.ToUpper(string(side)))
	q.Set("type", "MARKET")
	q.Set("newOrderRespType", "FULL") // best effort to get fills

	var qtyStr string
	switch side {
	case SideBuy:
		// BUY uses quote order qty in USDT (USD≈USDT for our mapping).
		quoteStr := formatWithDigits(quoteUSD, digitsFromStep(sf.quoteStep, 2))
		q.Set("quoteOrderQty", quoteStr)
	case SideSell:
		// Convert quoteUSD -> base quantity at snapshot price and snap to LOT_SIZE.
		base := quoteUSD / price
		if sf.baseStep > 0 {
			base = math.Floor(base/sf.baseStep) * sf.baseStep
		}
		if base <= 0 {
			return nil, fmt.Errorf("computed base size <= 0 after step snap")
		}
		qtyStr = formatWithDigits(base, sf.quantityDigits)
		q.Set("quantity", qtyStr)
	default:
		return nil, fmt.Errorf("unsupported side: %s", side)
	}

	// Place order
	bs, err := bb.post(ctx, "/api/v3/order", q)
	if err != nil {
		return nil, err
	}

	// Parse minimal fields (FULL response may include fills; we accept partials).
	var ord struct {
		OrderID          int64  `json:"orderId"`
		TransactTime     int64  `json:"transactTime"`
		ExecutedQty      string `json:"executedQty"`
		CummulativeQuote string `json:"cummulativeQuoteQty"`
	}
	_ = json.Unmarshal(bs, &ord)

	var (
		baseFilled float64
		quoteSpent float64
		px         float64
	)
	baseFilled, _ = strconv.ParseFloat(ord.ExecutedQty, 64)
	quoteSpent, _ = strconv.ParseFloat(ord.CummulativeQuote, 64)

	if baseFilled > 0 && quoteSpent > 0 {
		px = quoteSpent / baseFilled
	} else {
		// Fallbacks
		px = price
		if side == SideBuy && quoteUSD > 0 && price > 0 && baseFilled == 0 {
			baseFilled = quoteUSD / price
		}
		if side == SideSell && baseFilled == 0 && qtyStr != "" {
			baseFilled, _ = strconv.ParseFloat(qtyStr, 64)
			quoteSpent = baseFilled * px
		}
	}

	po := &PlacedOrder{
		ID:            fmt.Sprintf("%d", ord.OrderID),
		ProductID:     product,
		Side:          side,
		Price:         px,
		BaseSize:      baseFilled,
		QuoteSpent:    quoteSpent,
		CommissionUSD: 0,                // not reported in USD; trader falls back to FEE_RATE_PCT
		CreateTime:    time.Now().UTC(), // approximate; Binance returns transactTime in ms if needed
	}
	return po, nil
}

// ---- Account/balances ----

func (bb *BinanceBroker) accountBalance(ctx context.Context) (map[string]float64, error) {
	bs, err := bb.get(ctx, "/api/v3/account", url.Values{}, true)
	if err != nil {
		return nil, err
	}
	var a struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(bs, &a); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(a.Balances))
	for _, b := range a.Balances {
		f, _ := strconv.ParseFloat(b.Free, 64)
		out[strings.ToUpper(b.Asset)] = f
	}
	return out, nil
}
