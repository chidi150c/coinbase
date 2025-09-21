// FILE: live.go
// Package main – Live loop, real-candle polling, and time helpers.
//
// runLive drives the trading loop in real time:
//   • Warm up by fetching candles (paged from bridge if available; otherwise a large batch from the broker).
//   • Fit the tiny ML model on warmup history.
//   • On each cadence, update history, step the Trader, and update metrics.
//
// Notes:
//   - We prefer real OHLCV from the /candles endpoint instead of synthesizing candles.
//   - History is capped to MaxHistoryCandles to keep memory/compute stable.
//   - Tick mode nudges the last candle with a per-tick price feed.
//   - Works with or without BRIDGE_URL; when absent, we fall back to broker calls.

package main

import (
	"context"
	"log"
	"time"

	"encoding/json"
	"errors" // <-- added
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync" // <-- added
)

// runLive executes the real-time loop with cadence intervalSec (seconds).
func runLive(ctx context.Context, trader *Trader, model *AIMicroModel, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	log.Printf("Starting %s — product=%s dry_run=%v",
		trader.broker.Name(), trader.cfg.ProductID, trader.cfg.DryRun)

	// Safety banner
	log.Printf("[SAFETY] LONG_ONLY=%v | ORDER_MIN_USD=%.2f | RISK_PER_TRADE_PCT=%.2f | MAX_DAILY_LOSS_PCT=%.2f | TAKE_PROFIT_PCT=%.2f | STOP_LOSS_PCT=%.2f | MAX_HISTORY_CANDLES=%d",
		trader.cfg.LongOnly, trader.cfg.OrderMinUSD, trader.cfg.RiskPerTradePct,
		trader.cfg.MaxDailyLossPct, trader.cfg.TakeProfitPct, trader.cfg.StopLossPct, trader.cfg.MaxHistoryCandles)

	// --- Startup health-gate ---
	if trader.cfg.BridgeURL != "" {
		log.Printf("TRACE BOOT bridge_url=%s product=%s granularity=%s target=%d",
			trader.cfg.BridgeURL, trader.cfg.ProductID, trader.cfg.Granularity, func() int {
				if trader.cfg.MaxHistoryCandles > 0 {
					return trader.cfg.MaxHistoryCandles
				}
				return 6000
			}())
		if !waitBridgeHealthy(trader.cfg.BridgeURL, 90*time.Second) {
			log.Printf("[BOOT] bridge health not ready after 90s; proceeding with warmup anyway")
		} else {
			log.Printf("TRACE BOOT bridge health OK")
		}
	} else {
		if !waitBrokerHealthy(ctx, trader, 90*time.Second) {
			log.Printf("[BOOT] broker health not ready after 90s; proceeding with warmup anyway")
		} else {
			log.Printf("TRACE BOOT broker health OK")
		}
	}

	// Warmup candles (paged backfill from bridge to MaxHistoryCandles; else large single fetch from broker)
	var history []Candle
	target := trader.cfg.MaxHistoryCandles
	if target <= 0 {
		target = 6000
	}

	if trader.cfg.BridgeURL != "" {
		log.Printf("TRACE WARMUP using bridge paged fetch tries=10 pageLimit=300 target=%d", target)
		const tries = 10
		for i := 0; i < tries && len(history) == 0; i++ {
			log.Printf("TRACE WARMUP attempt=%d (bridge paged)", i+1)
			if hs, err := fetchHistoryPaged(trader.cfg.BridgeURL, trader.cfg.ProductID, trader.cfg.Granularity, 300, target); err == nil && len(hs) > 0 {
				history = hs
				log.Printf("[BOOT] history=%d (paged to %d target)", len(history), target)
				// TODO: remove TRACE
				log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
				break
			} else if err != nil {
				log.Printf("[BOOT] paged warmup error: %v", err)
				time.Sleep(3 * time.Second)
			} else {
				log.Printf("TRACE WARMUP bridge paged returned 0 rows (attempt=%d)", i+1)
				time.Sleep(1 * time.Second)
			}
		}
	}
	if len(history) == 0 {
		limitTry := target
		if limitTry < 200 {
			limitTry = 200
		}
		log.Printf("TRACE WARMUP broker large batch path limitTry=%d product=%s granularity=%s", limitTry, trader.cfg.ProductID, trader.cfg.Granularity)
		if cs, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, limitTry); err == nil && len(cs) > 0 {
			history = cs
			log.Printf("[BOOT] history=%d (broker large batch, limit=%d)", len(history), limitTry)
			// TODO: remove TRACE
			log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
		} else if err != nil {
			log.Printf("TRACE WARMUP broker large batch error: %v", err)
			log.Printf("TRACE WARMUP broker fallback limit=350")
			if cs2, err2 := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 350); err2 == nil && len(cs2) > 0 {
				history = cs2
				log.Printf("[BOOT] history=%d (broker fallback, limit=350)", len(history))
				// TODO: remove TRACE
				log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
			} else {
				log.Printf("warmup GetRecentCandles error: %v", err)
				if err2 != nil {
					log.Printf("TRACE WARMUP broker fallback error: %v", err2)
				} else {
					log.Printf("TRACE WARMUP broker fallback returned 0 rows")
				}
			}
		} else {
			log.Printf("TRACE WARMUP broker large batch returned 0 rows (limitTry=%d)", limitTry)
			log.Printf("TRACE WARMUP broker fallback limit=350")
			if cs2, err2 := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 350); err2 == nil && len(cs2) > 0 {
				history = cs2
				log.Printf("[BOOT] history=%d (broker fallback, limit=350)", len(history))
				// TODO: remove TRACE
				log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
			} else if err2 != nil {
				log.Printf("TRACE WARMUP broker fallback error: %v", err2)
			} else {
				log.Printf("TRACE WARMUP broker fallback returned 0 rows")
			}
		}
	}
	if len(history) == 0 {
		log.Printf("TRACE WARMUP giving up: no candles after bridge-paged and broker-large/fallback")
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

	// Track if we've successfully rebased equity from live balances (metrics gate).
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

	// --- Tick vs Candle loop selector (bridge optional) ---
	useTick := trader.cfg.UseTick()
	// TODO: remove TRACE
	log.Printf("TRACE selector useTick=%v granularity=%s tick_interval=%d", useTick, trader.cfg.Granularity, trader.cfg.TickInterval())
	// -------------------------------------------------------------------

	if useTick {
		// Tick-driven loop with periodic candle resync
		lastCandleSync := time.Now().UTC()

		for {
			select {
			case <-ctx.Done():
				log.Println("shutdown")
				return
			default:
				// Periodic candle resync from the broker (use getter for hot-ish reload)
				if time.Since(lastCandleSync) >= time.Duration(trader.cfg.CandleResync())*time.Second {
					if latestSlice, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 1); err == nil && len(latestSlice) > 0 {
						latest := latestSlice[len(latestSlice)-1]
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
						// TODO: remove TRACE
						log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
						log.Printf("[SYNC] latest=%s history_last=%s len=%d", latest.Time, history[len(history)-1].Time, len(history))
					} else if err != nil {
						log.Fatalf("[SYNC] Candle update failed: error: %v", err)
					}
				}

				// Per-tick price updates (bridge or broker)
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
				// TODO: remove TRACE
				log.Printf("TRACE price_fetch px=%.8f stale=%v err=%v", px, stale, err)

				// Gate traces (reason why [TICK] block may not run)
				if err != nil {
					// TODO: remove TRACE
					log.Printf("TRACE gate:err err=%v", err)
				} else if stale {
					// TODO: remove TRACE
					log.Printf("TRACE gate:stale px=%.8f", px)
				} else if px <= 0 {
					// TODO: remove TRACE
					log.Printf("TRACE gate:px<=0 px=%.8f", px)
				}

				if err == nil && !stale && px > 0 {
					// Defensive: ensure history non-empty before tick mutate
					if len(history) == 0 {
						// TODO: remove TRACE
						log.Printf("TRACE gate:history_empty before applyTick")
						cancelPx()
						// Skip this iteration safely
						time.Sleep(time.Duration(trader.cfg.TickInterval()) * time.Second)
						continue
					}
					applyTickToLastCandle(history, px)
					log.Printf("[TICK] px=%.2f lastClose(before-step)=%.2f", px, history[len(history)-1].Close)
					// TODO: remove TRACE
					log.Printf("TRACE TARGET [TICK] px=%.2f lastClose(before-step)=%.2f", px, history[len(history)-1].Close)
				}
				cancelPx()

				// Walk-forward refit (optional)
				lastRefit, trader.mdlExt = maybeWalkForwardRefit(trader.cfg, trader.mdlExt, history, lastRefit)

				// Step trader
				_, err = trader.step(ctx, history)
				if err != nil {
					log.Printf("step err: %v", err)
					time.Sleep(time.Duration(trader.cfg.TickInterval()) * time.Second)
					continue
				}

				// Live equity refresh (bridge or broker)
				if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun {
					ctxEq, cancelEq := context.WithTimeout(ctx, 5*time.Second)
					var bal map[string]float64
					if trader.cfg.BridgeURL != "" {
						bal, err = fetchBridgeAccounts(ctxEq, trader.cfg.BridgeURL)
						if err != nil && errors.Is(err, errBridgeAccountsNotFound) {
							// Fallback to broker if bridge lacks /accounts
							log.Printf("TRACE EQUITY fallback: bridge /accounts 404 -> broker")
							bal, err = fetchBrokerBalances(ctxEq, trader, trader.cfg.ProductID)
						}
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
							// TRACE: break down balances and price to see why eq<=0
							lb := bal[strings.ToUpper(base)]
							lq := bal[strings.ToUpper(quote)]
							lp := history[len(history)-1].Close // or latest.Close in candle loop
							log.Printf("TRACE equity_breakdown path=%s base=%s quote=%s bal_base=%.8f bal_quote=%.8f lastPrice=%.8f eq=%.8f",
								func() string {
									if trader.cfg.BridgeURL != "" {
										return "bridge|fallback"
									}
									return "broker"
								}(),
								base, quote, lb, lq, lp, eq)
							log.Printf("[EQUITY] waiting for accounts (eq<=0)")
						}

					} else if !eqReady {
						log.Printf("[EQUITY] waiting for accounts (error: %v)", err)
					}
					cancelEq()
				}
				// Metrics readiness gate for equity
				if eqReady || trader.cfg.DryRun || !trader.cfg.UseLiveEquity() {
					mtxPnL.Set(trader.EquityUSD())
				} else {
					log.Printf("[EQUITY] waiting for accounts (metric withheld)")
				}

				// Sleep before next iteration (use getter for hot-ish reload)
				time.Sleep(time.Duration(trader.cfg.TickInterval()) * time.Second)
			}
		}
	} else {
		// Candle-driven loop
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

				// TODO: remove TRACE
				log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)

				lastRefit, trader.mdlExt = maybeWalkForwardRefit(trader.cfg, trader.mdlExt, history, lastRefit)

				msg, err := trader.step(ctx, history)
				if err != nil {
					log.Printf("step err: %v", err)
					continue
				}
				log.Printf("%s", msg)

				// Live equity refresh (bridge or broker)
				if trader.cfg.UseLiveEquity() && !trader.cfg.DryRun {
					ctxEq, cancelEq := context.WithTimeout(ctx, 5*time.Second)
					var bal map[string]float64
					if trader.cfg.BridgeURL != "" {
						bal, err = fetchBridgeAccounts(ctxEq, trader.cfg.BridgeURL)
						if err != nil && errors.Is(err, errBridgeAccountsNotFound) {
							log.Printf("TRACE EQUITY fallback: bridge /accounts 404 -> broker")
							bal, err = fetchBrokerBalances(ctxEq, trader, trader.cfg.ProductID)
						}
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
							// TRACE: break down balances and price to see why eq<=0
							lb := bal[strings.ToUpper(base)]
							lq := bal[strings.ToUpper(quote)]
							lp := history[len(history)-1].Close // or latest.Close in candle loop
							log.Printf("TRACE equity_breakdown path=%s base=%s quote=%s bal_base=%.8f bal_quote=%.8f lastPrice=%.8f eq=%.8f",
								func() string {
									if trader.cfg.BridgeURL != "" {
										return "bridge|fallback"
									}
									return "broker"
								}(),
								base, quote, lb, lq, lp, eq)
							log.Printf("[EQUITY] waiting for accounts (eq<=0)")
						}

					} else if !eqReady {
						log.Printf("[EQUITY] waiting for accounts (error: %v)", err)
					}
					cancelEq()
				}
				// Metrics readiness gate for equity
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

