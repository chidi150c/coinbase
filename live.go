// FILE: live.go
// Package main – Live loop, real-candle polling, and time helpers.
//
// runLive drives the trading loop in real time:
//   • Warm up by fetching 350 recent candles from the bridge broker.
//   • Fit the tiny ML model on warmup history.
//   • Every intervalSec seconds, fetch the latest candle(s), update history,
//     ask the Trader to step (which may OPEN/HOLD/EXIT), and update metrics.
//
// Notes:
//   - We prefer real OHLCV from the /candles endpoint instead of synthesizing candles.
//   - History is capped to 5000 candles to keep memory/compute stable.
//   - The tiny indirection for monotonic time is here (monotonicNowSeconds).

package main

import (
	"context"
	"log"
	"time"

	// Minimal additions for live-equity support:
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sort"
)

// runLive executes the real-time loop with cadence intervalSec (seconds).
func runLive(ctx context.Context, trader *Trader, model *AIMicroModel, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	log.Printf("Starting %s — product=%s dry_run=%v",
		trader.broker.Name(), trader.cfg.ProductID, trader.cfg.DryRun)

	// Safety banner for operators (no behavior change)
	log.Printf("[SAFETY] LONG_ONLY=%v | ORDER_MIN_USD=%.2f | RISK_PER_TRADE_PCT=%.2f | MAX_DAILY_LOSS_PCT=%.2f | TAKE_PROFIT_PCT=%.2f | STOP_LOSS_PCT=%.2f | MAX_HISTORY_CANDLES=%d",
		trader.cfg.LongOnly, trader.cfg.OrderMinUSD, trader.cfg.RiskPerTradePct,
		trader.cfg.MaxDailyLossPct, trader.cfg.TakeProfitPct, trader.cfg.StopLossPct, trader.cfg.MaxHistoryCandles)

	// Give the bridge a moment to come up (prevents connection refused at boot) — only when in use.
	if trader.cfg.BridgeURL != "" {
		if !waitBridgeHealthy(trader.cfg.BridgeURL, 90*time.Second) {
			log.Printf("[BOOT] bridge health not ready after 90s; proceeding with warmup anyway")
		}
	}

	// Warmup candles (paged backfill to MaxHistoryCandles with fallback to single fetch)
	var history []Candle
	target := trader.cfg.MaxHistoryCandles
	if target <= 0 {
		target = 6000
	}
	if trader.cfg.BridgeURL != "" {
		// Small retry loop to handle racey startup of the bridge
		const tries = 10
		for i := 0; i < tries && len(history) == 0; i++ {
			if hs, err := fetchHistoryPaged(trader.cfg.BridgeURL, trader.cfg.ProductID, trader.cfg.Granularity, 300, target); err == nil && len(hs) > 0 {
				history = hs
				log.Printf("[BOOT] history=%d (paged to %d target)", len(history), target)
				break
			} else if err != nil {
				log.Printf("[BOOT] paged warmup error: %v", err)
				time.Sleep(3 * time.Second)
			}
		}
	}
	if len(history) == 0 {
		// Fallback single fetch from the broker
		if cs, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 350); err == nil && len(cs) > 0 {
			history = cs
		} else if err != nil {
			log.Printf("warmup GetRecentCandles error: %v", err)
		}
	}
	if len(history) == 0 {
		log.Fatalf("warmup failed: no candles returned")
	}

	// Fit the tiny model
	model.fit(history, 0.05, 4)

	// (Phase-7 opt-in) initial extended head training (no effect unless MODEL_MODE=extended)
	if trader.cfg.Extended().ModelMode == ModelModeExtended {
		if fe, la := BuildExtendedFeatures(history, true); len(fe) > 0 {
			trader.mdlExt = NewExtendedLogit(len(fe[0]))
			trader.mdlExt.FitMiniBatch(fe, la, 0.05, 6, 64)
		}
	}
	var lastRefit *time.Time

	// Track if we've successfully rebased equity from live balances.
	// (Now works with or without bridge.)
	eqReady := !(trader.cfg.UseLiveEquity() && !trader.cfg.DryRun)

	if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun {
		ctxInit, cancelInit := context.WithTimeout(context.Background(), 5*time.Second)
		var attempt bool
		if trader.cfg.BridgeURL != "" {
			attempt = attemptLiveEquityRebase(ctxInit, trader.cfg, trader, history[len(history)-1].Close)
		} else {
			attempt = attemptLiveEquityRebaseBroker(ctxInit, trader, history[len(history)-1].Close)
		}
		if attempt {
			eqReady = true
		} else {
			log.Printf("[EQUITY] waiting for accounts (initial rebase pending)")
		}
		cancelInit()
	}

	// Tick vs Candle loop selector:
	// Previously gated on BridgeURL; now enabled for any broker via per-broker envs.
	useTick := trader.cfg.UseTickPrice()
	tickInterval := trader.cfg.TickIntervalSec()
	if tickInterval <= 0 {
		log.Fatalf("[Error] TICK_INTERVAL_SEC must be positive, got %d", tickInterval)
	}
	candleResync := trader.cfg.CandleResyncSec()
	if candleResync <= 0 {
		candleResync = 60
	}

	if useTick {
		// Tick-driven loop with periodic candle resync
		lastCandleSync := time.Now().UTC()

		for {
			select {
			case <-ctx.Done():
				log.Println("shutdown")
				return
			default:
				// Re-read tick interval each iteration (allows live knob changes)
				tickInterval = trader.cfg.TickIntervalSec()

				// Periodic candle resync
				if time.Since(lastCandleSync) >= time.Duration(candleResync)*time.Second {
					if latestSlice, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 1); err == nil && len(latestSlice) > 0 {
						latest := latestSlice[len(latestSlice)-1]
						// Fallback: if broker didn’t set candle.Time, stamp it with now()
						if latest.Time.IsZero() {
							latest.Time = time.Now().UTC()
						}
						if len(history) == 0 || latest.Time.After(history[len(history)-1].Time) {
							history = append(history, latest)
						} else {
							history[len(history)-1] = latest
						}
						if len(history) > trader.cfg.MaxHistoryCandles {
							history = history[len(history)-trader.cfg.MaxHistoryCandles:]
						}
						lastCandleSync = time.Now().UTC()
						log.Printf("[SYNC] latest=%s history_last=%s len=%d", latest.Time, history[len(history)-1].Time, len(history))
					} else if err != nil {
						log.Fatalf("[SYNC] Candle update failed: error: %v", err)
					}
				}

				// Always poll a tick price each iteration
				ctxPx, cancelPx := context.WithTimeout(ctx, 2*time.Second)
				var px float64
				var stale bool
				var err error
				if trader.cfg.BridgeURL != "" {
					px, stale, err = fetchBridgePrice(ctxPx, trader.cfg.BridgeURL, trader.cfg.ProductID)
				} else {
					px, err = trader.broker.GetNowPrice(ctxPx, trader.cfg.ProductID)
					stale = false
				}
				if err == nil && !stale && px > 0 {
					applyTickToLastCandle(history, px)
					log.Printf("[TICK] px=%.2f lastClose(before-step)=%.2f", px, history[len(history)-1].Close)
				}
				cancelPx()

				// Walk-forward refit
				lastRefit, trader.mdlExt = maybeWalkForwardRefit(trader.cfg, trader.mdlExt, history, lastRefit)

				// Step trader
				_, err = trader.step(ctx, history)
				if err != nil {
					log.Printf("step err: %v", err)
					time.Sleep(time.Duration(tickInterval) * time.Second)
					continue
				}

				// Per-tick live-equity refresh
				if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun {
					ctxEq, cancelEq := context.WithTimeout(ctx, 5*time.Second)
					var bal map[string]float64
					if trader.cfg.BridgeURL != "" {
						bal, err = fetchBridgeAccounts(ctxEq, trader.cfg.BridgeURL)
					} else {
						bal, err = fetchBrokerBalances(ctxEq, trader, trader.cfg.ProductID)
					}
					if err == nil {
						base, quote := splitProductID(trader.cfg.ProductID)
						eq := computeLiveEquity(bal, base, quote, history[len(history)-1].Close)
						if eq > 0 {
							if !eqReady {
								log.Printf("[EQUITY] live balances received; rebased equity to %.2f", eq)
							}
							trader.SetEquityUSD(eq)
							eqReady = true
						} else if !eqReady {
							log.Printf("[EQUITY] waiting for accounts (eq<=0)")
						}
					} else if !eqReady {
						log.Printf("[EQUITY] waiting for accounts (error: %v)", err)
					}
					cancelEq()
				}
				if eqReady || trader.cfg.DryRun || !trader.cfg.UseLiveEquity() {
					mtxPnL.Set(trader.EquityUSD())
				} else {
					log.Printf("[EQUITY] waiting for accounts (metric withheld)")
				}

				// Sleep before next iteration
				time.Sleep(time.Duration(tickInterval) * time.Second)
			}
		}
	} else {
		// Original candle-driven loop
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Println("shutdown")
				return
			case <-ticker.C:
				latestSlice, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 1)
				if err != nil || len(latestSlice) == 0 {
					log.Printf("poll err: %v", err)
					continue
				}
				latest := latestSlice[0]
				if len(history) == 0 || latest.Time.After(history[len(history)-1].Time) {
					history = append(history, latest)
				} else {
					history[len(history)-1] = latest
				}
				if len(history) > trader.cfg.MaxHistoryCandles {
					history = history[len(history)-trader.cfg.MaxHistoryCandles:]
				}

				lastRefit, trader.mdlExt = maybeWalkForwardRefit(trader.cfg, trader.mdlExt, history, lastRefit)

				msg, err := trader.step(ctx, history)
				if err != nil {
					log.Printf("step err: %v", err)
					continue
				}
				log.Printf("%s", msg)

				if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun {
					ctxEq, cancelEq := context.WithTimeout(ctx, 5*time.Second)
					var bal map[string]float64
					if trader.cfg.BridgeURL != "" {
						bal, err = fetchBridgeAccounts(ctxEq, trader.cfg.BridgeURL)
					} else {
						bal, err = fetchBrokerBalances(ctxEq, trader, trader.cfg.ProductID)
					}
					if err == nil {
						base, quote := splitProductID(trader.cfg.ProductID)
						eq := computeLiveEquity(bal, base, quote, latest.Close)
						if eq > 0 {
							if !eqReady {
								log.Printf("[EQUITY] live balances received; rebased equity to %.2f", eq)
							}
							trader.SetEquityUSD(eq)
							eqReady = true
						} else if !eqReady {
							log.Printf("[EQUITY] waiting for accounts (eq<=0)")
						}
					} else if !eqReady {
						log.Printf("[EQUITY] waiting for accounts (error: %v)", err)
					}
					cancelEq()
				}
				if eqReady || trader.cfg.DryRun || !trader.cfg.UseLiveEquity() {
					mtxPnL.Set(trader.EquityUSD())
				} else {
					log.Printf("[EQUITY] waiting for accounts (metric withheld)")
				}
			}
		}
	}
}

// --- time helpers for model.go indirection ---

func monotonicNowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// ===== Minimal helpers for live-equity =====

type bridgeAccountsResp struct {
	Accounts []bridgeAccount `json:"accounts"`
	HasNext  bool            `json:"has_next"`
	Cursor   string          `json:"cursor"`
	Size     int             `json:"size"`
}
type bridgeAmount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}
type bridgeAccount struct {
	Currency         string       `json:"currency"`
	AvailableBalance bridgeAmount `json:"available_balance"`
	Type             string       `json:"type"`
	Platform         string       `json:"platform"`
}

func fetchBridgeAccounts(ctx context.Context, bridgeURL string) (map[string]float64, error) {
	u := strings.TrimRight(bridgeURL, "/") + "/accounts?limit=250"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge /accounts %d: %s", resp.StatusCode, string(b))
	}
	var payload bridgeAccountsResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(payload.Accounts))
	for _, r := range payload.Accounts {
		v, _ := strconv.ParseFloat(strings.TrimSpace(r.AvailableBalance.Value), 64)
		out[strings.ToUpper(strings.TrimSpace(r.Currency))] = v
	}
	return out, nil
}

// NOTE: improved to handle symbols like BTC-USD, BTCUSDT, ETHUSDC, etc.
func splitProductID(pid string) (base, quote string) {
	p := strings.ToUpper(strings.TrimSpace(pid))
	// Explicit dash form "BASE-QUOTE"
	if parts := strings.Split(p, "-"); len(parts) == 2 {
		return parts[0], parts[1]
	}
	// Suffix scan for common quotes (longer first)
	suffixes := []string{"USDT", "USDC", "BUSD", "FDUSD", "TUSD", "DAI", "EUR", "GBP", "TRY", "BTC", "ETH", "USD"}
	for _, sfx := range suffixes {
		if strings.HasSuffix(p, sfx) && len(p) > len(sfx) {
			return p[:len(p)-len(sfx)], sfx
		}
	}
	// Fallback: assume 3-char quote
	if len(p) > 3 {
		return p[:len(p)-3], p[len(p)-3:]
	}
	return p, "USD"
}