// Sentinel error for missing bridge /accounts endpoint
var errBridgeAccountsNotFound = errors.New("bridge accounts endpoint not found") // <-- added

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
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, errBridgeAccountsNotFound // <-- added
	}
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

func splitProductID(pid string) (base, quote string) {
	p := strings.ToUpper(strings.TrimSpace(pid))
	parts := strings.Split(p, "-")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	// Handle common suffix (quote) symbols used by Binance/others without a dash.
	// Order matters: match the longest known suffixes first.
	knownQuotes := []string{
		"FDUSD", "USDT", "USDC", "BUSD", "TUSD", // 5/4-letter stablecoins
		"EUR", "GBP", "TRY", "BRL",              // fiat
		"BTC", "ETH", "BNB", "USD",              // crypto/fiat 3-letter
	}
	for _, q := range knownQuotes {
		if strings.HasSuffix(p, q) && len(p) > len(q) {
			return p[:len(p)-len(q)], q
		}
	}

	// Fallback: old heuristic (last 3 chars as quote).
	// If limitTry > 1000 { limitTry = 1000 } // (comment-only; no behavior change)
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
		log.Printf("[EQUITY] waiting for accounts (last price unavailable)")
		return
	}
	if ok := attemptLiveEquityRebase(ctx, cfg, trader, lastPrice); !ok {
		log.Printf("[EQUITY] waiting for accounts (initial rebase pending)")
	}
}