func computeLiveEquity(bal map[string]float64, base, quote string, lastPrice float64) float64 {
	q := bal[strings.ToUpper(quote)]
	b := bal[strings.ToUpper(base)]
	return q + b*lastPrice
}

func initLiveEquity(ctx context.Context, cfg Config, trader *Trader, lastPrice float64) {
	if lastPrice <= 0 {
		log.Printf("[EQUITY] waiting for bridge accounts (last price unavailable)")
		return
	}
	if ok := attemptLiveEquityRebase(ctx, cfg, trader, lastPrice); !ok {
		log.Printf("[EQUITY] waiting for bridge accounts (initial rebase pending) lastPrice: %.8f", lastPrice)
	}
}

func attemptLiveEquityRebase(ctx context.Context, cfg Config, trader *Trader, lastPrice float64) bool {
	bal, err := fetchBridgeAccounts(ctx, cfg.BridgeURL)
	if err != nil {
		log.Printf("[EQUITY] error: %v", err)
		return false
	}
	base, quote := splitProductID(cfg.ProductID)
	eq := computeLiveEquity(bal, base, quote, lastPrice)
	if eq > 0 {
		trader.SetEquityUSD(eq)
		return true
	}
	return false
}

// --- NEW: Broker-based live-equity (no bridge required) ---
func attemptLiveEquityRebaseBroker(ctx context.Context, trader *Trader, lastPrice float64) bool {
	bal, err := fetchBrokerBalances(ctx, trader, trader.cfg.ProductID)
	if err != nil {
		log.Printf("[EQUITY] error2: %v", err)
		return false
	}
	base, quote := splitProductID(trader.cfg.ProductID)
	eq := computeLiveEquity(bal, base, quote, lastPrice)
	if eq > 0 {
		trader.SetEquityUSD(eq)
		return true
	}
	return false
}

func fetchBrokerBalances(ctx context.Context, trader *Trader, productID string) (map[string]float64, error) {
	baseAsset, baseAvail, _, err1 := trader.broker.GetAvailableBase(ctx, productID)
	quoteAsset, quoteAvail, _, err2 := trader.broker.GetAvailableQuote(ctx, productID)
	if err1 != nil && err2 != nil {
		if err1 != nil {
			return nil, err1
		}
		return nil, err2
	}
	out := make(map[string]float64, 2)
	if baseAsset != "" {
		out[strings.ToUpper(baseAsset)] = baseAvail
	}
	if quoteAsset != "" {
		out[strings.ToUpper(quoteAsset)] = quoteAvail
	}
	return out, nil
}

// ---- Phase-7: walk-forward refit ----

func maybeWalkForwardRefit(cfg Config, mdl *ExtendedLogit, history []Candle, lastRefit *time.Time) (*time.Time, *ExtendedLogit) {
	if cfg.Extended().ModelMode != ModelModeExtended || cfg.Extended().WalkForwardMin <= 0 {
		return lastRefit, mdl
	}
	now := time.Now().UTC()
	if lastRefit == nil || now.Sub(*lastRefit) >= time.Duration(cfg.Extended().WalkForwardMin)*time.Minute {
		fe, la := BuildExtendedFeatures(history, true)
		if len(fe) >= 100 {
			if mdl == nil {
				mdl = NewExtendedLogit(len(fe[0]))
			}
			mdl.FitMiniBatch(fe, la, 0.05, 6, 64)
			IncWalkForwardFits()
		}
		t := now
		return &t, mdl
	}
	return lastRefit, mdl
}

// ---- Tick-price helpers ----

type bridgePriceResp struct {
	ProductID string  `json:"product_id"`
	Price     float64 `json:"price"`
	TS        string  `json:"ts"`
	Stale     bool    `json:"stale"`
}

func fetchBridgePrice(ctx context.Context, bridgeURL, productID string) (float64, bool, error) {
	u := strings.TrimRight(bridgeURL, "/") + "/price?product_id=" + productID
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return 0, false, fmt.Errorf("bridge /price %d: %s", resp.StatusCode, string(b))
	}
	var payload bridgePriceResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, false, err
	}
	return payload.Price, payload.Stale, nil
}