func attemptLiveEquityRebase(ctx context.Context, cfg Config, trader *Trader, lastPrice float64) bool {
	bal, err := fetchBridgeAccounts(ctx, cfg.BridgeURL)
	if err != nil {
		if errors.Is(err, errBridgeAccountsNotFound) {
			log.Printf("TRACE EQUITY fallback: bridge /accounts 404 -> broker") // <-- added
			return attemptLiveEquityRebaseBroker(ctx, trader, lastPrice)        // <-- added
		}
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

// --- Broker-based live-equity (no bridge required) ---

func attemptLiveEquityRebaseBroker(ctx context.Context, trader *Trader, lastPrice float64) bool {
	bal, err := fetchBrokerBalances(ctx, trader, trader.cfg.ProductID)
	if err != nil {
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
	TS        any     `json:"ts"` // accept number/string timestamps
	Stale     bool    `json:"stale"`
}

// parseBridgeTS converts a flexible bridge TS (number seconds/ms or string unix/RFC3339) into time.
func parseBridgeTS(v any) (time.Time, bool) {
	switch t := v.(type) {
	case float64:
		n := int64(t)
		if n > 1e12 { // ms
			return time.Unix(n/1000, 0).UTC(), true
		}
		if n > 0 {
			return time.Unix(n, 0).UTC(), true
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			n := int64(f)
			if n > 1e12 { // ms
				return time.Unix(n/1000, 0).UTC(), true
			}
			if n > 0 {
				return time.Unix(n, 0).UTC(), true
			}
		}
		if tt, err := time.Parse(time.RFC3339, s); err == nil {
			return tt.UTC(), true
		}
	}
	return time.Time{}, false
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

	// Adjust 'stale' based on TS age: treat ticks as fresh if TS is recent (≤3s).
	stale := payload.Stale
	if ts, ok := parseBridgeTS(payload.TS); ok {
		age := time.Since(ts)
		if age < 0 {
			age = 0
		}
		if stale && age <= 3*time.Second {
			stale = false
		}
	}

	return payload.Price, stale, nil
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

// --- schema detection cache (per bridge URL) ---
const (
	candleStyleUnknown = 0
	candleStyleLegacy  = 1 // product_id+granularity, start/end in seconds
	candleStyleBinance = 2 // symbol+interval, startTime/endTime in ms
)

var (
	candleStyleMu      sync.Mutex
	candleStyleByBridge = map[string]int{}
)

func getCandleStyle(bridgeURL string) int {
	k := strings.TrimRight(bridgeURL, "/")
	candleStyleMu.Lock()
	defer candleStyleMu.Unlock()
	return candleStyleByBridge[k]
}
func setCandleStyle(bridgeURL string, style int) {
	k := strings.TrimRight(bridgeURL, "/")
	candleStyleMu.Lock()
	candleStyleByBridge[k] = style
	candleStyleMu.Unlock()
}

func binanceIntervalToken(g string) string {
	switch strings.ToUpper(strings.TrimSpace(g)) {
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
	case "TWO_HOUR":
		return "2h"
	case "FOUR_HOUR":
		return "4h"
	case "SIX_HOUR":
		return "6h"
	case "ONE_DAY":
		return "1d"
	default:
		return ""
	}
}

// fetchHistoryPaged pulls up to want candles from the bridge, paging backward by time.
func fetchHistoryPaged(bridgeURL, productID, granularity string, pageLimit, want int) ([]Candle, error) {
	if pageLimit <= 0 || pageLimit > 350 {
		pageLimit = 350
	}
	if want <= 0 {
		want = 5000
	}
	secPer := granularitySeconds(granularity)
	if secPer <= 0 {
		return nil, fmt.Errorf("unsupported granularity: %s", granularity)
	}

	end := time.Now().UTC().Add(-20 * time.Second)
	out := make([]Candle, 0, want+1024)
	seen := make(map[int64]struct{})

	style := getCandleStyle(bridgeURL) // 0=unknown; prefer to probe

	for len(out) < want {
		start := end.Add(-time.Duration((pageLimit+5)*secPer) * time.Second)

		var (
			rows   []candleJSON
			lastErr error
		)

		// We try up to two styles in a defined order:
		//  - if unknown: binance then legacy
		//  - if known=binance: binance then legacy (fallback if empty)
		//  - if known=legacy: legacy then binance (fallback if empty)
		tryOrder := []int{candleStyleBinance, candleStyleLegacy}
		if style == candleStyleLegacy {
			tryOrder = []int{candleStyleLegacy, candleStyleBinance}
		}

		for _, try := range tryOrder {
			var u string
			switch try {
			case candleStyleBinance:
				iv := binanceIntervalToken(granularity)
				if iv == "" {
					continue
				}
				u = fmt.Sprintf("%s/candles?symbol=%s&interval=%s&limit=%d&startTime=%d&endTime=%d",
					strings.TrimRight(bridgeURL, "/"), productID, iv, pageLimit, start.Unix()*1000, end.Unix()*1000)
			default: // candleStyleLegacy
				u = fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d&start=%d&end=%d",
					strings.TrimRight(bridgeURL, "/"), productID, granularity, pageLimit, start.Unix(), end.Unix())
			}

			log.Printf("TRACE WARMUP PAGE url=%s (style=%s)", u, map[int]string{candleStyleLegacy: "legacy", candleStyleBinance: "binance"}[try])

			resp, err := http.Get(u)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("bridge /candles %d: %s", resp.StatusCode, string(b))
				continue
			}

			var raw any
			if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
				resp.Body.Close()
				lastErr = err
				continue
			}
			resp.Body.Close()

			rows = normalizeCandles(raw)
			log.Printf("TRACE WARMUP PAGE rows=%d window=[%d,%d] limit=%d (style=%s)", len(rows), start.Unix(), end.Unix(), pageLimit, map[int]string{candleStyleLegacy: "legacy", candleStyleBinance: "binance"}[try])

			// Select this style once we see non-empty data
			if len(rows) > 0 && style != try {
				setCandleStyle(bridgeURL, try)
				log.Printf("TRACE WARMUP schema selected=%s", map[int]string{candleStyleLegacy: "legacy", candleStyleBinance: "binance"}[try])
			}

			// If rows is non-nil (even if empty), stop trying alternates and use it.
			if rows != nil {
				break
			}
		}

		// If we couldn't even decode (both attempts errored), return the last error.
		if rows == nil && lastErr != nil {
			return nil, lastErr
		}
		// If rows is empty, stop paging (no more data).
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

	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) > want {
		out = out[len(out)-want:]
	}
	log.Printf("TRACE WARMUP SUMMARY collected=%d want=%d granularity=%s", len(out), want, granularity)
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
		switch m := it.(type) {
		case map[string]any:
			out = append(out, candleJSON{
				Start:  asStr(m["start"]),
				Open:   asStr(m["open"]),
				High:   asStr(m["high"]),
				Low:    asStr(m["low"]),
				Close:  asStr(m["close"]),
				Volume: asStr(m["volume"]),
			})
		case []any:
			// Fallback for Binance /klines array-of-arrays shape:
			// [ openTime(ms), open, high, low, close, volume, closeTime(ms), ... ]
			if len(m) >= 6 {
				startMS := int64(0)
				switch t := m[0].(type) {
				case float64:
					startMS = int64(t)
				case string:
					if f, err := strconv.ParseFloat(t, 64); err == nil {
						startMS = int64(f)
					}
				}
				startSec := startMS / 1000
				out = append(out, candleJSON{
					Start:  strconv.FormatInt(startSec, 10),
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

// ---- simple /health poller (bridge) ----
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

// ---- broker health gate (no bridge required) ----
func waitBrokerHealthy(ctx context.Context, trader *Trader, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx1, cancel1 := context.WithTimeout(ctx, 3*time.Second)
		if px, err := trader.broker.GetNowPrice(ctx1, trader.cfg.ProductID); err == nil && px > 0 {
			cancel1()
			return true
		}
		cancel1()

		ctx2, cancel2 := context.WithTimeout(ctx, 3*time.Second)
		if cs, err := trader.broker.GetRecentCandles(ctx2, trader.cfg.ProductID, trader.cfg.Granularity, 1); err == nil && len(cs) > 0 {
			cancel2()
			return true
		}
		cancel2()

		time.Sleep(2 * time.Second)
	}
	return false
}