func applyTickToLastCandle(history []Candle, lastPrice float64) {
	if len(history) == 0 || lastPrice <= 0 {
		return
	}
	i := len(history) - 1
	c := history[i]
	if lastPrice > c.High {
		c.High = lastPrice
	}
	if lastPrice < c.Low || c.Low == 0 {
		c.Low = lastPrice
	}
	c.Close = lastPrice
	history[i] = c
}

// ---- Paged history bootstrap (bridge /candles) ----

type candleJSON struct {
	Start  string `json:"start"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}

// fetchHistoryPaged pulls up to want candles from the bridge, paging backward by time.
// Accepts either a bare JSON array or {"candles":[...]} response forms.
func fetchHistoryPaged(bridgeURL, productID, granularity string, pageLimit, want int) ([]Candle, error) {
	if pageLimit <= 0 || pageLimit > 350 {
		pageLimit = 350
	}
	if want <= 0 {
		want = 5000
	}
	secPer := granularitySeconds(granularity)
	if secPer <= 0 {
		// Fallback to single page via broker is handled by caller if this returns empty
		return nil, fmt.Errorf("unsupported granularity: %s", granularity)
	}

	end := time.Now().UTC().Add(-20 * time.Second) // slight pad to avoid edge filtering
	out := make([]Candle, 0, want+1024)
	seen := make(map[int64]struct{})

	for len(out) < want {
		start := end.Add(-time.Duration((pageLimit+5)*secPer) * time.Second)
		u := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d&start=%d&end=%d",
			strings.TrimRight(bridgeURL, "/"), productID, granularity, pageLimit, start.Unix(), end.Unix())

		resp, err := http.Get(u)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("bridge /candles %d: %s", resp.StatusCode, string(b))
		}
		var raw any
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		rows := normalizeCandles(raw)
		if len(rows) == 0 {
			break
		}

		for _, r := range rows {
			ts, _ := strconv.ParseInt(strings.TrimSpace(r.Start), 10, 64)
			if ts == 0 {
				continue
			}
			if _, ok := seen[ts]; ok {
				continue
			}
			seen[ts] = struct{}{}
			o, _ := strconv.ParseFloat(r.Open, 64)
			h, _ := strconv.ParseFloat(r.High, 64)
			l, _ := strconv.ParseFloat(r.Low, 64)
			c, _ := strconv.ParseFloat(r.Close, 64)
			v, _ := strconv.ParseFloat(r.Volume, 64)
			out = append(out, Candle{
				Time:   time.Unix(ts, 0).UTC(),
				Open:   o,
				High:   h,
				Low:    l,
				Close:  c,
				Volume: v,
			})
		}

		end = start
		if len(rows) < pageLimit {
			break
		}
	}

	// Sort ascending and cap to 'want'
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) > want {
		out = out[len(out)-want:]
	}
	return out, nil
}

func normalizeCandles(raw any) []candleJSON {
	switch v := raw.(type) {
	case []any:
		return toCandleJSON(v)
	case map[string]any:
		if c, ok := v["candles"]; ok {
			if arr, ok := c.([]any); ok {
				return toCandleJSON(arr)
			}
		}
	}
	return nil
}

func toCandleJSON(arr []any) []candleJSON {
	out := make([]candleJSON, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, candleJSON{
				Start:  asStr(m["start"]),
				Open:   asStr(m["open"]),
				High:   asStr(m["high"]),
				Low:    asStr(m["low"]),
				Close:  asStr(m["close"]),
				Volume: asStr(m["volume"]),
			})
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
	default:
		return ""
	}
}

func granularitySeconds(g string) int {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "ONE_MINUTE":
		return 60
	case "FIVE_MINUTE":
		return 5 * 60
	case "FIFTEEN_MINUTE":
		return 15 * 60
	case "THIRTY_MINUTE":
		return 30 * 60
	case "ONE_HOUR":
		return 60 * 60
	case "TWO_HOUR":
		return 2 * 60 * 60
	case "FOUR_HOUR":
		return 4 * 60 * 60
	case "SIX_HOUR":
		return 6 * 60 * 60
	case "ONE_DAY":
		return 24 * 60 * 60
	default:
		return 0
	}
}

// ---- NEW: simple /health poller to avoid initial connection-refused ----
func waitBridgeHealthy(bridgeURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	url := strings.TrimRight(bridgeURL, "/") + "/health"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return true
		}
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	return false
}
