You are joining an existing Coinbase Advanced Trade bot project. Invariant baseline (must remain stable and NOT be re-generated):

Operating rules:

Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed.

Default to INCREMENTAL CHANGES ONLY; ask for file context if needed.

Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:

Provide a brief Plan for: [***study ws_binance.py, ws_hitbtc.py, app.py, broker_<broker>.go files attached to to trace and understand why this "warmup failed: no candles returned" showing up and whether; updating the Binance/HitBTC WS trade handling to use params.symbols in the subscribe message and parse trades accepting symbol|t|s for instrument and price|p for price; would resolve the issue***]

1) For each item in the plan, first output a single-sentence CHANGE_DESCRIPTION:
   - Format: verb + what is being changed + how/with what.
   - Imperative style (e.g., “add…”, “update…”, “remove…”).
   - Describe exactly the code change required; avoid general context.
   - Avoid vague terms like “improve/enhance/fix” unless paired with the specific element being changed.
   - One sentence per item only.

2) Pause and verify required inputs. If ANY input is missing, ask me to provide it before writing code.
   - Examples of required inputs:
     - Source files: e.g., “paste current live.go / trader.go / strategy.go / model.go / metrics.go / config.go / env.go / backtest.go / broker*.go / bridge/app.py / monitoring/docker-compose.yml”.
     - Env/config values: e.g., “what Slack webhook URL?”, “what Docker base image?”, “list current /opt/coinbase/env/bot.env and bridge.env”.
     - External URLs/IDs (API keys should never be pasted in clear; ask me to confirm they are already configured).
   - Never guess; explicitly request missing files or settings.

3) After all inputs are provided, generate the code:
   - Output the complete updated file(s), copy-paste ready.
   - Apply only minimal edits needed to satisfy the CHANGE_DESCRIPTION(s).
   - Do not rename or remove existing functions, structs, metrics, environment keys, log strings, CLI flags, routes, or file paths unless the plan item explicitly requires it.
   - Maintain metrics compatibility and logging style.
   - Keep dependencies minimal; if adding any, list the precise versions and justify them in one line.

4) Safety & operations rules:
   - If a change affects live trading behavior, include an explicit SAFETY CALLOUT and REVERT INSTRUCTIONS (exact env changes or commands to roll back).
   - Provide a short runbook: required env edits, shell commands, restart instructions, and verification steps (health checks/metrics queries).
   - All changes must extend the bot safely and incrementally, without rewriting or replacing the existing foundation.

Example workflow:
Plan item: “fix startup equity spike in live.go.”
1. CHANGE_DESCRIPTION: `update initLiveEquity and per-tick equity refresh to skip setting trader equity until bridge accounts return a valid value, logging a waiting message instead`
2. Ask: “Please paste your current live.go so I can apply the change.”
3. After file is provided, output the complete updated live.go with only the minimal edits to implement the description, plus:
   - Safety callout (no risk to live trading; behavior is deferred until balances are available).
   - Revert instructions (restore previous lines X–Y to set equity immediately).
   - Runbook (commands to rebuild/restart and how to verify via /metrics and logs).

Note: Pause and verify required inputs. If ANY input is missing, ask me to provide it before writing code. - Examples of required inputs: - Source files: e.g., “paste current live.go / trader.go / strategy.go / model.go / metrics.go / config.go / env.go / backtest.go / broker*.go / bridge/app.py / monitoring/docker-compose.yml”. - Env/config values: e.g., “what Slack webhook URL?”, “what Docker base image?”, “list current /opt/coinbase/env/bot.env and bridge.env”. - External URLs/IDs (API keys should never be pasted in clear; ask me to confirm they are already configured). - Never guess; explicitly request missing files or settings.

chidi@Dynamo:~/coinbase$ tree
.
├── Dockerfile
├── Makefile
├── backtest.go
├── bot.log
├── bot.pid
├── bridge
│   ├── Dockerfile
│   ├── __pycache__
│   │   └── app.cpython-312.pyc
│   ├── app.py
│   └── requirements.txt
├── bridge_binance
│   ├── Dockerfile
│   └── ws_binance.py
├── bridge_hitbtc
│   ├── Dockerfile
│   └── ws_hitbtc.py
├── broker.go
├── broker_binance.go
├── broker_bridge.go
├── broker_hitbtc.go
├── broker_paper.go
├── config.go
├── data
│   ├── BTC-USD-Backup.csv
│   └── BTC-USD.csv
├── env.go
├── go.mod
├── go.sum
├── indicators.go
├── live.go
├── main.go
├── metrics.go
├── misc
│   ├── Deploy.md
│   ├── README.md
│   ├── README2.md
│   ├── README3.md
│   ├── README4.md
│   ├── issues.md
│   ├── prompt1.md
│   ├── prompt2.md
│   ├── prompt4A.md
│   ├── prompt4B.md
│   ├── prompt5.md
│   ├── prompt5B-code-continuity.md
│   ├── prompt6.md
│   ├── prompt7_Change_Description.md
│   ├── prompt7_Change_Proccess.md
│   └── promtp7_Change_Tiny.md
├── model.go
├── monitoring
│   ├── alertmanager
│   │   └── alertmanager.yml
│   ├── docker-compose.override.yml
│   ├── docker-compose.override.yml-
│   ├── docker-compose.prod.yml
│   ├── docker-compose.yml
│   ├── grafana
│   │   └── bot-dashboard.json
│   ├── grafana-data
│   ├── order.json
│   ├── order_response.json
│   └── prometheus
│       ├── prometheus.yml
│       └── rules.yml
├── order.json
├── smoke_coinbase.go
├── strategy.go
├── tools
│   └── backfill_bridge_paged.go
├── trader.go
└── verify.txt

13 directories, 61 files
chidi@Dynamo:~/coinbase$ 

live.go {{
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
		if !waitBridgeHealthy(trader.cfg.BridgeURL, 90*time.Second) {
			log.Printf("[BOOT] bridge health not ready after 90s; proceeding with warmup anyway")
		}
	} else {
		if !waitBrokerHealthy(ctx, trader, 90*time.Second) {
			log.Printf("[BOOT] broker health not ready after 90s; proceeding with warmup anyway")
		}
	}

	// Warmup candles (paged backfill from bridge to MaxHistoryCandles; else large single fetch from broker)
	var history []Candle
	target := trader.cfg.MaxHistoryCandles
	if target <= 0 {
		target = 6000
	}

	if trader.cfg.BridgeURL != "" {
		const tries = 10
		for i := 0; i < tries && len(history) == 0; i++ {
			if hs, err := fetchHistoryPaged(trader.cfg.BridgeURL, trader.cfg.ProductID, trader.cfg.Granularity, 300, target); err == nil && len(hs) > 0 {
				history = hs
				log.Printf("[BOOT] history=%d (paged to %d target)", len(history), target)
				// TODO: remove TRACE
				log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
				break
			} else if err != nil {
				log.Printf("[BOOT] paged warmup error: %v", err)
				time.Sleep(3 * time.Second)
			}
		}
	}
	if len(history) == 0 {
		limitTry := target
		if limitTry > 1000 {
			limitTry = 1000
		}
		if limitTry < 350 {
			limitTry = 350
		}
		if cs, err := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, limitTry); err == nil && len(cs) > 0 {
			history = cs
			log.Printf("[BOOT] history=%d (broker large batch, limit=%d)", len(history), limitTry)
			// TODO: remove TRACE
			log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
		} else if err != nil {
			if cs2, err2 := trader.broker.GetRecentCandles(ctx, trader.cfg.ProductID, trader.cfg.Granularity, 350); err2 == nil && len(cs2) > 0 {
				history = cs2
				log.Printf("[BOOT] history=%d (broker fallback, limit=350)", len(history))
				// TODO: remove TRACE
				log.Printf("TRACE history readiness len=%d need=%d", len(history), trader.cfg.MaxHistoryCandles)
			} else {
				log.Printf("warmup GetRecentCandles error: %v", err)
			}
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
}}

config.go {{
   // FILE: config.go
// Package main – Runtime configuration model and loader.
//
// This file defines the Config struct (all the knobs your bot uses) and a
// helper to populate it from environment variables. The .env file is read
// by loadBotEnv() (see env.go), so you can tune behavior without exports.
//
// Typical flow (see main.go):
//   loadBotEnv()
//   initThresholdsFromEnv()
//   cfg := loadConfigFromEnv()
package main

// NOTE: All knobs here are now read from UNPREFIXED env keys. Broker-specific
// API creds remain broker-prefixed (e.g., BINANCE_API_KEY/SECRET, HITBTC_API_KEY/SECRET)
// and are consumed by the respective broker clients, not here.

import "strings"

// Config holds all runtime knobs for trading and operations.
type Config struct {
	// Trading target
	ProductID   string // e.g., "BTC-USD" or "BTCUSDT"
	Granularity string // e.g., "ONE_MINUTE"

	// Safety & sizing
	DryRun          bool
	MaxDailyLossPct float64
	RiskPerTradePct float64
	USDEquity       float64
	TakeProfitPct   float64
	StopLossPct     float64
	OrderMinUSD     float64
	LongOnly        bool    // prevent SELL entries when flat on spot
	FeeRatePct      float64 // % fee applied on entry/exit trades

	// Ops
	Port              int
	BridgeURL         string // optional: http://127.0.0.1:8787 (only when using the bridge)
	MaxHistoryCandles int    // plural: loaded from MAX_HISTORY_CANDLES
	StateFile         string // path to persist bot state

	// Loop control (unprefixed; universal)
	UseTickPrice    bool // enable tick-driven loop
	TickIntervalSec int  // per-tick cadence
	CandleResyncSec int  // periodic candle resync in tick loop

	// Live equity gating (unprefixed; universal)
	LiveEquity bool // if true, rebase & refresh equity from live balances
}

// loadConfigFromEnv reads the process env (already hydrated by loadBotEnv())
// and returns a Config with sane defaults if keys are missing.
func loadConfigFromEnv() Config {
	cfg := Config{
		ProductID:         getEnv("PRODUCT_ID", "BTC-USD"),
		Granularity:       getEnv("GRANULARITY", "ONE_MINUTE"),

		// Universal, unprefixed knobs
		DryRun:            getEnvBool("DRY_RUN", true),
		MaxDailyLossPct:   getEnvFloat("MAX_DAILY_LOSS_PCT", 1.0),
		RiskPerTradePct:   getEnvFloat("RISK_PER_TRADE_PCT", 0.25),
		USDEquity:         getEnvFloat("USD_EQUITY", 1000.0),
		TakeProfitPct:     getEnvFloat("TAKE_PROFIT_PCT", 0.8),
		StopLossPct:       getEnvFloat("STOP_LOSS_PCT", 0.4),
		OrderMinUSD:       getEnvFloat("ORDER_MIN_USD", 5.00),
		LongOnly:          getEnvBool("LONG_ONLY", true),
		FeeRatePct:        getEnvFloat("FEE_RATE_PCT", 0.3),

		Port:              getEnvInt("PORT", 8080),
		BridgeURL:         getEnv("BRIDGE_URL", ""),
		MaxHistoryCandles: getEnvInt("MAX_HISTORY_CANDLES", 5000),
		StateFile:         getEnv("STATE_FILE", "/opt/coinbase/state/bot_state.json"),

		// Loop control
		UseTickPrice:    getEnvBool("USE_TICK_PRICE", false),
		TickIntervalSec: getEnvInt("TICK_INTERVAL_SEC", 1),
		CandleResyncSec: getEnvInt("CANDLE_RESYNC_SEC", 60),

		// Live equity
		LiveEquity: getEnvBool("USE_LIVE_EQUITY", false),
	}

	// Historical carry-over: if someone still sets BROKER=X, we may still
	// want to validate it's present, but we no longer use it to select knobs.
	_ = strings.TrimSpace(getEnv("BROKER", ""))

	return cfg
}

// ---- cfg helpers (getter methods) ----
// These fetch from env at call-time (falling back to the struct's initial values).
// This lets you tweak knobs live IF your process env is refreshed.

func (c *Config) UseLiveEquity() bool {
	return getEnvBool("USE_LIVE_EQUITY", c.LiveEquity)
}

func (c *Config) UseTick() bool {
	return getEnvBool("USE_TICK_PRICE", c.UseTickPrice)
}

func (c *Config) TickInterval() int {
	// Default to initial value; clamp to >=1
	ti := getEnvInt("TICK_INTERVAL_SEC", c.TickIntervalSec)
	if ti <= 0 {
		ti = 1
	}
	return ti
}

func (c *Config) CandleResync() int {
	cr := getEnvInt("CANDLE_RESYNC_SEC", c.CandleResyncSec)
	if cr <= 0 {
		cr = 60
	}
	return cr
}

// ---- Phase-7 toggles (append-only; no behavior changes unless envs set) ----

// ModelMode selects the prediction path; baseline is the default.
type ModelMode string

const (
	ModelModeBaseline ModelMode = "baseline"
	ModelModeExtended ModelMode = "extended"
)

// ExtendedToggles exposes optional Phase-7 features without altering existing behavior.
type ExtendedToggles struct {
	ModelMode      ModelMode // baseline (default) or extended
	WalkForwardMin int       // minutes between live refits; 0 disables
	VolRiskAdjust  bool      // enable volatility-aware risk sizing
	UseDirectSlack bool      // true if SLACK_WEBHOOK is set (optional direct pings)
}

// Extended reads optional Phase-7 toggles from env. Defaults preserve baseline behavior.
func (c *Config) Extended() ExtendedToggles {
	mm := ModelMode(getEnv("MODEL_MODE", string(ModelModeBaseline)))
	if mm != ModelModeExtended {
		mm = ModelModeBaseline
	}
	return ExtendedToggles{
		ModelMode:      mm,
		WalkForwardMin: getEnvInt("WALK_FORWARD_MIN", 0),
		VolRiskAdjust:  getEnvBool("VOL_RISK_ADJUST", false),
		UseDirectSlack: getEnv("SLACK_WEBHOOK", "") != "",
	}
}

// --- trailing env accessors (unchanged; universal) ---
func (c *Config) TrailActivatePct() float64 { return getEnvFloat("TRAIL_ACTIVATE_PCT", 1.2) }
func (c *Config) TrailDistancePct() float64 { return getEnvFloat("TRAIL_DISTANCE_PCT", 0.6) }
}}
broker.go{{
// FILE: broker.go
// Package main – Broker abstractions shared by all execution backends.
//
// This file defines the minimal interface the trading loop needs to talk to a
// market-execution backend (paper or real):
//   • Broker interface: price lookup, place market order by quote USD, fetch candles
//   • Common types: OrderSide, PlacedOrder
//
// Two concrete implementations live in separate files:
//   • broker_paper.go   – in-memory paper broker (no external calls)
//   • broker_bridge.go  – HTTP client for the Python FastAPI sidecar
package main

import (
	"context"
	"time"
)

// OrderSide is the side of a trade.
type OrderSide string

const (
	SideBuy  OrderSide = "BUY"
	SideSell OrderSide = "SELL"
)

// PlacedOrder is a normalized view of a filled/placed market order.
type PlacedOrder struct {
	ID            string
	ProductID     string
	Side          OrderSide
	Price         float64 // average/assumed execution price
	BaseSize      float64 // filled base (e.g., BTC)
	QuoteSpent    float64 // spent quote (e.g., USD)
	CommissionUSD float64 // total commission in quote currency (USD)
	CreateTime    time.Time
}

// Broker is the minimal surface the bot needs to operate.
type Broker interface {
	Name() string
	GetNowPrice(ctx context.Context, product string) (float64, error)
	PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error)
	GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error)
	GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error)
	GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error)
}
}}
broker_binance.go{{
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
	"net/http"
	"net/url"
	"strings"
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
	return &BinanceBridge{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
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

	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}

	// Enrich via GET /order/{order_id}, identical to broker_bridge.go
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o2, err := b.fetchOrder(ctx, product, ord.ID)
		if err == nil && (o2.BaseSize > 0 || o2.QuoteSpent > 0) {
			return o2, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return &ord, nil
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
	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}
	return &ord, nil
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

}}
broker_hitbtc.go{{
// FILE: broker_hitbtc.go
// Package main — HTTP broker against the HitBTC FastAPI sidecar.
// NOTE: This is a minimal clone of broker_bridge.go with only base URL and Name() changed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HitbtcBridge struct {
	base string
	hc   *http.Client
}

func NewHitbtcBridge(base string) *HitbtcBridge {
	if strings.TrimSpace(base) == "" {
		// default to the docker-compose service for HitBTC bridge
		base = "http://bridge_hitbtc:8788"
	}
	base = strings.TrimRight(base, "/")
	return &HitbtcBridge{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (h *HitbtcBridge) Name() string { return "hitbtc-bridge" }

// --- Price / Product ---

func (h *HitbtcBridge) GetNowPrice(ctx context.Context, product string) (float64, error) {
	u := fmt.Sprintf("%s/product/%s", h.base, url.PathEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := h.hc.Do(req)
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

func (h *HitbtcBridge) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 350
	}
	u := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d",
		h.base, url.QueryEscape(product), url.QueryEscape(granularity), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.hc.Do(req)
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

// --- Live balances / equity helpers ---

func (h *HitbtcBridge) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	u := fmt.Sprintf("%s/balance/base?product_id=%s", h.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, 0, err
	}
	resp, err := h.hc.Do(req)
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

func (h *HitbtcBridge) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	u := fmt.Sprintf("%s/balance/quote?product_id=%s", h.base, url.QueryEscape(product))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", 0, 0, err
	}
	resp, err := h.hc.Do(req)
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

// --- Orders (market by quote), identical body/shape to broker_bridge.go ---

func (h *HitbtcBridge) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	body := map[string]any{
		"product_id": product,
		"side":       side, // IMPORTANT: no .String(); mirror broker_bridge.go
		"quote_size": fmt.Sprintf("%.8f", quoteUSD),
	}
	data, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/order/market", h.base)

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order/market %d: %s", resp.StatusCode, string(xb))
	}

	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}

	// Enrich via GET /order/{order_id}, identical to broker_bridge.go
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o2, err := h.fetchOrder(ctx, product, ord.ID)
		if err == nil && (o2.BaseSize > 0 || o2.QuoteSpent > 0) {
			return o2, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return &ord, nil
}

func (h *HitbtcBridge) fetchOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	u := fmt.Sprintf("%s/order/%s", h.base, url.PathEscape(orderID))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		xb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge order get %d: %s", resp.StatusCode, string(xb))
	}
	var ord PlacedOrder
	if err := json.NewDecoder(resp.Body).Decode(&ord); err != nil {
		return nil, err
	}
	return &ord, nil
}

// --- helpers (EXACTLY like broker_bridge.go) ---

// func toCandles(rows []struct {
// 	Start  string
// 	Open   string
// 	High   string
// 	Low    string
// 	Close  string
// 	Volume string
// }) []Candle {
// 	out := make([]Candle, 0, len(rows))
// 	for _, r := range rows {
// 		out = append(out, Candle{
// 			Time:   toUnixTime(r.Start),
// 			Open:   parseFloat(r.Open),
// 			High:   parseFloat(r.High),
// 			Low:    parseFloat(r.Low),
// 			Close:  parseFloat(r.Close),
// 			Volume: parseFloat(r.Volume),
// 		})
// 	}
// 	return out
// }

// func toUnixTime(secStr string) time.Time {
// 	sec := int64(parseFloat(secStr))
// 	return time.Unix(sec, 0).UTC()
// }

// func parseFloat(s string) float64 {
// 	s = strings.TrimSpace(s)
// 	if s == "" {
// 		return 0
// 	}
// 	var f float64
// 	fmt.Sscanf(s, "%f", &f)
// 	return f
// }
}}
broker_bridge.go{{
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

}}

========================================================================================================
========================================================================================================

ws_binance.py{{
#!/usr/bin/env python3
# FILE: bridge_binance/ws_binance.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using Binance WS/REST.
import os, asyncio, json, time, hmac, hashlib
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq, parse as urlparse
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path
from fastapi.responses import JSONResponse
import websockets
import uvicorn

# ---- Config (mirror coinbase .env knobs where applicable) ----
SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8789"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

BINANCE_API_KEY     = os.getenv("BINANCE_API_KEY", "").strip()
BINANCE_API_SECRET  = os.getenv("BINANCE_API_SECRET", "").strip()
BINANCE_BASE_URL    = os.getenv("BINANCE_BASE_URL", "https://api.binance.com").strip().rstrip("/")
BINANCE_RECV_WINDOW = os.getenv("BINANCE_RECV_WINDOW", "5000").strip()  # ms

# ---- In-memory state ----
last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}  # minuteStartMs -> [o,h,l,c,vol]

# ---- Helpers ----
def _now_ms() -> int: return int(time.time() * 1000)
def _now_iso() -> str: return datetime.utcnow().replace(tzinfo=timezone.utc).isoformat()
def _minute_start(ts_ms: int) -> int: return (ts_ms // 60000) * 60000

def _trim_old_candles(max_minutes: int = 6000):
    if len(candles) <= max_minutes: return
    for k in sorted(candles.keys())[:-max_minutes]:
        candles.pop(k, None)

def _update_candle(px: float, ts_ms: int, vol: float = 0.0):
    m = _minute_start(ts_ms)
    if m not in candles:
        candles[m] = [px, px, px, px, vol]
    else:
        o,h,l,c,v = candles[m]
        if px > h: h = px
        if px < l: l = px
        candles[m] = [o,h,l,px,v+vol]
    _trim_old_candles()

def _binance_signed(path: str, params: Dict[str,str]) -> Dict:
    if not BINANCE_API_KEY or not BINANCE_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing BINANCE_API_KEY/SECRET")
    p = dict(params or {})
    p.setdefault("timestamp", str(_now_ms()))
    p.setdefault("recvWindow", BINANCE_RECV_WINDOW)
    q = urlparse.urlencode(p, doseq=True)
    sig = hmac.new(BINANCE_API_SECRET.encode("utf-8"), q.encode("utf-8"), hashlib.sha256).hexdigest()
    url = f"{BINANCE_BASE_URL}{path}?{q}&signature={sig}"
    req = urlreq.Request(url, method="GET", headers={"X-MBX-APIKEY": BINANCE_API_KEY})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON from Binance")

def _binance_signed_post(path: str, params: Dict[str,str]) -> Dict:
    if not BINANCE_API_KEY or not BINANCE_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing BINANCE_API_KEY/SECRET")
    p = dict(params or {})
    p.setdefault("timestamp", str(_now_ms()))
    p.setdefault("recvWindow", BINANCE_RECV_WINDOW)
    q = urlparse.urlencode(p, doseq=True)
    sig = hmac.new(BINANCE_API_SECRET.encode("utf-8"), q.encode("utf-8"), hashlib.sha256).hexdigest()
    url = f"{BINANCE_BASE_URL}{path}?{q}&signature={sig}"
    req = urlreq.Request(url, method="POST", headers={"X-MBX-APIKEY": BINANCE_API_KEY})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON from Binance")

def _split_product(pid: str) -> Tuple[str,str]:
    p = pid.upper().replace("-", "")
    for q in ["FDUSD","USDT","USDC","BUSD","TUSD","EUR","GBP","TRY","BRL","BTC","ETH","BNB","USD"]:
        if p.endswith(q) and len(p) > len(q): return p[:-len(q)], q
    if len(p) > 3: return p[:-3], p[-3:]
    return p, "USD"

def _sum_available(accts: List[Dict], asset: str) -> str:
    asset = asset.upper()
    for a in accts:
        if a.get("currency","").upper() == asset:
            return str(a.get("available_balance",{}).get("value","0"))
    return "0"

# ---- WS consumer ----
async def _ws_loop():
    global last_price, last_ts_ms
    sym = SYMBOL.lower()
    url = f"wss://stream.binance.com:9443/stream?streams={sym}@bookTicker/{sym}@trade"
    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
                backoff = 1
                while True:
                    raw = await ws.recv()
                    ts = _now_ms()
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        continue
                    stream = msg.get("stream","")
                    data   = msg.get("data",{})
                    px = None
                    if stream.endswith("@bookTicker"):
                        a = data.get("a"); b = data.get("b")
                        try:
                            if a is not None and b is not None:
                                px = (float(a)+float(b))/2.0
                        except Exception:
                            px = None
                    elif stream.endswith("@trade"):
                        p = data.get("p")
                        try:
                            if p is not None: px = float(p)
                        except Exception:
                            px = None
                    if px and px > 0:
                        last_price = px
                        last_ts_ms = ts
                        _update_candle(px, ts)
        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

# ---- FastAPI app (mirrors Coinbase endpoints) ----
app = FastAPI(title="bridge-binance", version="0.2")

@app.get("/health")
def health(): return {"ok": True}

@app.get("/price")
def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None; stale = True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms()-last_ts_ms)
        stale  = age_ms > STALE_MS
    return {"product_id": product_id, "price": float(last_price) if last_price else 0.0, "ts": last_ts_ms, "stale": stale}

@app.get("/candles")
def get_candles(product_id: str = Query(default=SYMBOL),
           granularity: str = Query(default="ONE_MINUTE"),
           start: Optional[int] = Query(default=None),
           end: Optional[int] = Query(default=None),
           limit: int = Query(default=350)):
    if granularity != "ONE_MINUTE":
        return {"candles": []}
    # --- minimal change: auto-detect seconds vs milliseconds for start/end ---
    def _ms(x: Optional[int]) -> Optional[int]:
        if x is None: return None
        # treat values < 10^12 as seconds; convert to ms
        return x * 1000 if x < 1_000_000_000_000 else x
    s_ms = _ms(start)
    e_ms = _ms(end)
    keys = sorted(k for k in candles.keys() if (s_ms is None or k >= s_ms) and (e_ms is None or k <= e_ms))
    rows = []
    for k in keys[-limit:]:
        o,h,l,c,v = candles[k]
        rows.append({"start": str(k//1000), "open": str(o), "high": str(h), "low": str(l), "close": str(c), "volume": str(v)})
    return {"candles": rows}

@app.get("/accounts")
def accounts(limit: int = 250):
    payload = _binance_signed("/api/v3/account", {})
    bals = payload.get("balances") or []
    out = []
    for b in bals:
        asset = str(b.get("asset","")).upper()
        free  = str(b.get("free","0"))
        out.append({
            "currency": asset,
            "available_balance": {"value": free, "currency": asset},
            "type": "spot",
            "platform": "binance",
        })
    return {"accounts": out, "has_next": False, "cursor": "", "size": len(out)}

@app.get("/balance/base")
def balance_base(product_id: str = Query(...)):
    base, _ = _split_product(product_id)
    accts = accounts()  # same process
    value = _sum_available(accts["accounts"], base)
    return {"asset": base, "available": value, "step": "0"}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(...)):
    _, quote = _split_product(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], quote)
    return {"asset": quote, "available": value, "step": "0"}

@app.get("/product/{product_id}")
def product_info(product_id: str = Path(...)):
    # Minimal parity: return latest price with same shape used by Go client
    # (Coinbase bridge exposes more metadata; we surface price+stale here.)
    return price(product_id)

# --- Order endpoints (market by quote size), with partial-fill enrichment ---

def _my_trades(symbol: str, order_id: int) -> List[Dict]:
    # https://binance-docs.github.io/apidocs/spot/en/#account-trade-list-user_data
    return _binance_signed("/api/v3/myTrades", {"symbol": symbol, "orderId": str(order_id)})

@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    # Delegate to single market endpoint (Coinbase bridge exposes both)
    return order_market(product_id=product_id, side="BUY", quote_size=quote_size)

@app.post("/order/market")
def order_market(product_id: str = Query(...), side: str = Query(...), quote_size: str = Query(...)):
    # Place market order using quote as notional (Binance uses quoteOrderQty)
    sym = product_id.upper().replace("-", "")
    side = side.upper()
    payload = _binance_signed_post("/api/v3/order",
        {"symbol": sym, "side": side, "type": "MARKET", "quoteOrderQty": quote_size})

    order_id = payload.get("orderId")
    resp = {
        "order_id": str(order_id),
        "product_id": product_id,
        "status": "open",
        "created_at": _now_iso(),
        "filled_size": "0",
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": side.lower(),
    }

    # Enrich via myTrades (fills)
    try:
        trades = _my_trades(sym, int(order_id))
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("qty","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("commission","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T" if t.get("isBuyerMaker") else "M",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty * price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done"
    except Exception:
        pass
    return resp

@app.get("/order/{order_id}")
def order_get(order_id: str, product_id: str = Query(default=SYMBOL)):
    sym = product_id.upper().replace("-", "")
    od = _binance_signed("/api/v3/order", {"symbol": sym, "orderId": order_id})
    # Normalize minimal Coinbase-like order view + enrich with trades
    resp = {
        "order_id": str(od.get("orderId")),
        "product_id": product_id,
        "status": "open" if od.get("status") in ("NEW","PARTIALLY_FILLED") else "done",
        "created_at": _now_iso(),
        "filled_size": str(od.get("executedQty","0")),
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": str(od.get("side","")).lower(),
    }
    try:
        trades = _my_trades(sym, int(order_id))
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("qty","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("commission","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T" if t.get("isBuyerMaker") else "M",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty * price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done" if str(od.get("status")) in ("FILLED","EXPIRED","CANCELED","REJECTED") else resp["status"]
    except Exception:
        pass
    return resp

# ---- Runner ----
async def _runner():
    task = asyncio.create_task(_ws_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass

}}
ws_hitbtc.py{{#!/usr/bin/env python3
# FILE: bridge_hitbtc/ws_hitbtc.py
# FastAPI app that mirrors Coinbase bridge endpoints/JSON using HitBTC WS/REST.
import os, asyncio, json, time, base64, logging
from decimal import Decimal
from typing import Dict, List, Optional, Tuple
from urllib import request as urlreq
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Query, Path
from fastapi.responses import JSONResponse
import websockets
import uvicorn

# ---- Logging ----
log = logging.getLogger("bridge-hitbtc")
logging.basicConfig(level=logging.INFO)

SYMBOL   = os.getenv("SYMBOL", "BTCUSDT").upper().replace("-", "")
PORT     = int(os.getenv("PORT", "8788"))
STALE_MS = int(os.getenv("STALE_MS", "3000"))

HITBTC_API_KEY   = os.getenv("HITBTC_API_KEY", "").strip()
HITBTC_API_SECRET= os.getenv("HITBTC_API_SECRET", "").strip()
HITBTC_BASE_URL  = os.getenv("HITBTC_BASE_URL", "https://api.hitbtc.com").strip().rstrip("/")

last_price: Optional[float] = None
last_ts_ms: Optional[int] = None
candles: Dict[int, List[float]] = {}

def _now_ms() -> int: return int(time.time()*1000)
def _now_iso() -> str: return datetime.utcnow().replace(tzinfo=timezone.utc).isoformat()
def _minute_start(ts_ms: int) -> int: return (ts_ms//60000)*60000

def _trim_old(max_minutes=6000):
    if len(candles) <= max_minutes: return
    for k in sorted(candles.keys())[:-max_minutes]:
        candles.pop(k,None)

def _update_candle(px: float, ts_ms: int, vol: float = 0.0):
    m = _minute_start(ts_ms)
    if m not in candles:
        candles[m] = [px,px,px,px,vol]
    else:
        o,h,l,c,v = candles[m]
        if px>h: h=px
        if px<l: l=px
        candles[m] = [o,h,l,px,v+vol]
    _trim_old()

def _normalize_symbol(pid: str) -> str:
    # HitBTC typically uses BTCUSDT as well (no dash). Just upper-case and strip '-'.
    return (pid or SYMBOL).upper().replace("-", "")

def _http_get(url: str, headers: Optional[Dict[str,str]] = None):
    req = urlreq.Request(url, headers=headers or {})
    with urlreq.urlopen(req, timeout=10) as resp:
        body = resp.read().decode("utf-8", "ignore")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=body[:200])
        try:
            return json.loads(body)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON")

def _req(path: str, method="GET", body: Optional[bytes]=None) -> Dict:
    if not HITBTC_API_KEY or not HITBTC_API_SECRET:
        raise HTTPException(status_code=401, detail="Missing HITBTC_API_KEY/SECRET")
    url = f"{HITBTC_BASE_URL}{path}"
    token = base64.b64encode(f"{HITBTC_API_KEY}:{HITBTC_API_SECRET}".encode("utf-8")).decode("ascii")
    headers = {"Authorization": f"Basic {token}"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urlreq.Request(url, method=method, data=body, headers=headers)
    with urlreq.urlopen(req, timeout=10) as resp:
        data = resp.read().decode("utf-8")
        if resp.status != 200:
            raise HTTPException(status_code=resp.status, detail=data[:200])
        try:
            return json.loads(data)
        except Exception:
            raise HTTPException(status_code=500, detail="Invalid JSON from HitBTC")

def _split(pid: str) -> Tuple[str,str]:
    p = pid.upper().replace("-", "")
    # HitBTC typically uses BTCUSDT as well
    if len(p)>3: return p[:-3], p[-3:]
    return p, "USD"

def _sum_available(accts: List[Dict], asset: str) -> str:
    a = asset.upper()
    for r in accts:
        if r.get("currency","").upper()==a:
            return str(r.get("available_balance",{}).get("value","0"))
    return "0"

async def _ws_loop():
    global last_price, last_ts_ms
    url = "wss://api.hitbtc.com/api/3/ws/public"
    sub = {"method":"subscribe","ch":"trades","params":{"symbols":[_normalize_symbol(SYMBOL)]}}
    backoff = 1
    while True:
        try:
            async with websockets.connect(url, ping_interval=20, ping_timeout=20, max_queue=1024) as ws:
                await ws.send(json.dumps(sub))
                log.info(f"[TRACE] WS subscribe params={sub}")
                backoff = 1
                while True:
                    raw = await ws.recv()
                    ts = _now_ms()
                    try:
                        msg = json.loads(raw)
                    except Exception:
                        continue
                    if msg.get("ch")=="trades" and "data" in msg:
                        for d in msg["data"]:
                            instr = d.get("symbol") or d.get("t") or d.get("s")
                            if instr == _normalize_symbol(SYMBOL):
                                pr = d.get("price", d.get("p"))
                                try:
                                    px = float(pr) if pr is not None else None
                                except Exception:
                                    px = None
                                if px and px>0:
                                    last_price = px
                                    last_ts_ms = ts
                                    _update_candle(px, ts)
        except Exception:
            await asyncio.sleep(backoff)
            backoff = min(backoff*2, 15)

app = FastAPI(title="bridge-hitbtc", version="0.2")

@app.get("/health")
def health(): return {"ok": True}

@app.get("/price")
def price(product_id: str = Query(default=SYMBOL)):
    age_ms = None; stale=True
    if last_ts_ms is not None:
        age_ms = max(0, _now_ms()-last_ts_ms)
        stale  = age_ms > STALE_MS
    return {"product_id": product_id, "price": float(last_price) if last_price else 0.0, "ts": last_ts_ms, "stale": stale}

@app.get("/candles")
def get_candles(product_id: str = Query(default=SYMBOL),
           granularity: str = Query(default="ONE_MINUTE"),
           start: Optional[int] = Query(default=None),
           end: Optional[int] = Query(default=None),
           limit: int = Query(default=350)):
    if granularity != "ONE_MINUTE":
        return {"candles":[]}
    # auto-detect seconds vs milliseconds for start/end (seconds < 1e12)
    def _ms(x: Optional[int]) -> Optional[int]:
        if x is None: return None
        return x * 1000 if x < 1_000_000_000_000 else x
    s_ms = _ms(start)
    e_ms = _ms(end)

    # 1) Serve from in-memory first
    keys = sorted(k for k in candles.keys() if (s_ms is None or k>=s_ms) and (e_ms is None or k<=e_ms))
    rows=[]
    for k in keys[-limit:]:
        o,h,l,c,v = candles[k]
        rows.append({"start": str(k//1000), "open": str(o), "high": str(h), "low": str(l), "close": str(c), "volume": str(v)})
    if rows:
        return {"candles": rows}

    # 2) REST backfill from public endpoint
    sym = _normalize_symbol(product_id)
    # HitBTC v3 public candles: GET /api/3/public/candles/{symbol}?period=M1&limit=N
    period = "M1"
    url = f"{HITBTC_BASE_URL}/api/3/public/candles/{sym}?period={period}&limit={min(max(1,limit),1000)}"
    try:
        data = _http_get(url)
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"hitbtc candles error: {e}")

    out=[]
    # shape: [{"t":"2024-01-01T00:00:00Z","o":"...","h":"...","l":"...","c":"...","v":"..."}...]
    for r in data[-limit:]:
        try:
            t = r.get("t") or r.get("timestamp")
            dt = datetime.fromisoformat(t.replace("Z","+00:00"))
            ot = int(dt.timestamp())
            o,h,l,c,v = str(r.get("o","")), str(r.get("h","")), str(r.get("l","")), str(r.get("c","")), str(r.get("v",""))
        except Exception:
            continue
        out.append({"start": str(ot), "open": o, "high": h, "low": l, "close": c, "volume": v})
        try:
            _update_candle(float(c), ot*1000)
        except Exception:
            pass
    log.info(f"[TRACE] REST candles fetched: symbol={sym} period={period} rows={len(out)}")
    return {"candles": out}

@app.get("/accounts")
def accounts(limit: int = 250):
    # HitBTC: GET /api/3/spot/balance
    payload = _req("/api/3/spot/balance")
    rows = payload if isinstance(payload, list) else payload.get("balance", [])
    out=[]
    for r in rows:
        cur = str(r.get("currency","")).upper()
        avail = str(r.get("available", r.get("cash","0")))
        out.append({
            "currency": cur,
            "available_balance": {"value": avail, "currency": cur},
            "type": "spot",
            "platform": "hitbtc",
        })
    return {"accounts": out, "has_next": False, "cursor": "", "size": len(out)}

@app.get("/balance/base")
def balance_base(product_id: str = Query(...)):
    base, _ = _split(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], base)
    return {"asset": base, "available": value, "step": "0"}

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(...)):
    _, quote = _split(product_id)
    accts = accounts()
    value = _sum_available(accts["accounts"], quote)
    return {"asset": quote, "available": value, "step": "0"}

@app.get("/product/{product_id}")
def product_info(product_id: str = Path(...)):
    # Minimal parity: return price view as Coinbase broker expects
    return price(product_id)

# --- Orders (market), partial-fill enrichment ---
# HitBTC v3 spot order create: POST /api/3/spot/order { "symbol": "...", "side": "buy|sell", "type":"market", "quantity": "..."}
# We implement market-by-quote as: quantity ≈ quote / last_price (best-effort), then place type=market.
def _place_order(symbol: str, side: str, quantity: str) -> Dict:
    body = json.dumps({"symbol": symbol, "side": side.lower(), "type":"market", "quantity": quantity}).encode("utf-8")
    return _req("/api/3/spot/order", method="POST", body=body)

def _get_order(order_id: str) -> Dict:
    # HitBTC v3: GET /api/3/spot/order/{order_id}
    return _req(f"/api/3/spot/order/{order_id}")

def _order_trades(order_id: str) -> List[Dict]:
    # HitBTC v3: GET /api/3/spot/order/{order_id}/trades
    payload = _req(f"/api/3/spot/order/{order_id}/trades")
    return payload if isinstance(payload, list) else payload.get("trades", [])

@app.post("/orders/market_buy")
def orders_market_buy(product_id: str = Query(...), quote_size: str = Query(...)):
    return order_market(product_id=product_id, side="BUY", quote_size=quote_size)

@app.post("/order/market")
def order_market(product_id: str = Query(...), side: str = Query(...), quote_size: str = Query(...)):
    sym = _normalize_symbol(product_id)
    side = side.upper()
    # Approximate quantity from last price (HitBTC doesn't natively support quote notional in v3)
    px = last_price or 0.0
    if px <= 0:
        raise HTTPException(status_code=503, detail="Last price unavailable")
    qty = Decimal(quote_size) / Decimal(str(px))
    od = _place_order(sym, side, str(qty))

    order_id = str(od.get("id") or od.get("order_id") or "")
    resp = {
        "order_id": order_id,
        "product_id": product_id,
        "status": "open",
        "created_at": _now_iso(),
        "filled_size": "0",
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": side.lower(),
    }

    # Enrich with trades
    try:
        trades = _order_trades(order_id)
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills = []
        for t in trades:
            qty = Decimal(str(t.get("quantity","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("fee","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty * price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done"
    except Exception:
        pass
    return resp

@app.get("/order/{order_id}")
def order_get(order_id: str, product_id: str = Query(default=SYMBOL)):
    od = _get_order(order_id)
    # Basic normalization + enrichment
    resp = {
        "order_id": str(od.get("id") or order_id),
        "product_id": product_id,
        "status": "open" if str(od.get("status","")).lower() in ("new","partially_filled") else "done",
        "created_at": _now_iso(),
        "filled_size": str(od.get("quantity_cumulative","0")),
        "executed_value": "0",
        "fill_fees": "0",
        "fills": [],
        "side": str(od.get("side","")).lower(),
    }
    try:
        trades = _order_trades(order_id)
        filled = Decimal("0"); value = Decimal("0"); fee = Decimal("0")
        fills=[]
        for t in trades:
            qty = Decimal(str(t.get("quantity","0")))
            price = Decimal(str(t.get("price","0")))
            commission = Decimal(str(t.get("fee","0")))
            fills.append({
                "price": str(price),
                "size": str(qty),
                "fee": str(commission),
                "liquidity": "T",
                "time": _now_iso(),
            })
            filled += qty
            value  += qty*price
            fee    += commission
        if fills:
            resp["fills"] = fills
            resp["filled_size"] = str(filled)
            resp["executed_value"] = str(value)
            resp["fill_fees"] = str(fee)
            resp["status"] = "done" if str(od.get("status","")).lower() in ("filled","canceled","expired","rejected") else resp["status"]
    except Exception:
        pass
    return resp

async def _runner():
    task = asyncio.create_task(_ws_loop())
    cfg  = uvicorn.Config(app, host="0.0.0.0", port=PORT, log_level="info")
    srv  = uvicorn.Server(cfg)
    await asyncio.gather(task, srv.serve())

if __name__ == "__main__":
    try: asyncio.run(_runner())
    except KeyboardInterrupt: pass

}}
app.py{{
import os
import json
from pathlib import Path
from decimal import Decimal
from urllib import request as urlreq
from urllib import parse as urlparse

from fastapi import FastAPI, HTTPException, Query
from pydantic import BaseModel
from coinbase.rest import RESTClient
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, List, Optional, Literal

# Auto-load parent .env: ~/coinbase/.env
try:
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).resolve().parents[1] / ".env")
except Exception:
    pass

# === EXISTING AUTH (unchanged) ===
API_KEY = os.environ["COINBASE_API_KEY_NAME"]               # organizations/.../apiKeys/...
API_SECRET = os.getenv("COINBASE_API_PRIVATE_KEY") or os.getenv("COINBASE_API_SECRET")
if not API_SECRET:
    raise RuntimeError("Missing COINBASE_API_PRIVATE_KEY (or COINBASE_API_SECRET)")
if "\\n" in API_SECRET:
    API_SECRET = API_SECRET.replace("\\n", "\n")

client = RESTClient(api_key=API_KEY, api_secret=API_SECRET)
app = FastAPI(title="coinbase-bridge", version="0.1")

# === EXISTING ROUTES (unchanged) ===

@app.get("/health")
def health():
    return {"ok": True}

@app.get("/accounts")
def accounts(limit: int = 1):
    try:
        return client.get_accounts(limit=limit).to_dict()
    except Exception as e:
        raise HTTPException(status_code=401, detail=str(e))

@app.get("/product/{product_id}")
def product(product_id: str):
    try:
        p = client.get_product(product_id)
        return p.to_dict() if hasattr(p, "to_dict") else p
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

class MarketBuy(BaseModel):
    client_order_id: str
    product_id: str
    quote_size: str

@app.post("/orders/market_buy")
def market_buy(payload: MarketBuy):
    try:
        return client.market_order_buy(
            client_order_id=payload.client_order_id,
            product_id=payload.product_id,
            quote_size=payload.quote_size,
        )
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === NEW/UPDATED: /candles + /order/market ===

# Canonical granularity to seconds (per Coinbase enums)
_GRAN_TO_SECONDS = {
    "ONE_MINUTE": 60,
    "FIVE_MINUTE": 5 * 60,
    "FIFTEEN_MINUTE": 15 * 60,
    "THIRTY_MINUTE": 30 * 60,
    "ONE_HOUR": 60 * 60,
    "TWO_HOUR": 2 * 60 * 60,
    "FOUR_HOUR": 4 * 60 * 60,
    "SIX_HOUR": 6 * 60 * 60,
    "ONE_DAY": 24 * 60 * 60,
}

def _to_unix_s(dt: datetime) -> str:
    return str(int(dt.timestamp()))

def _normalize_candles(raw: Any) -> List[Dict[str, str]]:
    """
    Output: [{"start","open","high","low","close","volume"}, ...] with values as strings.
    Accepts SDK dict/list, or public endpoint response.
    """
    if hasattr(raw, "to_dict"):
        raw = raw.to_dict()

    rows = raw.get("candles") if isinstance(raw, dict) else raw
    if rows is None:
        rows = []

    out: List[Dict[str, str]] = []
    for r in rows:
        if isinstance(r, dict):
            start = str(r.get("start") or r.get("start_time") or r.get("time") or "")
            open_ = str(r.get("open") or "")
            high  = str(r.get("high") or "")
            low   = str(r.get("low") or "")
            close = str(r.get("close") or "")
            vol   = str(r.get("volume") or r.get("vol") or "")
        else:
            # list/tuple [start, open, high, low, close, volume]
            try:
                start, open_, high, low, close, vol = [str(x) for x in r]
            except Exception:
                continue

        out.append({
            "start": start,      # per spec: UNIX seconds (string)
            "open": open_,
            "high": high,
            "low": low,
            "close": close,
            "volume": vol,
        })
    return out

def _call_candles_via_sdk(product_id: str, granularity: str, start_unix: str, end_unix: str, limit: int):
    """
    Use the official SDK with UNIX seconds. Try multiple method/arg shapes.
    """
    methods = []
    for name in ("get_candles", "get_market_candles", "list_candles"):
        if hasattr(client, name):
            methods.append(getattr(client, name))
    if not methods:
        raise RuntimeError("RESTClient has no candle method (expected get_candles/get_market_candles/list_candles)")

    # Per OpenAPI: start/end required; limit max 350
    argsets = [
        {"product_id": product_id, "granularity": granularity, "start": start_unix, "end": end_unix, "limit": limit},
        {"product_id": product_id, "granularity": granularity, "start": start_unix, "end": end_unix},
        {"product_id": product_id, "granularity": granularity, "start_time": start_unix, "end_time": end_unix, "limit": limit},
        {"product_id": product_id, "granularity": granularity, "start_time": start_unix, "end_time": end_unix},
    ]

    last_err: Optional[Exception] = None
    for fn in methods:
        for kwargs in argsets:
            try:
                return fn(**kwargs)
            except TypeError as te:
                last_err = te
                continue
            except Exception as e:
                last_err = e
                continue
    raise last_err or RuntimeError("All candle method attempts failed")

def _call_candles_via_public_http(product_id: str, granularity: str, start_unix: str, end_unix: str, limit: int):
    """
    Fallback to HTTP. Note: the canonical path is /api/v3/brokerage/products/{product_id}/candles.
    This endpoint typically expects auth; this is a last-resort path for environments where it works.
    """
    base = "https://api.coinbase.com"
    path = f"/api/v3/brokerage/products/{urlparse.quote(product_id)}/candles"
    qs = {
        "granularity": granularity,
        "start": start_unix,
        "end": end_unix,
        "limit": str(limit),
    }
    url = f"{base}{path}?{urlparse.urlencode(qs)}"
    req = urlreq.Request(url, headers={"User-Agent": "coinbase-bridge/1.0"})
    with urlreq.urlopen(req, timeout=15) as resp:
        body = resp.read()
        if resp.status >= 300:
            raise HTTPException(status_code=resp.status, detail=f"public candles error: {body.decode('utf-8', 'ignore')}")
    data = json.loads(body.decode("utf-8", "ignore"))
    return _normalize_candles(data)

@app.get("/candles")
def candles(
    product_id: str = Query(..., description="e.g., BTC-USD"),
    granularity: str = Query("ONE_MINUTE", description="ONE_MINUTE, FIVE_MINUTE, FIFTEEN_MINUTE, THIRTY_MINUTE, ONE_HOUR, TWO_HOUR, FOUR_HOUR, SIX_HOUR, ONE_DAY"),
    limit: int = Query(300, ge=1, le=350),  # clamp to API max 350
    start: Optional[str] = Query(None, description="UNIX seconds (string). If omitted, window is inferred."),
    end: Optional[str]   = Query(None, description="UNIX seconds (string). If omitted, now is used."),
):
    """
    Returns normalized OHLCV list. If start/end are omitted, infer a window of
    (limit × granularity) ending at 'now' per API requirements. All times
    sent to Coinbase are **UNIX seconds** (strings), matching the spec.
    """
    try:
        sec = _GRAN_TO_SECONDS.get(granularity, 60)

        # Build time window in UTC
        if end and end.strip().isdigit():
            end_dt = datetime.fromtimestamp(int(end.strip()), tz=timezone.utc)
        else:
            end_dt = datetime.now(timezone.utc)

        if start and start.strip().isdigit():
            start_dt = datetime.fromtimestamp(int(start.strip()), tz=timezone.utc)
        else:
            # add a small buffer (+2 buckets) to avoid edge truncation
            start_dt = end_dt - timedelta(seconds=sec * max(1, min(limit + 2, 350)))

        # Ensure start < end
        if start_dt >= end_dt:
            start_dt = end_dt - timedelta(seconds=sec * max(1, min(limit, 350)))

        start_unix = _to_unix_s(start_dt)
        end_unix   = _to_unix_s(end_dt)

        # SDK first (auth), fallback HTTP (best-effort)
        try:
            raw = _call_candles_via_sdk(product_id, granularity, start_unix, end_unix, limit)
            return _normalize_candles(raw)
        except Exception:
            raw = _call_candles_via_public_http(product_id, granularity, start_unix, end_unix, limit)
            return raw
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# ---- /order/market (unified) ----

class MarketOrder(BaseModel):
    product_id: str
    side: Literal["BUY", "SELL"]
    quote_size: str
    client_order_id: Optional[str] = None

def _get_price(product_id: str) -> Decimal:
    p = client.get_product(product_id)
    if hasattr(p, "to_dict"):
        p = p.to_dict()
    price_str = str(p.get("price") or p.get("mid_market_price") or p.get("ask") or p.get("bid") or "")
    if not price_str:
        raise RuntimeError("Could not retrieve product price")
    return Decimal(price_str)

@app.post("/order/market")
def order_market(payload: MarketOrder):
    """
    Unified market order by quote_size (USD), side = BUY|SELL.
    For SELL, if the SDK rejects quote_size, fallback to base_size using current price.
    """
    try:
        if payload.side == "BUY":
            return client.market_order_buy(
                client_order_id=payload.client_order_id,
                product_id=payload.product_id,
                quote_size=payload.quote_size,
            )

        # SELL path
        if hasattr(client, "market_order_sell"):
            try:
                return client.market_order_sell(
                    client_order_id=payload.client_order_id,
                    product_id=payload.product_id,
                    quote_size=payload.quote_size,
                )
            except Exception:
                price = _get_price(payload.product_id)
                base_size = (Decimal(payload.quote_size) / price).quantize(Decimal("0.00000001"))
                return client.market_order_sell(
                    client_order_id=payload.client_order_id,
                    product_id=payload.product_id,
                    base_size=str(base_size),
                )

        if hasattr(client, "place_market_order"):
            return client.place_market_order(
                client_order_id=payload.client_order_id,
                product_id=payload.product_id,
                side="SELL",
                quote_size=payload.quote_size,
            )

        raise RuntimeError("RESTClient missing market_order_sell/place_market_order")
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === INCREMENTAL ADDITIONS: optional WS ticker + /price endpoint (opt-in) ===

# These additions are fully optional and do not change existing behavior unless enabled via env:
#   COINBASE_WS_ENABLE=true
#   COINBASE_WS_PRODUCTS=BTC-USD[,ETH-USD,...]
#   COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com
#   COINBASE_WS_STALE_SEC=10
import asyncio
import time
try:
    import websockets  # type: ignore
except Exception:
    websockets = None  # degrade gracefully if package not present

WS_ENABLE = os.getenv("COINBASE_WS_ENABLE", "false").lower() == "true"
WS_URL = os.getenv("COINBASE_WS_URL", "wss://advanced-trade-ws.coinbase.com")
WS_PRODUCTS = [p.strip() for p in os.getenv("COINBASE_WS_PRODUCTS", "BTC-USD").split(",") if p.strip()]
WS_STALE_SEC = int(os.getenv("COINBASE_WS_STALE_SEC", "10"))

_last_ticks: Dict[str, Dict[str, float]] = {}  # {"BTC-USD": {"price": 12345.67, "ts": 1690000000.0}}
_ws_task: Optional[asyncio.Task] = None

async def _ws_consume():
    if not WS_ENABLE:
        return
    if websockets is None:
        print("[bridge] websockets not installed; WS disabled")
        return
    # Coinbase Advanced Trade WS subscribe format
    payload = {"type": "subscribe", "channel": "ticker", "product_ids": WS_PRODUCTS}
    backoff = 1
    while True:
        try:
            async with websockets.connect(WS_URL, ping_interval=20, ping_timeout=20) as ws:
                await ws.send(json.dumps(payload))
                print(f"[bridge] WS connected: {WS_URL} products={WS_PRODUCTS}")
                backoff = 1
                async for msg in ws:
                    try:
                        ev = json.loads(msg)
                        pid = ev.get("product_id") or ev.get("productId") or ev.get("product")
                        price = ev.get("price") or ev.get("last_trade_price") or ev.get("best_ask") or ev.get("best_bid")
                        # If top-level keys missing, try event-wrapped payloads
                        if not (pid and price):
                            events = ev.get("events") or []
                            if events and isinstance(events, list):
                                e0 = events[0]
                                if isinstance(e0, dict) and "tickers" in e0 and e0["tickers"]:
                                    t0 = e0["tickers"][0]
                                    pid = t0.get("product_id") or t0.get("productId")
                                    price = t0.get("price")
                        if pid and price:
                            try:
                                p = float(price)
                                ts = time.time()
                                _last_ticks[pid] = {"price": p, "ts": ts}
                            except Exception:
                                pass
                    except Exception:
                        continue
        except Exception as e:
            print(f"[bridge] WS error: {e}; reconnecting in {backoff}s")
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 30)

@app.on_event("startup")
async def _startup_ws():
    if WS_ENABLE and websockets is not None:
        loop = asyncio.get_event_loop()
        global _ws_task
        if _ws_task is None:
            _ws_task = loop.create_task(_ws_consume())
            print(f"[bridge] WS ticker enabled; products={WS_PRODUCTS}")

@app.get("/price")
def price(product_id: str = Query(..., alias="product_id")):
    rec = _last_ticks.get(product_id)
    if not rec:
        return {"error": "no_tick", "product_id": product_id}
    ts = rec["ts"]
    stale = (time.time() - ts) > WS_STALE_SEC
    t_iso = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts))
    return {"product_id": product_id, "price": rec["price"], "ts": t_iso, "stale": stale}

# === MINIMAL ADDITION: GET /order/{order_id} (fills/status summary) ===

@app.get("/order/{order_id}")
def get_order(order_id: str):
    """
    Return a minimal summary for an order:
      - status (if available from fills payload)
      - filled_size (sum of fill sizes, **BASE units**)
      - average_filled_price (size-weighted)
      - commission_total_usd (sum of fills[].commission)
    Notes:
      * Uses the official SDK's fills method(s) and is robust to naming differences.
      * If no fills are found, returns zeros and status 'UNKNOWN'.
    """
    try:
        # Try available fills methods on the SDK
        methods = [name for name in ("get_fills", "list_fills") if hasattr(client, name)]
        fills: List[Dict[str, Any]] = []
        last_err: Optional[Exception] = None

        for m in methods:
            try:
                resp = getattr(client, m)(order_id=order_id)
                data = resp.to_dict() if hasattr(resp, "to_dict") else resp
                arr = None
                if isinstance(data, dict):
                    # Common shapes: {"fills":[...]} or {"data":[...]} or {"results":[...]}
                    arr = data.get("fills") or data.get("data") or data.get("results")
                if isinstance(arr, list):
                    fills = arr
                    break
            except Exception as e:
                last_err = e
                continue

        # If no fills were returned, report UNKNOWN with zeros
        if not fills:
            return {
                "order_id": order_id,
                "status": "UNKNOWN",
                "filled_size": "0",
                "average_filled_price": "0",
                "commission_total_usd": "0"
            }

        total_base = Decimal("0")
        total_notional = Decimal("0")
        total_commission = Decimal("0")
        status = "UNKNOWN"

        for f in fills:
            # price
            price_str = str(f.get("price") or f.get("average_filled_price") or "0")
            try:
                price = Decimal(price_str)
            except Exception:
                price = Decimal("0")

            # size may be base or quote; Coinbase provides a size_in_quote flag
            size_str = str(f.get("size") or f.get("filled_size") or "0")
            try:
                size = Decimal(size_str)
            except Exception:
                size = Decimal("0")

            # commission (USD)
            commission_str = str(f.get("commission") or "0")
            try:
                commission = Decimal(commission_str)
            except Exception:
                commission = Decimal("0")
            total_commission += commission

            size_in_quote = bool(f.get("size_in_quote"))
            if size_in_quote:
                base = (size / price) if price > 0 else Decimal("0")
                notional = size
            else:
                base = size
                notional = size * price

            total_base += base
            total_notional += notional

            st = f.get("order_status") or f.get("status")
            if isinstance(st, str) and st:
                status = st

        avg_price = (total_notional / total_base) if total_base > 0 else Decimal("0")

        return {
            "order_id": order_id,
            "status": status,
            "filled_size": format(total_base, "f"),              # BASE units
            "average_filled_price": format(avg_price, "f"),
            "commission_total_usd": format(total_commission, "f"),
        }
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

# === NEW: balances for base/quote (strings: asset, available, step) ===

def _product_symbols_and_steps(product_id: str):
    """
    Return (base, quote, base_step, quote_step) strings from the product payload.
    Any missing step returns "0" so the Go side can apply its env override.
    """
    p = client.get_product(product_id)
    if hasattr(p, "to_dict"):
        p = p.to_dict()
    base  = str(p.get("base_currency")  or p.get("base_currency_id")  or p.get("base")  or p.get("base_display_symbol")  or "")
    quote = str(p.get("quote_currency") or p.get("quote_currency_id") or p.get("quote") or p.get("quote_display_symbol") or "")
    base_step  = str(p.get("base_increment")  or p.get("base_increment_value")  or "0")
    quote_step = str(p.get("quote_increment") or p.get("quote_increment_value") or "0")
    return base, quote, base_step, quote_step

def _sum_available(currency: str) -> str:
    """
    Sum available balances for a currency across all accounts; return as decimal string.
    Looks specifically at 'available_balance': {'currency': 'BTC', 'value': '...'}.
    """
    if not currency:
        return "0"
    try:
        resp = client.get_accounts(limit=200)
        data = resp.to_dict() if hasattr(resp, "to_dict") else resp
        accounts = data.get("accounts") or data.get("data") or []
        total = Decimal("0")
        for a in accounts:
            ab = a.get("available_balance") or {}
            if str(ab.get("currency") or "") == currency:
                try:
                    total += Decimal(str(ab.get("value") or "0"))
                except Exception:
                    pass
        return format(total, "f")
    except Exception:
        return "0"

@app.get("/balance/base")
def balance_base(product_id: str = Query(..., description="e.g., BTC-USD")):
    """
    Shape: {"asset":"BTC","available":"0.00000000","step":"0.00000001"}
    """
    try:
        base, _quote, base_step, _quote_step = _product_symbols_and_steps(product_id)
        if not base:
            raise RuntimeError("could not resolve base currency for product")
        return {"asset": base, "available": _sum_available(base), "step": base_step or "0"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

@app.get("/balance/quote")
def balance_quote(product_id: str = Query(..., description="e.g., BTC-USD")):
    """
    Shape: {"asset":"USD","available":"0.00","step":"0.01"}
    """
    try:
        _base, quote, _base_step, quote_step = _product_symbols_and_steps(product_id)
        if not quote:
            raise RuntimeError("could not resolve quote currency for product")
        return {"asset": quote, "available": _sum_available(quote), "step": quote_step or "0"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

}}

============================================================================================================
==============================================================================================================

trader.go{{
// FILE: trader.go
// Package main – Position/risk management and the synchronized trading loop.
//
// What’s here:
//   • Position state (open price/side/size/stop/take)
//   • Trader: holds config, broker, model, equity/PnL, and mutex
//   • step(): the core synchronized tick that may OPEN, HOLD, or EXIT
//
// Concurrency design:
//   - We take the trader mutex to read/update in-memory state,
//     but RELEASE the lock around any network I/O (placing orders,
//     fetching prices via the broker). That prevents stalls/blocking.
//   - On EXIT, we actually place a closing market order (unless DryRun).
//
// Safety:
//   - Daily circuit breaker: MaxDailyLossPct
//   - Long-only guard (Config.LongOnly): prevents new SELL entries on spot
//   - OrderMinUSD floor and proportional risk per trade

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---- Position & Trader ----

type Position struct {
	OpenPrice float64
	Side      OrderSide
	SizeBase  float64
	Stop      float64
	Take      float64
	OpenTime  time.Time
	// --- NEW: record entry fee for later P/L adjustment ---
	EntryFee float64

	// --- NEW (runner-only trailing fields; used only when this lot is the runner) ---
	TrailActive bool    // becomes true after TRAIL_ACTIVATE_PCT threshold
	TrailPeak   float64 // best favorable price since activation (peak for long; trough for short)
	TrailStop   float64 // current trailing stop level derived from TrailPeak and TRAIL_DISTANCE_PCT

	// --- NEW: human-readable gates/why string captured at entry time ---
	Reason string `json:"reason,omitempty"`
}

// BotState is the persistent snapshot of trader state.
type BotState struct {
	EquityUSD      float64
	DailyStart     time.Time
	DailyPnL       float64
	Lots           []*Position
	Model          *AIMicroModel
	MdlExt         *ExtendedLogit
	WalkForwardMin int
	LastFit        time.Time
	LastAdd           time.Time
	WinLow            float64
	LatchedGate       float64
	WinHigh           float64
	LatchedGateShort  float64
}

type Trader struct {
	cfg        Config
	broker     Broker
	model      *AIMicroModel
	pos        *Position   // kept for backward compatibility with earlier logic
	lots       []*Position // NEW: multiple lots when pyramiding is enabled
	lastAdd    time.Time   // NEW: last time a pyramid add was placed
	dailyStart time.Time
	dailyPnL   float64
	mu         sync.Mutex
	equityUSD  float64

	// NEW (minimal): optional extended head passed through to decide(); nil if unused.
	mdlExt *ExtendedLogit

	// NEW: path to persisted state file
	stateFile string

	// NEW: track last model fit time for walk-forward
	lastFit time.Time

	// NEW: index of the designated runner lot (-1 if none). Not persisted; derived on load.
	runnerIdx int

	// NEW: independent pyramiding anchor (stable reference, not tied to latest scalp)
	pyramidAnchorPrice float64
	pyramidAnchorTime  time.Time

	// --- NEW: adverse gate helpers (since last add) ---
	// BUY path trackers:
	winLow      float64 // lowest price seen since lastAdd (BUY adverse tracking)
	latchedGate float64 // latched adverse gate once threshold time is reached; reset on new add
	// SELL path trackers (NEW):
	winHigh           float64 // highest price seen since lastAdd (SELL adverse tracking)
	latchedGateShort  float64 // latched adverse gate for SELL once threshold time is reached; reset on new add
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,
		runnerIdx:  -1,
	}

	// Persistence guard: backtests set PERSIST_STATE=false
	persist := getEnvBool("PERSIST_STATE", true)
	if !persist {
		// Disable persistence hard by clearing the path.
		t.stateFile = ""
		log.Printf("[INFO] persistence disabled (PERSIST_STATE=false); starting fresh state")
	} else {
		// Try to load state if enabled
		if err := t.loadState(); err == nil {
			log.Printf("[INFO] trader state restored from %s", t.stateFile)
		} else {
			log.Printf("[INFO] no prior state restored: %v", err)
			// >>> FAIL-FAST (requested): if live (not DryRun) and persistence is expected,
			// and the state path isn't a mounted/writable volume, abort with a clear message.
			if !t.cfg.DryRun && shouldFatalNoStateMount(t.stateFile) {
				log.Fatalf("[FATAL] persistence required but state path is not a mounted volume or not writable: STATE_FILE=%s ; "+
					"mount /opt/coinbase/state into the container and ensure it's writable. "+
					"Example docker-compose:\n  volumes:\n    - /opt/coinbase/state:/opt/coinbase/state",
					t.stateFile)
			}
		}
	}
	// If state has existing lots but no runner assigned (fresh field), default runner to the oldest or 0.
	if t.runnerIdx == -1 && len(t.lots) > 0 {
		t.runnerIdx = 0
	}
	return t
}

func (t *Trader) EquityUSD() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.equityUSD
}

// SetEquityUSD safely updates trader equity and the equity metric.
func (t *Trader) SetEquityUSD(v float64) {
	t.mu.Lock()
	t.equityUSD = v
	t.mu.Unlock()

	// update the metric with same naming style
	mtxPnL.Set(v)
	// persist new state (no-op if disabled)
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		
	}
}

// NEW (minimal): allow live loop to inject/refresh the optional extended model.
func (t *Trader) SetExtendedModel(m *ExtendedLogit) {
	t.mu.Lock()
	t.mdlExt = m
	t.mu.Unlock()
}

func midnightUTC(ts time.Time) time.Time {
	y, m, d := ts.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (t *Trader) updateDaily(date time.Time) {
	if midnightUTC(date) != t.dailyStart {
		t.dailyStart = midnightUTC(date)
		t.dailyPnL = 0
		if err := t.saveState(); err != nil {
			log.Printf("[WARN] saveState: %v", err)
		}
	}
}

// ---- helpers for pyramiding ----

func allowPyramiding() bool {
	return getEnvBool("ALLOW_PYRAMIDING", false)
}
func pyramidMinSeconds() int {
	return getEnvInt("PYRAMID_MIN_SECONDS_BETWEEN", 0)
}
func pyramidMinAdversePct() float64 {
	return getEnvFloat("PYRAMID_MIN_ADVERSE_PCT", 0.0) // 0 = no adverse-move requirement
}
func scalpTPDecayEnabled() bool   { return getEnvBool("SCALP_TP_DECAY_ENABLE", false) }
func scalpTPDecayMode() string    { return getEnv("SCALP_TP_DEC_MODE", "linear") }
func scalpTPDecPct() float64      { return getEnvFloat("SCALP_TP_DEC_PCT", 0.0) }      // % points
func scalpTPDecayFactor() float64 { return getEnvFloat("SCALP_TP_DECAY_FACTOR", 1.0) } // multiplicative
func scalpTPMinPct() float64      { return getEnvFloat("SCALP_TP_MIN_PCT", 0.0) }      // floor

// --- NEW: Option A – time-based exponential decay knobs (0 disables) ---
func pyramidDecayLambda() float64 { return getEnvFloat("PYRAMID_DECAY_LAMBDA", 0.0) }  // per-minute
func pyramidDecayMinPct() float64 { return getEnvFloat("PYRAMID_DECAY_MIN_PCT", 0.0) } // floor

// Cap concurrent lots (env-tunable). Default is effectively "no cap".
func maxConcurrentLots() int {
	n := getEnvInt("MAX_CONCURRENT_LOTS", 1_000_000)
	if n < 1 {
		n = 1_000_000 // safety: never block adds due to bad input
	}
	return n
}

// Spot SELL guard and paper overrides
func requireBaseForShort() bool { return getEnvBool("REQUIRE_BASE_FOR_SHORT", true) }
func paperBaseBalance() float64 { return getEnvFloat("PAPER_BASE_BALANCE", 0.0) }
func baseAssetOverride() string { return getEnv("BASE_ASSET", "") }
func baseStepOverride() float64 { return getEnvFloat("BASE_STEP", 0.0) } // 0 => unknown

// --- NEW: backtest-only quote balance helpers (BUY gating symmetry) ---
func paperQuoteBalance() float64 { return getEnvFloat("PAPER_QUOTE_BALANCE", 0.0) }
func quoteStepOverride() float64 { return getEnvFloat("QUOTE_STEP", 0.0) } // 0 => unknown

// Runner tuning (internal, no new env keys): runner takes profit farther, same stop by default.
const runnerTPMult = 2.0
const runnerStopMult = 1.0

// Minimal "runner gap" guard (disabled)
const runnerMinGapPct = 0.0

// --- NEW: runner-only trailing env tunables (0 disables) ---
func trailActivatePct() float64 {
	return getEnvFloat("TRAIL_ACTIVATE_PCT", 0.0)
}
func trailDistancePct() float64 {
	return getEnvFloat("TRAIL_DISTANCE_PCT", 0.0)
}

// latestEntry returns the most recent long lot entry price, or 0 if none.
func (t *Trader) latestEntry() float64 {
	if len(t.lots) == 0 {
		return 0
	}
	return t.lots[len(t.lots)-1].OpenPrice
}

// aggregateOpen sets t.pos to the latest lot (for legacy reads) or nil.
func (t *Trader) aggregateOpen() {
	if len(t.lots) == 0 {
		t.pos = nil
		return
	}
	// keep last lot as representative for legacy checks
	t.pos = t.lots[len(t.lots)-1]
}

// applyRunnerTargets adjusts stop/take for the designated runner lot.
func (t *Trader) applyRunnerTargets(p *Position) {
	if p == nil {
		return
	}
	op := p.OpenPrice
	if p.Side == SideBuy {
		p.Stop = op * (1.0 - (t.cfg.StopLossPct*runnerStopMult)/100.0)
		p.Take = op * (1.0 + (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
	} else {
		p.Stop = op * (1.0 + (t.cfg.StopLossPct*runnerStopMult)/100.0)
		p.Take = op * (1.0 - (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
	}
}

// --- NEW: runner trailing updater (no-ops if env not set or lot not runner).
// Returns (shouldExit, newTrailStopIfAny).
func (t *Trader) updateRunnerTrail(lot *Position, price float64) (bool, float64) {
	if lot == nil {
		return false, 0
	}
	act := trailActivatePct()
	dist := trailDistancePct()
	if act <= 0 || dist <= 0 {
		return false, 0
	}

	switch lot.Side {
	case SideBuy:
		activateAt := lot.OpenPrice * (1.0 + act/100.0)
		if !lot.TrailActive {
			if price >= activateAt {
				lot.TrailActive = true
				lot.TrailPeak = price
				lot.TrailStop = price * (1.0 - dist/100.0)
			}
		} else {
			if price > lot.TrailPeak {
				lot.TrailPeak = price
				ts := lot.TrailPeak * (1.0 - dist/100.0)
				if ts > lot.TrailStop {
					lot.TrailStop = ts
				}
			}
			if price <= lot.TrailStop && lot.TrailStop > 0 {
				return true, lot.TrailStop
			}
		}
	case SideSell:
		activateAt := lot.OpenPrice * (1.0 - act/100.0)
		if !lot.TrailActive {
			if price <= activateAt {
				lot.TrailActive = true
				lot.TrailPeak = price // trough for short
				lot.TrailStop = price * (1.0 + dist/100.0)
			}
		} else {
			if price < lot.TrailPeak {
				lot.TrailPeak = price
				lot.TrailStop = lot.TrailPeak * (1.0 + dist/100.0)
			}
			if price >= lot.TrailStop && lot.TrailStop > 0 {
				return true, lot.TrailStop
			}
		}
	}
	return false, lot.TrailStop
}

// closeLotAtIndex closes a single lot at idx (assumes mutex held), performing I/O unlocked.
// exitReason is a short label for logs: "take_profit" | "stop_loss" | "trailing_stop" (or other).
func (t *Trader) closeLotAtIndex(ctx context.Context, c []Candle, idx int, exitReason string) (string, error) {
	price := c[len(c)-1].Close
	lot := t.lots[idx]
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}
	baseRequested := lot.SizeBase
	quote := baseRequested * price

	// unlock for I/O
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote)
		if err != nil {
			if t.cfg.Extended().UseDirectSlack {
				postSlack(fmt.Sprintf("ERR step: %v", err))
			}
			// Re-lock before returning so caller's Unlock matches.
			t.mu.Lock()
			return "", fmt.Errorf("close order failed: %w", err)
		}
		mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
	}
	// re-lock
	t.mu.Lock()

	// --- NEW: check if the lot being closed is the most recent add (newest OpenTime) ---
	wasNewest := true
	refOpen := lot.OpenTime
	for j := range t.lots {
		if j == idx {
			continue
		}
		if !t.lots[j].OpenTime.IsZero() && t.lots[j].OpenTime.After(refOpen) {
			wasNewest = false
			break
		}
	}

	// --- MINIMAL CHANGE: use actual filled size/price if available ---
	priceExec := c[len(c)-1].Close
	baseFilled := baseRequested
	if placed != nil {
		if placed.Price > 0 {
			priceExec = placed.Price
		}
		if placed.BaseSize > 0 {
			baseFilled = placed.BaseSize
		}
		// Log WARN on partial fill (filled < requested) with a small tolerance.
		const tol = 1e-9
		if baseFilled+tol < baseRequested {
			log.Printf("[WARN] partial fill (exit): requested_base=%.8f filled_base=%.8f (%.2f%%)",
				baseRequested, baseFilled, 100.0*(baseFilled/baseRequested))
		}
	}
	// refresh price snapshot (best-effort) if no execution price was available
	if placed == nil || placed.Price <= 0 {
		priceExec = c[len(c)-1].Close
	}

	// compute P/L using actual fill size and execution price
	pl := (priceExec - lot.OpenPrice) * baseFilled
	if lot.Side == SideSell {
		pl = (lot.OpenPrice - priceExec) * baseFilled
	}

	// apply exit fee; prefer broker-provided commission if present ---
	quoteExec := baseFilled * priceExec
	feeRate := t.cfg.FeeRatePct
	exitFee := quoteExec * (feeRate / 100.0)
	if placed != nil {
		if placed.CommissionUSD > 0 {
			exitFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (exit); falling back to FEE_RATE_PCT=%.4f%%", feeRate)
		}
	}
	pl -= lot.EntryFee // subtract entry fee recorded
	pl -= exitFee      // subtract exit fee now

	t.dailyPnL += pl
	t.equityUSD += pl

	// --- NEW: increment win/loss trades ---
	if pl >= 0 {
		mtxTrades.WithLabelValues("win").Inc()
	} else {
		mtxTrades.WithLabelValues("loss").Inc()
	}

	// Track if we removed the runner and adjust runnerIdx accordingly after removal.
	removedWasRunner := (idx == t.runnerIdx)

	// remove lot idx
	t.lots = append(t.lots[:idx], t.lots[idx+1:]...)

	// shift runnerIdx if needed
	if t.runnerIdx >= 0 {
		if idx < t.runnerIdx {
			t.runnerIdx-- // slice shifted left
		} else if idx == t.runnerIdx {
			// runner removed; promote the NEWEST remaining lot (if any) to runner
			if len(t.lots) > 0 {
				t.runnerIdx = len(t.lots) - 1
				// reset trailing fields for the newly promoted runner
				nr := t.lots[t.runnerIdx]
				nr.TrailActive = false
				nr.TrailPeak = nr.OpenPrice
				nr.TrailStop = 0
				// also re-apply runner targets (keeps existing behavior)
				t.applyRunnerTargets(nr)
			} else {
				t.runnerIdx = -1
			}
		}
	}

	// --- if the closed lot was the most recent add, re-anchor pyramiding timers/state ---
	if wasNewest {
		// If any lots remain, restart the decay clock from now to avoid instant latch.
		// If none remain, also set now; next add will proceed normally.
		t.lastAdd = time.Now().UTC()
		// Reset adverse tracking; winLow/winHigh will start accumulating after t_floor_min,
		// and latching can only occur after 2*t_floor_min from this new anchor.
		t.winLow = 0
		t.latchedGate = 0
		t.winHigh = 0
		t.latchedGateShort = 0
	}

	t.aggregateOpen()
	// Include reason in message for operator visibility
	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, lot.EntryFee+exitFee)
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
	}

	_ = removedWasRunner // kept to emphasize runner path; no extra logs.
	return msg, nil
}

// ---- Core tick ----

// step consumes the current candle history and may place/close a position.
// It returns a human-readable status string for logging.
func (t *Trader) step(ctx context.Context, c []Candle) (string, error) {
	if len(c) == 0 {
		return "NO_DATA", nil
	}

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	// Use wall clock as authoritative "now" for pyramiding timings; fall back for zero candle time.
	wallNow := time.Now().UTC()

	now := c[len(c)-1].Time
	if now.IsZero() {
		now = wallNow
	}
	t.updateDaily(now)

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	// Any refit logic must first check shouldRefit(len(c)).
	// This preserves restored weights when history is thin.
	_ = t.shouldRefit(len(c)) // intentionally unused here (guard only)

	// Keep paper broker price in sync with the latest close so paper fills are realistic.
	if pb, ok := t.broker.(*PaperBroker); ok {
		if len(c) > 0 {
			pb.mu.Lock()
			pb.price = c[len(c)-1].Close
			pb.mu.Unlock()
		}
	}

	// --- EXIT path: if any lots are open, evaluate TP/SL for each and close those that trigger.
	if len(t.lots) > 0 {
		price := c[len(c)-1].Close
		nearestStop := 0.0
		nearestTake := 0.0
		for i := 0; i < len(t.lots); {
			lot := t.lots[i]

			// --- NEW: runner-only trailing exit check (wired alongside TP/SL) ---
			if i == t.runnerIdx {
				if trigger, tstop := t.updateRunnerTrail(lot, price); trigger {
					// reflect trailing level for visibility in debug/Slack
					lot.Stop = tstop
					msg, err := t.closeLotAtIndex(ctx, c, i, "trailing_stop")
					if err != nil {
						t.mu.Unlock()
						return "", err
					}
					// closeLotAtIndex removed index i; continue without i++
					t.mu.Unlock()
					return msg, nil
				}
			}

			trigger := false
			exitReason := ""
			if lot.Side == SideBuy && (price <= lot.Stop || price >= lot.Take) {
				trigger = true
				if price <= lot.Stop {
					exitReason = "stop_loss"
				} else {
					exitReason = "take_profit"
				}
			}
			if lot.Side == SideSell && (price >= lot.Stop || price <= lot.Take) {
				trigger = true
				if price >= lot.Stop {
					exitReason = "stop_loss"
				} else {
					exitReason = "take_profit"
				}
			}
			if trigger {
				msg, err := t.closeLotAtIndex(ctx, c, i, exitReason)
				if err != nil {
					t.mu.Unlock()
					return "", err
				}
				// closeLotAtIndex removed index i; continue without i++
				t.mu.Unlock()
				return msg, nil
			}

			if lot.Side == SideBuy {
				if nearestStop == 0 || lot.Stop > nearestStop { // highest stop for long
					nearestStop = lot.Stop
				}
				if nearestTake == 0 || lot.Take < nearestTake { // lowest take for long
					nearestTake = lot.Take
				}
			} else { // SideSell
				if nearestStop == 0 || lot.Stop < nearestStop { // lowest stop for short
					nearestStop = lot.Stop
				}
				if nearestTake == 0 || lot.Take > nearestTake { // highest take for short
					nearestTake = lot.Take
				}
			}

			i++ // no trigger; move to next
		}
		log.Printf("[DEBUG] nearest stop=%.2f take=%.2f across %d lots", nearestStop, nearestTake, len(t.lots))
	}

	d := decide(c, t.model, t.mdlExt)
	log.Printf("[DEBUG] Lots=%d, Decision=%s Reason = %s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v", len(t.lots), d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)

	// Ignore discretionary SELL signals while lots are open; exits are TP/SL only.
	// if len(t.lots) > 0 && d.Signal == Sell {
	// 	t.mu.Unlock()
	// 	return "HOLD", nil
	// }

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	price := c[len(c)-1].Close

	// --- NEW: track lowest price since last add (BUY path) and highest price (SELL path) ---
	if !t.lastAdd.IsZero() {
		if t.winLow == 0 || price < t.winLow {
			t.winLow = price
		}
		if t.winHigh == 0 || price > t.winHigh {
			t.winHigh = price
		}
	}

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}
	if d.Signal == Flat {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT [%s]", d.Reason), nil
	}

	// Respect lot cap (both sides)
	if len(t.lots) >= maxConcurrentLots() {
		t.mu.Unlock()
		log.Printf("[DEBUG] lot cap reached (%d); HOLD", maxConcurrentLots())
		return "HOLD", nil
	}

	// Determine if we are opening first lot or attempting a pyramid add.
	// --- CHANGED: enable SELL pyramiding symmetry ---
	isAdd := len(t.lots) > 0 && allowPyramiding() && (d.Signal == Buy || d.Signal == Sell)

	// --- NEW: variables to capture gate audit fields for the reason string (side-biased; no winLow) ---
	var (
		reasonGatePrice float64
		reasonLatched   float64
		reasonEffPct    float64
		reasonBasePct   float64
		reasonElapsedHr float64
	)

	// Gating for pyramiding adds — spacing + adverse move (with optional time-decay).
	if isAdd {
		// 1) Spacing: always enforce (s=0 means no wait; set >0 to require time gap)
		s := pyramidMinSeconds()
		if time.Since(t.lastAdd) < time.Duration(s)*time.Second {
			t.mu.Unlock()
			hrs := time.Since(t.lastAdd).Hours()
			log.Printf("[DEBUG] pyramid: blocked by spacing; since_last=%vHours need>=%ds", fmt.Sprintf("%.1f", hrs), s)
			return "HOLD", nil
		}

		// 2) Adverse move gate with optional time-based exponential decay.
		basePct := pyramidMinAdversePct()
		effPct := basePct
		lambda := pyramidDecayLambda()
		floor := pyramidDecayMinPct()
		elapsedMin := 0.0
		if lambda > 0 {
			if !t.lastAdd.IsZero() {
				elapsedMin = time.Since(t.lastAdd).Minutes()
			} else {
				elapsedMin = 0.0
			}
			decayed := basePct * math.Exp(-lambda*elapsedMin)
			if decayed < floor {
				decayed = floor
			}
			effPct = decayed
		}

		// Capture for reason string
		reasonBasePct = basePct
		reasonEffPct = effPct
		reasonElapsedHr = elapsedMin / 60.0

		// Time (in minutes) to hit the floor once (t_floor_min); used for latching thresholds.
		tFloorMin := 0.0
		if lambda > 0 && basePct > floor {
			tFloorMin = math.Log(basePct/floor) / lambda
		}

		last := t.latestEntry()
		if last > 0 {
			if d.Signal == Buy {
				// BUY adverse tracker
				if elapsedMin >= tFloorMin {
					if t.winLow == 0 || price < t.winLow {
						t.winLow = price
					}
				} else {
					t.winLow = 0
				}
				// latch at 2*t_floor_min
				if t.latchedGate == 0 && elapsedMin >= 2.0*tFloorMin && t.winLow > 0 {
					t.latchedGate = t.winLow
				}
				// baseline gate: last * (1 - effPct); latched replaces baseline
				gatePrice := last * (1.0 - effPct/100.0)
				if t.latchedGate > 0 {
					gatePrice = t.latchedGate
				}
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGate

				if !(price <= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (BUY); price=%.2f last_gate<=%.2f win_low=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winLow, effPct, basePct, reasonElapsedHr)
					return "HOLD", nil
				}
			} else { // SELL
				// SELL adverse tracker
				if elapsedMin >= tFloorMin {
					if t.winHigh == 0 || price > t.winHigh {
						t.winHigh = price
					}
				} else {
					t.winHigh = 0
				}
				if t.latchedGateShort == 0 && elapsedMin >= 2.0*tFloorMin && t.winHigh > 0 {
					t.latchedGateShort = t.winHigh
				}
				// baseline gate: last * (1 + effPct); latched replaces baseline
				gatePrice := last * (1.0 + effPct/100.0)
				if t.latchedGateShort > 0 {
					gatePrice = t.latchedGateShort
				}
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateShort

				if !(price >= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (SELL); price=%.2f last_gate>=%.2f win_high=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winHigh, effPct, basePct, reasonElapsedHr)
					return "HOLD", nil
				}
			}
		}
	}

	// Sizing (risk % of current equity, with optional volatility adjust already supported).
	riskPct := t.cfg.RiskPerTradePct
	if t.cfg.Extended().VolRiskAdjust {
		f := volRiskFactor(c)
		riskPct = riskPct * f
		SetVolRiskFactorMetric(f)
	}
	quote := (riskPct / 100.0) * t.equityUSD
	if quote < t.cfg.OrderMinUSD {
		quote = t.cfg.OrderMinUSD
	}
	base := quote / price
	side := d.SignalToSide()

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// Reserve quote needed to close all existing short lots at current price.
		var reservedShortQuote float64
		for _, lot := range t.lots {
			if lot.Side == SideSell {
				reservedShortQuote += lot.SizeBase * price
			}
		}

		// Ask broker for quote balance/step (uniform: live & DryRun).
		sym, aq, qs, err := t.broker.GetAvailableQuote(ctx, t.cfg.ProductID)
		if err != nil || strings.TrimSpace(sym) == "" {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: GetAvailableQuote failed: %v", err)
		}
		availQuote := aq
		qstep := qs
		if qstep <= 0 {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: missing/invalid QUOTE step for %s (step=%.8f)", t.cfg.ProductID, qstep)
		}

		// Floor the needed quote to step.
		neededQuote := quote
		if qstep > 0 {
			n := math.Floor(neededQuote/qstep) * qstep
			if n > 0 {
				neededQuote = n
			}
		}

		spare := availQuote - reservedShortQuote
		if spare < 0 {
			spare = 0
		}
		if spare+spareEps < neededQuote {
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE BUY: need=%.2f quote, spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
				neededQuote, spare, availQuote, reservedShortQuote, qstep)
			return "HOLD", nil
		}

		// Enforce exchange minimum notional after snapping, then snap UP to step to keep >= min; re-check spare.
		if neededQuote < t.cfg.OrderMinUSD {
			neededQuote = t.cfg.OrderMinUSD
			if qstep > 0 {
				steps := math.Ceil(neededQuote / qstep)
				neededQuote = steps * qstep
			}
			if spare+spareEps < neededQuote {
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE BUY: need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
					neededQuote, spare, availQuote, reservedShortQuote, qstep)
				return "HOLD", nil
			}
		}

		// Use the final neededQuote; recompute base.
		quote = neededQuote
		base = quote / price
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && requireBaseForShort() {
		// Sum reserved base for long lots
		var reservedLong float64
		for _, lot := range t.lots {
			if lot.Side == SideBuy {
				reservedLong += lot.SizeBase
			}
		}

		// Ask broker for base balance/step (uniform: live & DryRun).
		sym, ab, stp, err := t.broker.GetAvailableBase(ctx, t.cfg.ProductID)
		if err != nil || strings.TrimSpace(sym) == "" {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: GetAvailableBase failed: %v", err)
		}
		availBase := ab
		step := stp
		if step <= 0 {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: missing/invalid BASE step for %s (step=%.8f)", t.cfg.ProductID, step)
		}

		// Floor the *needed* base to step (if known) and cap by spare availability
		neededBase := base
		if step > 0 {
			n := math.Floor(neededBase/step) * step
			if n > 0 {
				neededBase = n
			}
		}
		spare := availBase - reservedLong
		if spare < 0 {
			spare = 0
		}
		if spare+spareEps < neededBase {
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE SELL: need=%.8f base, spare=%.8f (avail=%.8f, reserved_longs=%.8f, step=%.8f)",
				neededBase, spare, availBase, reservedLong, step)
			return "HOLD", nil
		}

		// Use the floored base for the order by updating quote
		quote = neededBase * price
		base = neededBase

		// Ensure SELL meets exchange min funds and step rules (and re-check spare symmetry)
		if quote < t.cfg.OrderMinUSD {
			quote = t.cfg.OrderMinUSD
			base = quote / price
			if step > 0 {
				b := math.Floor(base/step) * step
				if b > 0 {
					base = b
					quote = base * price
				}
			}
			// >>> Symmetry: re-check spare after min-notional snap <<<
			if spare+spareEps < base {
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE SELL: need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, step=%.8f)",
					base, spare, availBase, reservedLong, step)
				return "HOLD", nil
			}
		}
	}

	// Stops/takes (baseline for scalps)
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	if side == SideSell {
		stop = price * (1.0 + t.cfg.StopLossPct/100.0)
		take = price * (1.0 - t.cfg.TakeProfitPct/100.0)
	}

	// Decide if this new entry will be the runner (only when there is no existing runner).
	willBeRunner := (t.runnerIdx == -1 && len(t.lots) == 0)
	if willBeRunner {
		// Stretch runner targets without introducing new env keys.
		if side == SideBuy {
			stop = price * (1.0 - (t.cfg.StopLossPct*runnerStopMult)/100.0)
			take = price * (1.0 + (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
		} else {
			stop = price * (1.0 + (t.cfg.StopLossPct*runnerStopMult)/100.0)
			take = price * (1.0 - (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
		}
	} else if scalpTPDecayEnabled() {
		// This is a scalp add: compute k = number of existing scalps
		k := len(t.lots)
		if t.runnerIdx >= 0 && t.runnerIdx < len(t.lots) {
			k = len(t.lots) - 1 // exclude the runner from the scalp index
		}
		baseTP := t.cfg.TakeProfitPct
		tpPct := baseTP

		switch scalpTPDecayMode() {
		case "exp", "exponential":
			// geometric decay: baseTP * factor^k, floored
			f := scalpTPDecayFactor()
			if f <= 0 {
				f = 1.0
			}
			factorPow := 1.0
			for i := 0; i < k; i++ {
				factorPow *= f
			}
			tpPct = baseTP * factorPow
		default:
			// linear: baseTP - k * decPct, floored
			dec := scalpTPDecPct()
			tpPct = baseTP - float64(k)*dec
		}

		minTP := scalpTPMinPct()
		if tpPct < minTP {
			tpPct = minTP
		}

		// apply the (possibly reduced) TP for the scalp only
		if side == SideBuy {
			take = price * (1.0 + tpPct/100.0)
		} else {
			take = price * (1.0 - tpPct/100.0)
		}

		// >>> DEBUG LOG <<<
		log.Printf("[DEBUG] scalp tp decay: k=%d mode=%s baseTP=%.3f%% tpPct=%.3f%% minTP=%.3f%% take=%.2f",
			k, scalpTPDecayMode(), t.cfg.TakeProfitPct, tpPct, minTP, take)
	}

	// --- apply entry fee (preliminary; may be replaced by broker-provided commission below) ---
	feeRate := t.cfg.FeeRatePct
	entryFee := quote * (feeRate / 100.0)
	if t.cfg.DryRun {
		t.equityUSD -= entryFee
	}

	// Place live order without holding the lock.
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
		if err != nil {
			// Retry once with ORDER_MIN_USD on insufficient-funds style failures.
			e := strings.ToLower(err.Error())
			if quote > t.cfg.OrderMinUSD && (strings.Contains(e, "insufficient") || strings.Contains(e, "funds") || strings.Contains(e, "400")) {
				log.Printf("[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f", quote, err, t.cfg.OrderMinUSD)
				quote = t.cfg.OrderMinUSD
				base = quote / price
				placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
			}
			if err != nil {
				if t.cfg.Extended().UseDirectSlack {
					postSlack(fmt.Sprintf("ERR step: %v", err))
				}
				return "", err
			}
		}
		mtxOrders.WithLabelValues("live", string(side)).Inc()
		mtxTrades.WithLabelValues("open").Inc()
	} else {
		mtxTrades.WithLabelValues("open").Inc()
	}

	// Re-lock to mutate state (append new lot or first lot).
	t.mu.Lock()

	// --- MINIMAL CHANGE: use actual filled size/price when available ---
	priceToUse := price
	baseRequested := base
	baseToUse := baseRequested
	actualQuote := quote

	if placed != nil {
		if placed.Price > 0 {
			priceToUse = placed.Price
		}
		if placed.BaseSize > 0 {
			baseToUse = placed.BaseSize
		}
		if placed.QuoteSpent > 0 {
			actualQuote = placed.QuoteSpent
		}
		// Log WARN on partial fill (filled < requested) with a small tolerance.
		const tol = 1e-9
		if baseToUse+tol < baseRequested {
			log.Printf("[WARN] partial fill: requested_base=%.8f filled_base=%.8f (%.2f%%)",
				baseRequested, baseToUse, 100.0*(baseToUse/baseRequested))
		}
	}

	// Prefer broker-provided commission for entry if present; otherwise fallback to FEE_RATE_PCT.
	if placed != nil {
		if placed.CommissionUSD > 0 {
			entryFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (entry); falling back to FEE_RATE_PCT=%.4f%%", feeRate)
			entryFee = actualQuote * (feeRate / 100.0)
		}
	} else {
		// DryRun path keeps previously computed entryFee and adjusts by delta as before.
	}

	if t.cfg.DryRun {
		// already deducted above for DryRun using quote; adjust to the actualQuote delta
		delta := (actualQuote - quote) * (feeRate / 100.0)
		t.equityUSD -= delta
	}

	// --- NEW: side-biased Lot reason (without winLow) ---
	var gatesReason string
	if side == SideBuy {
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|PriceDownGoingUp=%v|LowBottom=%v",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr,
			d.PriceDownGoingUp, d.LowBottom,
		)
	} else { // SideSell
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|HighPeak=%v|PriceUpGoingDown=%v",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr,
			d.HighPeak, d.PriceUpGoingDown,
		)
	}

	newLot := &Position{
		OpenPrice: priceToUse,
		Side:      side,
		SizeBase:  baseToUse,
		Stop:      stop,
		Take:      take,
		OpenTime:  now,
		EntryFee:  entryFee,
		Reason:    gatesReason, // side-biased; no winLow
		// trailing fields default zero/false; they’ll be initialized if this becomes runner
	}
	t.lots = append(t.lots, newLot)
	// Use wall clock for lastAdd to drive spacing/decay even if candle time is zero.
	t.lastAdd = wallNow
	// Reset adverse tracking for the new add.
	t.winLow = priceToUse
	t.latchedGate = 0
	t.winHigh = priceToUse
	t.latchedGateShort = 0

	// Assign/designate runner if none exists yet; otherwise this is a scalp.
	if t.runnerIdx == -1 {
		t.runnerIdx = len(t.lots) - 1 // the just-added lot is runner
		// Initialize runner trailing baseline
		r := t.lots[t.runnerIdx]
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		// Ensure runner's stretched targets are applied (keeps baseline behavior for runner).
		t.applyRunnerTargets(r)
	}

	t.aggregateOpen()

	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f stop=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			d.Signal, actualQuote, baseToUse, newLot.Stop, newLot.Take, entryFee, newLot.Reason, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s quote=%.2f stop=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			d.Signal, actualQuote, newLot.Stop, newLot.Take, entryFee, newLot.Reason, d.Reason)
	}
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
	}
	t.mu.Unlock()
	return msg, nil
}

// ---- labels ----

func signalLabel(s Signal) string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	default:
		return "flat"
	}
}

// ---- Persistence helpers ----

func (t *Trader) saveState() error {
	if t.stateFile == "" || !getEnvBool("PERSIST_STATE", true) {
		return nil
	}
	state := BotState{
		EquityUSD:      t.equityUSD,
		DailyStart:     t.dailyStart,
		DailyPnL:       t.dailyPnL,
		Lots:           t.lots,
		Model:          t.model,
		MdlExt:         t.mdlExt,
		WalkForwardMin: t.cfg.Extended().WalkForwardMin,
		LastFit:        t.lastFit,
		LastAdd:          t.lastAdd,
		WinLow:           t.winLow,
		LatchedGate:      t.latchedGate,
		WinHigh:          t.winHigh,
		LatchedGateShort: t.latchedGateShort,
	}
	bs, err := json.MarshalIndent(state, "", " ")
	if err != nil {
		return err
	}
	tmp := t.stateFile + ".tmp"
	if err := os.WriteFile(tmp, bs, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, t.stateFile)
}

func (t *Trader) loadState() error {
	if t.stateFile == "" || !getEnvBool("PERSIST_STATE", true) {
		return fmt.Errorf("no state file configured")
	}
	bs, err := os.ReadFile(t.stateFile)
	if err != nil {
		return err
	}
	var st BotState
	if err := json.Unmarshal(bs, &st); err != nil {
		return err
	}
	// Prefer configured/live equity rather than stale persisted equity when:
	//  - running in DRY_RUN / backtest, or
	//  - live-equity mode is enabled (we will rebase from Bridge when available).
	// This prevents negative/old EquityUSD from leaking into runs that don't want it.
	if !(t.cfg.DryRun || t.cfg.UseLiveEquity()) {
		t.equityUSD = st.EquityUSD
	} else {
		// keep t.equityUSD as initialized from cfg.USDEquity; live rebase will adjust later
	}
	t.dailyStart = st.DailyStart
	t.dailyPnL = st.DailyPnL
	t.lots = st.Lots
	if st.Model != nil {
		t.model = st.Model
	}
	if st.MdlExt != nil {
		t.mdlExt = st.MdlExt
	}
	if !st.LastFit.IsZero() {
		t.lastFit = st.LastFit
	}

	t.aggregateOpen()
	// Re-derive runnerIdx if not set (old state files won't carry it).
	if t.runnerIdx == -1 && len(t.lots) > 0 {
		t.runnerIdx = 0
		// Initialize trailing baseline for current runner if not already set
		r := t.lots[t.runnerIdx]
		if r.TrailPeak == 0 {
			// Initialize baseline to current open price if peak is unset.
			r.TrailPeak = r.OpenPrice
		}
	}

	// Restore pyramiding gate memory (if present in state file).
	t.lastAdd          = st.LastAdd
	t.winLow           = st.WinLow
	t.latchedGate      = st.LatchedGate
	t.winHigh          = st.WinHigh
	t.latchedGateShort = st.LatchedGateShort

	// --- Restart warmup for pyramiding decay/adverse tracking ---
	// If we restored with open lots but have no lastAdd, seed the decay clock to "now"
	// and reset adverse trackers/latches so they rebuild over real time (prevents instant latch).
	if len(t.lots) > 0 && t.lastAdd.IsZero() {
		t.lastAdd = time.Now().UTC()
		t.winLow = 0
		t.latchedGate = 0
		t.winHigh = 0
		t.latchedGateShort = 0
	}
	return nil
}

// ---- Phase-7 helpers ----

// postSlack sends a best-effort Slack webhook message if SLACK_WEBHOOK is set.
// No impact on baseline behavior or logging; errors are ignored.
func postSlack(msg string) {
	hook := getEnv("SLACK_WEBHOOK", "")
	if hook == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body := map[string]string{"text": msg}
	bs, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", hook, bytes.NewReader(bs))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	_, _ = http.DefaultClient.Do(req)
}

// volRiskFactor derives a multiplicative factor from recent relative volatility.
// Returns ~0.6–0.8 in high vol, ~1.0 normal, up to ~1.2 in very low vol.
func volRiskFactor(c []Candle) float64 {
	if len(c) < 40 {
		return 1.0
	}
	cl := make([]float64, len(c))
	for i := range c {
		cl[i] = c[i].Close
	}
	std20 := RollingStd(cl, 20)
	i := len(std20) - 1
	relVol := std20[i] / (cl[i] + 1e-12)
	switch {
	case relVol > 0.02:
		return 0.6
	case relVol > 0.01:
		return 0.8
	case relVol < 0.004:
		return 1.2
	default:
		return 1.0
	}
}

// ---- Refit guard (minimal, internal) ----

// shouldRefit returns true only when we allow a model (re)fit:
// 1) len(history) >= cfg.MaxHistoryCandles, and
// 2) optional walk-forward cadence satisfied (cfg.Extended().WalkForwardMin).
// This is a guard only; it performs no fitting and emits no logs/metrics.
func (t *Trader) shouldRefit(historyLen int) bool {
	if historyLen < t.cfg.MaxHistoryCandles {
		return false
	}
	min := t.cfg.Extended().WalkForwardMin
	if min <= 0 {
		return true
	}
	if t.lastFit.IsZero() {
		return true
	}
	return time.Since(t.lastFit) >= time.Duration(min)*time.Minute
}

// ---- Fail-fast helpers (startup state mount check) ----

// shouldFatalNoStateMount returns true when we expect persistence but the state file's
// parent directory is not a mounted volume or not writable. This prevents accidental
// flat-boot trading after CI/CD restarts when the host volume isn't mounted.
func shouldFatalNoStateMount(stateFile string) bool {
	stateFile = strings.TrimSpace(stateFile)
	if stateFile == "" {
		return false
	}
	dir := filepath.Dir(stateFile)

	// If the file already exists, don't fatal — persistence is working.
	if _, err := os.Stat(stateFile); err == nil {
		return false
	}

	// Ensure parent directory exists and is a directory.
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return true
	}

	// Ensure directory is writable.
	if f, err := os.CreateTemp(dir, "wtest-*.tmp"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
	} else {
		return true
	}

	// Ensure it's actually a mount point (host volume), not a container tmp dir.
	isMount, err := isMounted(dir)
	if err == nil && !isMount {
		return true
	}
	return false
}

// isMounted checks /proc/self/mountinfo to see if dir is a mount point.
func isMounted(dir string) (bool, error) {
	bs, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	dir = filepath.Clean(dir)
	for _, ln := range strings.Split(string(bs), "\n") {
		parts := strings.Split(ln, " ")
		if len(parts) < 5 {
			continue
		}
		mp := filepath.Clean(parts[4]) // mount point field
		if mp == dir {
			return true, nil
		}
	}
	return false, nil
}
}}
main.go{{
// FILE: trader.go
// Package main – Position/risk management and the synchronized trading loop.
//
// What’s here:
//   • Position state (open price/side/size/stop/take)
//   • Trader: holds config, broker, model, equity/PnL, and mutex
//   • step(): the core synchronized tick that may OPEN, HOLD, or EXIT
//
// Concurrency design:
//   - We take the trader mutex to read/update in-memory state,
//     but RELEASE the lock around any network I/O (placing orders,
//     fetching prices via the broker). That prevents stalls/blocking.
//   - On EXIT, we actually place a closing market order (unless DryRun).
//
// Safety:
//   - Daily circuit breaker: MaxDailyLossPct
//   - Long-only guard (Config.LongOnly): prevents new SELL entries on spot
//   - OrderMinUSD floor and proportional risk per trade

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---- Position & Trader ----

type Position struct {
	OpenPrice float64
	Side      OrderSide
	SizeBase  float64
	Stop      float64
	Take      float64
	OpenTime  time.Time
	// --- NEW: record entry fee for later P/L adjustment ---
	EntryFee float64

	// --- NEW (runner-only trailing fields; used only when this lot is the runner) ---
	TrailActive bool    // becomes true after TRAIL_ACTIVATE_PCT threshold
	TrailPeak   float64 // best favorable price since activation (peak for long; trough for short)
	TrailStop   float64 // current trailing stop level derived from TrailPeak and TRAIL_DISTANCE_PCT

	// --- NEW: human-readable gates/why string captured at entry time ---
	Reason string `json:"reason,omitempty"`
}

// BotState is the persistent snapshot of trader state.
type BotState struct {
	EquityUSD      float64
	DailyStart     time.Time
	DailyPnL       float64
	Lots           []*Position
	Model          *AIMicroModel
	MdlExt         *ExtendedLogit
	WalkForwardMin int
	LastFit        time.Time
	LastAdd           time.Time
	WinLow            float64
	LatchedGate       float64
	WinHigh           float64
	LatchedGateShort  float64
}

type Trader struct {
	cfg        Config
	broker     Broker
	model      *AIMicroModel
	pos        *Position   // kept for backward compatibility with earlier logic
	lots       []*Position // NEW: multiple lots when pyramiding is enabled
	lastAdd    time.Time   // NEW: last time a pyramid add was placed
	dailyStart time.Time
	dailyPnL   float64
	mu         sync.Mutex
	equityUSD  float64

	// NEW (minimal): optional extended head passed through to decide(); nil if unused.
	mdlExt *ExtendedLogit

	// NEW: path to persisted state file
	stateFile string

	// NEW: track last model fit time for walk-forward
	lastFit time.Time

	// NEW: index of the designated runner lot (-1 if none). Not persisted; derived on load.
	runnerIdx int

	// NEW: independent pyramiding anchor (stable reference, not tied to latest scalp)
	pyramidAnchorPrice float64
	pyramidAnchorTime  time.Time

	// --- NEW: adverse gate helpers (since last add) ---
	// BUY path trackers:
	winLow      float64 // lowest price seen since lastAdd (BUY adverse tracking)
	latchedGate float64 // latched adverse gate once threshold time is reached; reset on new add
	// SELL path trackers (NEW):
	winHigh           float64 // highest price seen since lastAdd (SELL adverse tracking)
	latchedGateShort  float64 // latched adverse gate for SELL once threshold time is reached; reset on new add
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	t := &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
		stateFile:  cfg.StateFile,
		runnerIdx:  -1,
	}

	// Persistence guard: backtests set PERSIST_STATE=false
	persist := getEnvBool("PERSIST_STATE", true)
	if !persist {
		// Disable persistence hard by clearing the path.
		t.stateFile = ""
		log.Printf("[INFO] persistence disabled (PERSIST_STATE=false); starting fresh state")
	} else {
		// Try to load state if enabled
		if err := t.loadState(); err == nil {
			log.Printf("[INFO] trader state restored from %s", t.stateFile)
		} else {
			log.Printf("[INFO] no prior state restored: %v", err)
			// >>> FAIL-FAST (requested): if live (not DryRun) and persistence is expected,
			// and the state path isn't a mounted/writable volume, abort with a clear message.
			if !t.cfg.DryRun && shouldFatalNoStateMount(t.stateFile) {
				log.Fatalf("[FATAL] persistence required but state path is not a mounted volume or not writable: STATE_FILE=%s ; "+
					"mount /opt/coinbase/state into the container and ensure it's writable. "+
					"Example docker-compose:\n  volumes:\n    - /opt/coinbase/state:/opt/coinbase/state",
					t.stateFile)
			}
		}
	}
	// If state has existing lots but no runner assigned (fresh field), default runner to the oldest or 0.
	if t.runnerIdx == -1 && len(t.lots) > 0 {
		t.runnerIdx = 0
	}
	return t
}

func (t *Trader) EquityUSD() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.equityUSD
}

// SetEquityUSD safely updates trader equity and the equity metric.
func (t *Trader) SetEquityUSD(v float64) {
	t.mu.Lock()
	t.equityUSD = v
	t.mu.Unlock()

	// update the metric with same naming style
	mtxPnL.Set(v)
	// persist new state (no-op if disabled)
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
		
	}
}

// NEW (minimal): allow live loop to inject/refresh the optional extended model.
func (t *Trader) SetExtendedModel(m *ExtendedLogit) {
	t.mu.Lock()
	t.mdlExt = m
	t.mu.Unlock()
}

func midnightUTC(ts time.Time) time.Time {
	y, m, d := ts.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (t *Trader) updateDaily(date time.Time) {
	if midnightUTC(date) != t.dailyStart {
		t.dailyStart = midnightUTC(date)
		t.dailyPnL = 0
		if err := t.saveState(); err != nil {
			log.Printf("[WARN] saveState: %v", err)
		}
	}
}

// ---- helpers for pyramiding ----

func allowPyramiding() bool {
	return getEnvBool("ALLOW_PYRAMIDING", false)
}
func pyramidMinSeconds() int {
	return getEnvInt("PYRAMID_MIN_SECONDS_BETWEEN", 0)
}
func pyramidMinAdversePct() float64 {
	return getEnvFloat("PYRAMID_MIN_ADVERSE_PCT", 0.0) // 0 = no adverse-move requirement
}
func scalpTPDecayEnabled() bool   { return getEnvBool("SCALP_TP_DECAY_ENABLE", false) }
func scalpTPDecayMode() string    { return getEnv("SCALP_TP_DEC_MODE", "linear") }
func scalpTPDecPct() float64      { return getEnvFloat("SCALP_TP_DEC_PCT", 0.0) }      // % points
func scalpTPDecayFactor() float64 { return getEnvFloat("SCALP_TP_DECAY_FACTOR", 1.0) } // multiplicative
func scalpTPMinPct() float64      { return getEnvFloat("SCALP_TP_MIN_PCT", 0.0) }      // floor

// --- NEW: Option A – time-based exponential decay knobs (0 disables) ---
func pyramidDecayLambda() float64 { return getEnvFloat("PYRAMID_DECAY_LAMBDA", 0.0) }  // per-minute
func pyramidDecayMinPct() float64 { return getEnvFloat("PYRAMID_DECAY_MIN_PCT", 0.0) } // floor

// Cap concurrent lots (env-tunable). Default is effectively "no cap".
func maxConcurrentLots() int {
	n := getEnvInt("MAX_CONCURRENT_LOTS", 1_000_000)
	if n < 1 {
		n = 1_000_000 // safety: never block adds due to bad input
	}
	return n
}

// Spot SELL guard and paper overrides
func requireBaseForShort() bool { return getEnvBool("REQUIRE_BASE_FOR_SHORT", true) }
func paperBaseBalance() float64 { return getEnvFloat("PAPER_BASE_BALANCE", 0.0) }
func baseAssetOverride() string { return getEnv("BASE_ASSET", "") }
func baseStepOverride() float64 { return getEnvFloat("BASE_STEP", 0.0) } // 0 => unknown

// --- NEW: backtest-only quote balance helpers (BUY gating symmetry) ---
func paperQuoteBalance() float64 { return getEnvFloat("PAPER_QUOTE_BALANCE", 0.0) }
func quoteStepOverride() float64 { return getEnvFloat("QUOTE_STEP", 0.0) } // 0 => unknown

// Runner tuning (internal, no new env keys): runner takes profit farther, same stop by default.
const runnerTPMult = 2.0
const runnerStopMult = 1.0

// Minimal "runner gap" guard (disabled)
const runnerMinGapPct = 0.0

// --- NEW: runner-only trailing env tunables (0 disables) ---
func trailActivatePct() float64 {
	return getEnvFloat("TRAIL_ACTIVATE_PCT", 0.0)
}
func trailDistancePct() float64 {
	return getEnvFloat("TRAIL_DISTANCE_PCT", 0.0)
}

// latestEntry returns the most recent long lot entry price, or 0 if none.
func (t *Trader) latestEntry() float64 {
	if len(t.lots) == 0 {
		return 0
	}
	return t.lots[len(t.lots)-1].OpenPrice
}

// aggregateOpen sets t.pos to the latest lot (for legacy reads) or nil.
func (t *Trader) aggregateOpen() {
	if len(t.lots) == 0 {
		t.pos = nil
		return
	}
	// keep last lot as representative for legacy checks
	t.pos = t.lots[len(t.lots)-1]
}

// applyRunnerTargets adjusts stop/take for the designated runner lot.
func (t *Trader) applyRunnerTargets(p *Position) {
	if p == nil {
		return
	}
	op := p.OpenPrice
	if p.Side == SideBuy {
		p.Stop = op * (1.0 - (t.cfg.StopLossPct*runnerStopMult)/100.0)
		p.Take = op * (1.0 + (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
	} else {
		p.Stop = op * (1.0 + (t.cfg.StopLossPct*runnerStopMult)/100.0)
		p.Take = op * (1.0 - (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
	}
}

// --- NEW: runner trailing updater (no-ops if env not set or lot not runner).
// Returns (shouldExit, newTrailStopIfAny).
func (t *Trader) updateRunnerTrail(lot *Position, price float64) (bool, float64) {
	if lot == nil {
		return false, 0
	}
	act := trailActivatePct()
	dist := trailDistancePct()
	if act <= 0 || dist <= 0 {
		return false, 0
	}

	switch lot.Side {
	case SideBuy:
		activateAt := lot.OpenPrice * (1.0 + act/100.0)
		if !lot.TrailActive {
			if price >= activateAt {
				lot.TrailActive = true
				lot.TrailPeak = price
				lot.TrailStop = price * (1.0 - dist/100.0)
			}
		} else {
			if price > lot.TrailPeak {
				lot.TrailPeak = price
				ts := lot.TrailPeak * (1.0 - dist/100.0)
				if ts > lot.TrailStop {
					lot.TrailStop = ts
				}
			}
			if price <= lot.TrailStop && lot.TrailStop > 0 {
				return true, lot.TrailStop
			}
		}
	case SideSell:
		activateAt := lot.OpenPrice * (1.0 - act/100.0)
		if !lot.TrailActive {
			if price <= activateAt {
				lot.TrailActive = true
				lot.TrailPeak = price // trough for short
				lot.TrailStop = price * (1.0 + dist/100.0)
			}
		} else {
			if price < lot.TrailPeak {
				lot.TrailPeak = price
				lot.TrailStop = lot.TrailPeak * (1.0 + dist/100.0)
			}
			if price >= lot.TrailStop && lot.TrailStop > 0 {
				return true, lot.TrailStop
			}
		}
	}
	return false, lot.TrailStop
}

// closeLotAtIndex closes a single lot at idx (assumes mutex held), performing I/O unlocked.
// exitReason is a short label for logs: "take_profit" | "stop_loss" | "trailing_stop" (or other).
func (t *Trader) closeLotAtIndex(ctx context.Context, c []Candle, idx int, exitReason string) (string, error) {
	price := c[len(c)-1].Close
	lot := t.lots[idx]
	closeSide := SideSell
	if lot.Side == SideSell {
		closeSide = SideBuy
	}
	baseRequested := lot.SizeBase
	quote := baseRequested * price

	// unlock for I/O
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote)
		if err != nil {
			if t.cfg.Extended().UseDirectSlack {
				postSlack(fmt.Sprintf("ERR step: %v", err))
			}
			// Re-lock before returning so caller's Unlock matches.
			t.mu.Lock()
			return "", fmt.Errorf("close order failed: %w", err)
		}
		mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
	}
	// re-lock
	t.mu.Lock()

	// --- NEW: check if the lot being closed is the most recent add (newest OpenTime) ---
	wasNewest := true
	refOpen := lot.OpenTime
	for j := range t.lots {
		if j == idx {
			continue
		}
		if !t.lots[j].OpenTime.IsZero() && t.lots[j].OpenTime.After(refOpen) {
			wasNewest = false
			break
		}
	}

	// --- MINIMAL CHANGE: use actual filled size/price if available ---
	priceExec := c[len(c)-1].Close
	baseFilled := baseRequested
	if placed != nil {
		if placed.Price > 0 {
			priceExec = placed.Price
		}
		if placed.BaseSize > 0 {
			baseFilled = placed.BaseSize
		}
		// Log WARN on partial fill (filled < requested) with a small tolerance.
		const tol = 1e-9
		if baseFilled+tol < baseRequested {
			log.Printf("[WARN] partial fill (exit): requested_base=%.8f filled_base=%.8f (%.2f%%)",
				baseRequested, baseFilled, 100.0*(baseFilled/baseRequested))
		}
	}
	// refresh price snapshot (best-effort) if no execution price was available
	if placed == nil || placed.Price <= 0 {
		priceExec = c[len(c)-1].Close
	}

	// compute P/L using actual fill size and execution price
	pl := (priceExec - lot.OpenPrice) * baseFilled
	if lot.Side == SideSell {
		pl = (lot.OpenPrice - priceExec) * baseFilled
	}

	// apply exit fee; prefer broker-provided commission if present ---
	quoteExec := baseFilled * priceExec
	feeRate := t.cfg.FeeRatePct
	exitFee := quoteExec * (feeRate / 100.0)
	if placed != nil {
		if placed.CommissionUSD > 0 {
			exitFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (exit); falling back to FEE_RATE_PCT=%.4f%%", feeRate)
		}
	}
	pl -= lot.EntryFee // subtract entry fee recorded
	pl -= exitFee      // subtract exit fee now

	t.dailyPnL += pl
	t.equityUSD += pl

	// --- NEW: increment win/loss trades ---
	if pl >= 0 {
		mtxTrades.WithLabelValues("win").Inc()
	} else {
		mtxTrades.WithLabelValues("loss").Inc()
	}

	// Track if we removed the runner and adjust runnerIdx accordingly after removal.
	removedWasRunner := (idx == t.runnerIdx)

	// remove lot idx
	t.lots = append(t.lots[:idx], t.lots[idx+1:]...)

	// shift runnerIdx if needed
	if t.runnerIdx >= 0 {
		if idx < t.runnerIdx {
			t.runnerIdx-- // slice shifted left
		} else if idx == t.runnerIdx {
			// runner removed; promote the NEWEST remaining lot (if any) to runner
			if len(t.lots) > 0 {
				t.runnerIdx = len(t.lots) - 1
				// reset trailing fields for the newly promoted runner
				nr := t.lots[t.runnerIdx]
				nr.TrailActive = false
				nr.TrailPeak = nr.OpenPrice
				nr.TrailStop = 0
				// also re-apply runner targets (keeps existing behavior)
				t.applyRunnerTargets(nr)
			} else {
				t.runnerIdx = -1
			}
		}
	}

	// --- if the closed lot was the most recent add, re-anchor pyramiding timers/state ---
	if wasNewest {
		// If any lots remain, restart the decay clock from now to avoid instant latch.
		// If none remain, also set now; next add will proceed normally.
		t.lastAdd = time.Now().UTC()
		// Reset adverse tracking; winLow/winHigh will start accumulating after t_floor_min,
		// and latching can only occur after 2*t_floor_min from this new anchor.
		t.winLow = 0
		t.latchedGate = 0
		t.winHigh = 0
		t.latchedGateShort = 0
	}

	t.aggregateOpen()
	// Include reason in message for operator visibility
	msg := fmt.Sprintf("EXIT %s at %.2f reason=%s entry_reason=%s P/L=%.2f (fees=%.4f)",
		c[len(c)-1].Time.Format(time.RFC3339), priceExec, exitReason, lot.Reason, pl, lot.EntryFee+exitFee)
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
	}

	_ = removedWasRunner // kept to emphasize runner path; no extra logs.
	return msg, nil
}

// ---- Core tick ----

// step consumes the current candle history and may place/close a position.
// It returns a human-readable status string for logging.
func (t *Trader) step(ctx context.Context, c []Candle) (string, error) {
	if len(c) == 0 {
		return "NO_DATA", nil
	}

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	// Use wall clock as authoritative "now" for pyramiding timings; fall back for zero candle time.
	wallNow := time.Now().UTC()

	now := c[len(c)-1].Time
	if now.IsZero() {
		now = wallNow
	}
	t.updateDaily(now)

	// --- NEW: walk-forward (re)fit guard hook (no-op other than the guard) ---
	// Any refit logic must first check shouldRefit(len(c)).
	// This preserves restored weights when history is thin.
	_ = t.shouldRefit(len(c)) // intentionally unused here (guard only)

	// Keep paper broker price in sync with the latest close so paper fills are realistic.
	if pb, ok := t.broker.(*PaperBroker); ok {
		if len(c) > 0 {
			pb.mu.Lock()
			pb.price = c[len(c)-1].Close
			pb.mu.Unlock()
		}
	}

	// --- EXIT path: if any lots are open, evaluate TP/SL for each and close those that trigger.
	if len(t.lots) > 0 {
		price := c[len(c)-1].Close
		nearestStop := 0.0
		nearestTake := 0.0
		for i := 0; i < len(t.lots); {
			lot := t.lots[i]

			// --- NEW: runner-only trailing exit check (wired alongside TP/SL) ---
			if i == t.runnerIdx {
				if trigger, tstop := t.updateRunnerTrail(lot, price); trigger {
					// reflect trailing level for visibility in debug/Slack
					lot.Stop = tstop
					msg, err := t.closeLotAtIndex(ctx, c, i, "trailing_stop")
					if err != nil {
						t.mu.Unlock()
						return "", err
					}
					// closeLotAtIndex removed index i; continue without i++
					t.mu.Unlock()
					return msg, nil
				}
			}

			trigger := false
			exitReason := ""
			if lot.Side == SideBuy && (price <= lot.Stop || price >= lot.Take) {
				trigger = true
				if price <= lot.Stop {
					exitReason = "stop_loss"
				} else {
					exitReason = "take_profit"
				}
			}
			if lot.Side == SideSell && (price >= lot.Stop || price <= lot.Take) {
				trigger = true
				if price >= lot.Stop {
					exitReason = "stop_loss"
				} else {
					exitReason = "take_profit"
				}
			}
			if trigger {
				msg, err := t.closeLotAtIndex(ctx, c, i, exitReason)
				if err != nil {
					t.mu.Unlock()
					return "", err
				}
				// closeLotAtIndex removed index i; continue without i++
				t.mu.Unlock()
				return msg, nil
			}

			if lot.Side == SideBuy {
				if nearestStop == 0 || lot.Stop > nearestStop { // highest stop for long
					nearestStop = lot.Stop
				}
				if nearestTake == 0 || lot.Take < nearestTake { // lowest take for long
					nearestTake = lot.Take
				}
			} else { // SideSell
				if nearestStop == 0 || lot.Stop < nearestStop { // lowest stop for short
					nearestStop = lot.Stop
				}
				if nearestTake == 0 || lot.Take > nearestTake { // highest take for short
					nearestTake = lot.Take
				}
			}

			i++ // no trigger; move to next
		}
		log.Printf("[DEBUG] nearest stop=%.2f take=%.2f across %d lots", nearestStop, nearestTake, len(t.lots))
	}

	d := decide(c, t.model, t.mdlExt)
	log.Printf("[DEBUG] Lots=%d, Decision=%s Reason = %s, buyThresh=%.3f, sellThresh=%.3f, LongOnly=%v", len(t.lots), d.Signal, d.Reason, buyThreshold, sellThreshold, t.cfg.LongOnly)

	// Ignore discretionary SELL signals while lots are open; exits are TP/SL only.
	// if len(t.lots) > 0 && d.Signal == Sell {
	// 	t.mu.Unlock()
	// 	return "HOLD", nil
	// }

	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	price := c[len(c)-1].Close

	// --- NEW: track lowest price since last add (BUY path) and highest price (SELL path) ---
	if !t.lastAdd.IsZero() {
		if t.winLow == 0 || price < t.winLow {
			t.winLow = price
		}
		if t.winHigh == 0 || price > t.winHigh {
			t.winHigh = price
		}
	}

	// Long-only veto for SELL when flat; unchanged behavior.
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}
	if d.Signal == Flat {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT [%s]", d.Reason), nil
	}

	// Respect lot cap (both sides)
	if len(t.lots) >= maxConcurrentLots() {
		t.mu.Unlock()
		log.Printf("[DEBUG] lot cap reached (%d); HOLD", maxConcurrentLots())
		return "HOLD", nil
	}

	// Determine if we are opening first lot or attempting a pyramid add.
	// --- CHANGED: enable SELL pyramiding symmetry ---
	isAdd := len(t.lots) > 0 && allowPyramiding() && (d.Signal == Buy || d.Signal == Sell)

	// --- NEW: variables to capture gate audit fields for the reason string (side-biased; no winLow) ---
	var (
		reasonGatePrice float64
		reasonLatched   float64
		reasonEffPct    float64
		reasonBasePct   float64
		reasonElapsedHr float64
	)

	// Gating for pyramiding adds — spacing + adverse move (with optional time-decay).
	if isAdd {
		// 1) Spacing: always enforce (s=0 means no wait; set >0 to require time gap)
		s := pyramidMinSeconds()
		if time.Since(t.lastAdd) < time.Duration(s)*time.Second {
			t.mu.Unlock()
			hrs := time.Since(t.lastAdd).Hours()
			log.Printf("[DEBUG] pyramid: blocked by spacing; since_last=%vHours need>=%ds", fmt.Sprintf("%.1f", hrs), s)
			return "HOLD", nil
		}

		// 2) Adverse move gate with optional time-based exponential decay.
		basePct := pyramidMinAdversePct()
		effPct := basePct
		lambda := pyramidDecayLambda()
		floor := pyramidDecayMinPct()
		elapsedMin := 0.0
		if lambda > 0 {
			if !t.lastAdd.IsZero() {
				elapsedMin = time.Since(t.lastAdd).Minutes()
			} else {
				elapsedMin = 0.0
			}
			decayed := basePct * math.Exp(-lambda*elapsedMin)
			if decayed < floor {
				decayed = floor
			}
			effPct = decayed
		}

		// Capture for reason string
		reasonBasePct = basePct
		reasonEffPct = effPct
		reasonElapsedHr = elapsedMin / 60.0

		// Time (in minutes) to hit the floor once (t_floor_min); used for latching thresholds.
		tFloorMin := 0.0
		if lambda > 0 && basePct > floor {
			tFloorMin = math.Log(basePct/floor) / lambda
		}

		last := t.latestEntry()
		if last > 0 {
			if d.Signal == Buy {
				// BUY adverse tracker
				if elapsedMin >= tFloorMin {
					if t.winLow == 0 || price < t.winLow {
						t.winLow = price
					}
				} else {
					t.winLow = 0
				}
				// latch at 2*t_floor_min
				if t.latchedGate == 0 && elapsedMin >= 2.0*tFloorMin && t.winLow > 0 {
					t.latchedGate = t.winLow
				}
				// baseline gate: last * (1 - effPct); latched replaces baseline
				gatePrice := last * (1.0 - effPct/100.0)
				if t.latchedGate > 0 {
					gatePrice = t.latchedGate
				}
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGate

				if !(price <= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (BUY); price=%.2f last_gate<=%.2f win_low=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winLow, effPct, basePct, reasonElapsedHr)
					return "HOLD", nil
				}
			} else { // SELL
				// SELL adverse tracker
				if elapsedMin >= tFloorMin {
					if t.winHigh == 0 || price > t.winHigh {
						t.winHigh = price
					}
				} else {
					t.winHigh = 0
				}
				if t.latchedGateShort == 0 && elapsedMin >= 2.0*tFloorMin && t.winHigh > 0 {
					t.latchedGateShort = t.winHigh
				}
				// baseline gate: last * (1 + effPct); latched replaces baseline
				gatePrice := last * (1.0 + effPct/100.0)
				if t.latchedGateShort > 0 {
					gatePrice = t.latchedGateShort
				}
				reasonGatePrice = gatePrice
				reasonLatched = t.latchedGateShort

				if !(price >= gatePrice) {
					t.mu.Unlock()
					log.Printf("[DEBUG] pyramid: blocked by last gate (SELL); price=%.2f last_gate>=%.2f win_high=%.3f eff_pct=%.3f base_pct=%.3f elapsed_Hours=%.1f",
						price, gatePrice, t.winHigh, effPct, basePct, reasonElapsedHr)
					return "HOLD", nil
				}
			}
		}
	}

	// Sizing (risk % of current equity, with optional volatility adjust already supported).
	riskPct := t.cfg.RiskPerTradePct
	if t.cfg.Extended().VolRiskAdjust {
		f := volRiskFactor(c)
		riskPct = riskPct * f
		SetVolRiskFactorMetric(f)
	}
	quote := (riskPct / 100.0) * t.equityUSD
	if quote < t.cfg.OrderMinUSD {
		quote = t.cfg.OrderMinUSD
	}
	base := quote / price
	side := d.SignalToSide()

	// Unified epsilon for spare checks
	const spareEps = 1e-9

	// --- BUY gating (require spare quote after reserving open shorts) ---
	if side == SideBuy {
		// Reserve quote needed to close all existing short lots at current price.
		var reservedShortQuote float64
		for _, lot := range t.lots {
			if lot.Side == SideSell {
				reservedShortQuote += lot.SizeBase * price
			}
		}

		// Ask broker for quote balance/step (uniform: live & DryRun).
		sym, aq, qs, err := t.broker.GetAvailableQuote(ctx, t.cfg.ProductID)
		if err != nil || strings.TrimSpace(sym) == "" {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: GetAvailableQuote failed: %v", err)
		}
		availQuote := aq
		qstep := qs
		if qstep <= 0 {
			t.mu.Unlock()
			log.Fatalf("BUY blocked: missing/invalid QUOTE step for %s (step=%.8f)", t.cfg.ProductID, qstep)
		}

		// Floor the needed quote to step.
		neededQuote := quote
		if qstep > 0 {
			n := math.Floor(neededQuote/qstep) * qstep
			if n > 0 {
				neededQuote = n
			}
		}

		spare := availQuote - reservedShortQuote
		if spare < 0 {
			spare = 0
		}
		if spare+spareEps < neededQuote {
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE BUY: need=%.2f quote, spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
				neededQuote, spare, availQuote, reservedShortQuote, qstep)
			return "HOLD", nil
		}

		// Enforce exchange minimum notional after snapping, then snap UP to step to keep >= min; re-check spare.
		if neededQuote < t.cfg.OrderMinUSD {
			neededQuote = t.cfg.OrderMinUSD
			if qstep > 0 {
				steps := math.Ceil(neededQuote / qstep)
				neededQuote = steps * qstep
			}
			if spare+spareEps < neededQuote {
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE BUY: need=%.2f quote (min-notional), spare=%.2f (avail=%.2f, reserved_shorts=%.6f, step=%.2f)",
					neededQuote, spare, availQuote, reservedShortQuote, qstep)
				return "HOLD", nil
			}
		}

		// Use the final neededQuote; recompute base.
		quote = neededQuote
		base = quote / price
	}

	// If SELL, require spare base inventory (spot safe)
	if side == SideSell && requireBaseForShort() {
		// Sum reserved base for long lots
		var reservedLong float64
		for _, lot := range t.lots {
			if lot.Side == SideBuy {
				reservedLong += lot.SizeBase
			}
		}

		// Ask broker for base balance/step (uniform: live & DryRun).
		sym, ab, stp, err := t.broker.GetAvailableBase(ctx, t.cfg.ProductID)
		if err != nil || strings.TrimSpace(sym) == "" {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: GetAvailableBase failed: %v", err)
		}
		availBase := ab
		step := stp
		if step <= 0 {
			t.mu.Unlock()
			log.Fatalf("SELL blocked: missing/invalid BASE step for %s (step=%.8f)", t.cfg.ProductID, step)
		}

		// Floor the *needed* base to step (if known) and cap by spare availability
		neededBase := base
		if step > 0 {
			n := math.Floor(neededBase/step) * step
			if n > 0 {
				neededBase = n
			}
		}
		spare := availBase - reservedLong
		if spare < 0 {
			spare = 0
		}
		if spare+spareEps < neededBase {
			t.mu.Unlock()
			log.Printf("[DEBUG] GATE SELL: need=%.8f base, spare=%.8f (avail=%.8f, reserved_longs=%.8f, step=%.8f)",
				neededBase, spare, availBase, reservedLong, step)
			return "HOLD", nil
		}

		// Use the floored base for the order by updating quote
		quote = neededBase * price
		base = neededBase

		// Ensure SELL meets exchange min funds and step rules (and re-check spare symmetry)
		if quote < t.cfg.OrderMinUSD {
			quote = t.cfg.OrderMinUSD
			base = quote / price
			if step > 0 {
				b := math.Floor(base/step) * step
				if b > 0 {
					base = b
					quote = base * price
				}
			}
			// >>> Symmetry: re-check spare after min-notional snap <<<
			if spare+spareEps < base {
				t.mu.Unlock()
				log.Printf("[DEBUG] GATE SELL: need=%.8f base (min-notional), spare=%.8f (avail=%.8f, reserved_longs=%.8f, step=%.8f)",
					base, spare, availBase, reservedLong, step)
				return "HOLD", nil
			}
		}
	}

	// Stops/takes (baseline for scalps)
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	if side == SideSell {
		stop = price * (1.0 + t.cfg.StopLossPct/100.0)
		take = price * (1.0 - t.cfg.TakeProfitPct/100.0)
	}

	// Decide if this new entry will be the runner (only when there is no existing runner).
	willBeRunner := (t.runnerIdx == -1 && len(t.lots) == 0)
	if willBeRunner {
		// Stretch runner targets without introducing new env keys.
		if side == SideBuy {
			stop = price * (1.0 - (t.cfg.StopLossPct*runnerStopMult)/100.0)
			take = price * (1.0 + (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
		} else {
			stop = price * (1.0 + (t.cfg.StopLossPct*runnerStopMult)/100.0)
			take = price * (1.0 - (t.cfg.TakeProfitPct*runnerTPMult)/100.0)
		}
	} else if scalpTPDecayEnabled() {
		// This is a scalp add: compute k = number of existing scalps
		k := len(t.lots)
		if t.runnerIdx >= 0 && t.runnerIdx < len(t.lots) {
			k = len(t.lots) - 1 // exclude the runner from the scalp index
		}
		baseTP := t.cfg.TakeProfitPct
		tpPct := baseTP

		switch scalpTPDecayMode() {
		case "exp", "exponential":
			// geometric decay: baseTP * factor^k, floored
			f := scalpTPDecayFactor()
			if f <= 0 {
				f = 1.0
			}
			factorPow := 1.0
			for i := 0; i < k; i++ {
				factorPow *= f
			}
			tpPct = baseTP * factorPow
		default:
			// linear: baseTP - k * decPct, floored
			dec := scalpTPDecPct()
			tpPct = baseTP - float64(k)*dec
		}

		minTP := scalpTPMinPct()
		if tpPct < minTP {
			tpPct = minTP
		}

		// apply the (possibly reduced) TP for the scalp only
		if side == SideBuy {
			take = price * (1.0 + tpPct/100.0)
		} else {
			take = price * (1.0 - tpPct/100.0)
		}

		// >>> DEBUG LOG <<<
		log.Printf("[DEBUG] scalp tp decay: k=%d mode=%s baseTP=%.3f%% tpPct=%.3f%% minTP=%.3f%% take=%.2f",
			k, scalpTPDecayMode(), t.cfg.TakeProfitPct, tpPct, minTP, take)
	}

	// --- apply entry fee (preliminary; may be replaced by broker-provided commission below) ---
	feeRate := t.cfg.FeeRatePct
	entryFee := quote * (feeRate / 100.0)
	if t.cfg.DryRun {
		t.equityUSD -= entryFee
	}

	// Place live order without holding the lock.
	t.mu.Unlock()
	var placed *PlacedOrder
	if !t.cfg.DryRun {
		var err error
		placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
		if err != nil {
			// Retry once with ORDER_MIN_USD on insufficient-funds style failures.
			e := strings.ToLower(err.Error())
			if quote > t.cfg.OrderMinUSD && (strings.Contains(e, "insufficient") || strings.Contains(e, "funds") || strings.Contains(e, "400")) {
				log.Printf("[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f", quote, err, t.cfg.OrderMinUSD)
				quote = t.cfg.OrderMinUSD
				base = quote / price
				placed, err = t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote)
			}
			if err != nil {
				if t.cfg.Extended().UseDirectSlack {
					postSlack(fmt.Sprintf("ERR step: %v", err))
				}
				return "", err
			}
		}
		mtxOrders.WithLabelValues("live", string(side)).Inc()
		mtxTrades.WithLabelValues("open").Inc()
	} else {
		mtxTrades.WithLabelValues("open").Inc()
	}

	// Re-lock to mutate state (append new lot or first lot).
	t.mu.Lock()

	// --- MINIMAL CHANGE: use actual filled size/price when available ---
	priceToUse := price
	baseRequested := base
	baseToUse := baseRequested
	actualQuote := quote

	if placed != nil {
		if placed.Price > 0 {
			priceToUse = placed.Price
		}
		if placed.BaseSize > 0 {
			baseToUse = placed.BaseSize
		}
		if placed.QuoteSpent > 0 {
			actualQuote = placed.QuoteSpent
		}
		// Log WARN on partial fill (filled < requested) with a small tolerance.
		const tol = 1e-9
		if baseToUse+tol < baseRequested {
			log.Printf("[WARN] partial fill: requested_base=%.8f filled_base=%.8f (%.2f%%)",
				baseRequested, baseToUse, 100.0*(baseToUse/baseRequested))
		}
	}

	// Prefer broker-provided commission for entry if present; otherwise fallback to FEE_RATE_PCT.
	if placed != nil {
		if placed.CommissionUSD > 0 {
			entryFee = placed.CommissionUSD
		} else {
			log.Printf("[WARN] commission missing (entry); falling back to FEE_RATE_PCT=%.4f%%", feeRate)
			entryFee = actualQuote * (feeRate / 100.0)
		}
	} else {
		// DryRun path keeps previously computed entryFee and adjusts by delta as before.
	}

	if t.cfg.DryRun {
		// already deducted above for DryRun using quote; adjust to the actualQuote delta
		delta := (actualQuote - quote) * (feeRate / 100.0)
		t.equityUSD -= delta
	}

	// --- NEW: side-biased Lot reason (without winLow) ---
	var gatesReason string
	if side == SideBuy {
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|PriceDownGoingUp=%v|LowBottom=%v",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr,
			d.PriceDownGoingUp, d.LowBottom,
		)
	} else { // SideSell
		gatesReason = fmt.Sprintf(
			"pUp=%.5f|gatePrice=%.3f|latched=%.3f|effPct=%.3f|basePct=%.3f|elapsedHr=%.1f|HighPeak=%v|PriceUpGoingDown=%v",
			d.PUp, reasonGatePrice, reasonLatched, reasonEffPct, reasonBasePct, reasonElapsedHr,
			d.HighPeak, d.PriceUpGoingDown,
		)
	}

	newLot := &Position{
		OpenPrice: priceToUse,
		Side:      side,
		SizeBase:  baseToUse,
		Stop:      stop,
		Take:      take,
		OpenTime:  now,
		EntryFee:  entryFee,
		Reason:    gatesReason, // side-biased; no winLow
		// trailing fields default zero/false; they’ll be initialized if this becomes runner
	}
	t.lots = append(t.lots, newLot)
	// Use wall clock for lastAdd to drive spacing/decay even if candle time is zero.
	t.lastAdd = wallNow
	// Reset adverse tracking for the new add.
	t.winLow = priceToUse
	t.latchedGate = 0
	t.winHigh = priceToUse
	t.latchedGateShort = 0

	// Assign/designate runner if none exists yet; otherwise this is a scalp.
	if t.runnerIdx == -1 {
		t.runnerIdx = len(t.lots) - 1 // the just-added lot is runner
		// Initialize runner trailing baseline
		r := t.lots[t.runnerIdx]
		r.TrailActive = false
		r.TrailPeak = r.OpenPrice
		r.TrailStop = 0
		// Ensure runner's stretched targets are applied (keeps baseline behavior for runner).
		t.applyRunnerTargets(r)
	}

	t.aggregateOpen()

	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f stop=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			d.Signal, actualQuote, baseToUse, newLot.Stop, newLot.Take, entryFee, newLot.Reason, d.Reason)
	} else {
		msg = fmt.Sprintf("[LIVE ORDER] %s quote=%.2f stop=%.2f take=%.2f fee=%.4f reason=%s [%s]",
			d.Signal, actualQuote, newLot.Stop, newLot.Take, entryFee, newLot.Reason, d.Reason)
	}
	if t.cfg.Extended().UseDirectSlack {
		postSlack(msg)
	}
	// persist new state
	if err := t.saveState(); err != nil {
		log.Printf("[WARN] saveState: %v", err)
	}
	t.mu.Unlock()
	return msg, nil
}

// ---- labels ----

func signalLabel(s Signal) string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	default:
		return "flat"
	}
}

// ---- Persistence helpers ----

func (t *Trader) saveState() error {
	if t.stateFile == "" || !getEnvBool("PERSIST_STATE", true) {
		return nil
	}
	state := BotState{
		EquityUSD:      t.equityUSD,
		DailyStart:     t.dailyStart,
		DailyPnL:       t.dailyPnL,
		Lots:           t.lots,
		Model:          t.model,
		MdlExt:         t.mdlExt,
		WalkForwardMin: t.cfg.Extended().WalkForwardMin,
		LastFit:        t.lastFit,
		LastAdd:          t.lastAdd,
		WinLow:           t.winLow,
		LatchedGate:      t.latchedGate,
		WinHigh:          t.winHigh,
		LatchedGateShort: t.latchedGateShort,
	}
	bs, err := json.MarshalIndent(state, "", " ")
	if err != nil {
		return err
	}
	tmp := t.stateFile + ".tmp"
	if err := os.WriteFile(tmp, bs, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, t.stateFile)
}

func (t *Trader) loadState() error {
	if t.stateFile == "" || !getEnvBool("PERSIST_STATE", true) {
		return fmt.Errorf("no state file configured")
	}
	bs, err := os.ReadFile(t.stateFile)
	if err != nil {
		return err
	}
	var st BotState
	if err := json.Unmarshal(bs, &st); err != nil {
		return err
	}
	// Prefer configured/live equity rather than stale persisted equity when:
	//  - running in DRY_RUN / backtest, or
	//  - live-equity mode is enabled (we will rebase from Bridge when available).
	// This prevents negative/old EquityUSD from leaking into runs that don't want it.
	if !(t.cfg.DryRun || t.cfg.UseLiveEquity()) {
		t.equityUSD = st.EquityUSD
	} else {
		// keep t.equityUSD as initialized from cfg.USDEquity; live rebase will adjust later
	}
	t.dailyStart = st.DailyStart
	t.dailyPnL = st.DailyPnL
	t.lots = st.Lots
	if st.Model != nil {
		t.model = st.Model
	}
	if st.MdlExt != nil {
		t.mdlExt = st.MdlExt
	}
	if !st.LastFit.IsZero() {
		t.lastFit = st.LastFit
	}

	t.aggregateOpen()
	// Re-derive runnerIdx if not set (old state files won't carry it).
	if t.runnerIdx == -1 && len(t.lots) > 0 {
		t.runnerIdx = 0
		// Initialize trailing baseline for current runner if not already set
		r := t.lots[t.runnerIdx]
		if r.TrailPeak == 0 {
			// Initialize baseline to current open price if peak is unset.
			r.TrailPeak = r.OpenPrice
		}
	}

	// Restore pyramiding gate memory (if present in state file).
	t.lastAdd          = st.LastAdd
	t.winLow           = st.WinLow
	t.latchedGate      = st.LatchedGate
	t.winHigh          = st.WinHigh
	t.latchedGateShort = st.LatchedGateShort

	// --- Restart warmup for pyramiding decay/adverse tracking ---
	// If we restored with open lots but have no lastAdd, seed the decay clock to "now"
	// and reset adverse trackers/latches so they rebuild over real time (prevents instant latch).
	if len(t.lots) > 0 && t.lastAdd.IsZero() {
		t.lastAdd = time.Now().UTC()
		t.winLow = 0
		t.latchedGate = 0
		t.winHigh = 0
		t.latchedGateShort = 0
	}
	return nil
}

// ---- Phase-7 helpers ----

// postSlack sends a best-effort Slack webhook message if SLACK_WEBHOOK is set.
// No impact on baseline behavior or logging; errors are ignored.
func postSlack(msg string) {
	hook := getEnv("SLACK_WEBHOOK", "")
	if hook == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body := map[string]string{"text": msg}
	bs, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", hook, bytes.NewReader(bs))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	_, _ = http.DefaultClient.Do(req)
}

// volRiskFactor derives a multiplicative factor from recent relative volatility.
// Returns ~0.6–0.8 in high vol, ~1.0 normal, up to ~1.2 in very low vol.
func volRiskFactor(c []Candle) float64 {
	if len(c) < 40 {
		return 1.0
	}
	cl := make([]float64, len(c))
	for i := range c {
		cl[i] = c[i].Close
	}
	std20 := RollingStd(cl, 20)
	i := len(std20) - 1
	relVol := std20[i] / (cl[i] + 1e-12)
	switch {
	case relVol > 0.02:
		return 0.6
	case relVol > 0.01:
		return 0.8
	case relVol < 0.004:
		return 1.2
	default:
		return 1.0
	}
}

// ---- Refit guard (minimal, internal) ----

// shouldRefit returns true only when we allow a model (re)fit:
// 1) len(history) >= cfg.MaxHistoryCandles, and
// 2) optional walk-forward cadence satisfied (cfg.Extended().WalkForwardMin).
// This is a guard only; it performs no fitting and emits no logs/metrics.
func (t *Trader) shouldRefit(historyLen int) bool {
	if historyLen < t.cfg.MaxHistoryCandles {
		return false
	}
	min := t.cfg.Extended().WalkForwardMin
	if min <= 0 {
		return true
	}
	if t.lastFit.IsZero() {
		return true
	}
	return time.Since(t.lastFit) >= time.Duration(min)*time.Minute
}

// ---- Fail-fast helpers (startup state mount check) ----

// shouldFatalNoStateMount returns true when we expect persistence but the state file's
// parent directory is not a mounted volume or not writable. This prevents accidental
// flat-boot trading after CI/CD restarts when the host volume isn't mounted.
func shouldFatalNoStateMount(stateFile string) bool {
	stateFile = strings.TrimSpace(stateFile)
	if stateFile == "" {
		return false
	}
	dir := filepath.Dir(stateFile)

	// If the file already exists, don't fatal — persistence is working.
	if _, err := os.Stat(stateFile); err == nil {
		return false
	}

	// Ensure parent directory exists and is a directory.
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return true
	}

	// Ensure directory is writable.
	if f, err := os.CreateTemp(dir, "wtest-*.tmp"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
	} else {
		return true
	}

	// Ensure it's actually a mount point (host volume), not a container tmp dir.
	isMount, err := isMounted(dir)
	if err == nil && !isMount {
		return true
	}
	return false
}

// isMounted checks /proc/self/mountinfo to see if dir is a mount point.
func isMounted(dir string) (bool, error) {
	bs, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	dir = filepath.Clean(dir)
	for _, ln := range strings.Split(string(bs), "\n") {
		parts := strings.Split(ln, " ")
		if len(parts) < 5 {
			continue
		}
		mp := filepath.Clean(parts[4]) // mount point field
		if mp == dir {
			return true, nil
		}
	}
	return false, nil
}

}}
env.go{{
// FILE: env.go
// Package main – Environment helpers for the trading bot.
//
// This file provides:
//   1) Small helpers to read environment variables with sane defaults
//      (strings, ints, floats, bools).
//   2) A safe loader (loadBotEnv) that reads /opt/coinbase/env/bot.env only,
//      ignoring secrets meant for the Python bridge.
//   3) Strategy threshold knobs (buyThreshold, sellThreshold, useMAFilter) and an
//      initializer (initThresholdsFromEnv) so you can tune behavior via env
//      without recompiling.
//
// Notes:
//   • The bot never requires `export $(cat .env ...)`.
//   • The Python FastAPI sidecar uses its own /opt/coinbase/env/bridge.env.

package main

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
)

// --------- Env helpers (used across files) ---------

func getEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
func getEnvFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
func getEnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "y", "yes":
		return true
	case "0", "false", "n", "no":
		return false
	case "":
		return def
	default:
		return def
	}
}
func getEnvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

// --------- .env loader (bot-only) ---------

// loadBotEnv reads /opt/coinbase/env/bot.env and sets ONLY the keys the Go bot needs.
// It won't override variables already in the environment and ignores secrets not required.
func loadBotEnv() {
	path := "/opt/coinbase/env/bot.env"
	f, err := os.Open(path)
	if err != nil {
		log.Printf("env: %s not found, relying on process env", path)
		return
	}
	defer f.Close()

	needed := map[string]struct{}{
		"PRODUCT_ID": {}, "GRANULARITY": {}, "DRY_RUN": {}, "MAX_DAILY_LOSS_PCT": {},
		"RISK_PER_TRADE_PCT": {}, "USD_EQUITY": {}, "TAKE_PROFIT_PCT": {},
		"STOP_LOSS_PCT": {}, "ORDER_MIN_USD": {}, "LONG_ONLY": {}, "PORT": {}, "BRIDGE_URL": {},
		"BUY_THRESHOLD": {}, "SELL_THRESHOLD": {}, "USE_MA_FILTER": {}, "BACKTEST_SLEEP_MS": {},
		
		// ---- pyramiding/env-driven knobs ----
		"ALLOW_PYRAMIDING":            {},
		"PYRAMID_MIN_SECONDS_BETWEEN": {},
		"PYRAMID_MIN_ADVERSE_PCT":     {},
		// time-based exponential decay
		"PYRAMID_DECAY_LAMBDA":  {},
		"PYRAMID_DECAY_MIN_PCT": {}, // tick/candle sync & risk
		"USE_TICK_PRICE":            {},
		"TICK_INTERVAL_SEC":         {},
		"CANDLE_RESYNC_SEC":         {},
		"DAILY_BREAKER_MARK_TO_MARKET": {},
		"FEE_RATE_PCT":                 {},
		"MAX_CONCURRENT_LOTS":          {},
		"STATE_FILE":                   {},

		// trailing & tp-decay (optional)
		"TRAIL_ACTIVATE_PCT":    {},
		"TRAIL_DISTANCE_PCT":    {},
		"SCALP_TP_DECAY_ENABLE": {},
		"SCALP_TP_DEC_MODE":     {},
		"SCALP_TP_DEC_PCT":      {},
		"SCALP_TP_DECAY_FACTOR": {},
		"SCALP_TP_MIN_PCT":      {},

		// history depth
		"MAX_HISTORY_CANDLES": {},

		// spot safety and paper balances/steps
		"REQUIRE_BASE_FOR_SHORT": {},
		"PAPER_BASE_BALANCE":     {},
		"BASE_ASSET":             {},
		"BASE_STEP":              {},
		"PAPER_QUOTE_BALANCE":    {},
		"QUOTE_STEP":             {},

		// broker selection + Binance keys
		"BROKER":                  {}, // "binance" selects direct Binance broker; empty keeps current path
		"BINANCE_API_KEY":         {},
		"BINANCE_API_SECRET":      {},
		"BINANCE_API_BASE":        {},  // default https://api.binance.com
		"BINANCE_RECV_WINDOW_MS":  {},  // default 5000
		"BINANCE_USE_TESTNET":     {},  // ignored unless you opt-in
		"BINANCE_FEE_RATE_PCT":    {},  // NEW: per-exchange fee override for binance
	}

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[len("export "):])
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		// Allow known keys OR any broker-scoped fee var (e.g., BINANCE_FEE_RATE_PCT, KRAKEN_FEE_RATE_PCT)
		if _, ok := needed[key]; !ok {
			if !strings.HasSuffix(key, "_FEE_RATE_PCT") {
				continue
			}
		}
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if idx := strings.Index(val, "#"); idx >= 0 {
			val = strings.TrimSpace(val[:idx])
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
	if err := s.Err(); err != nil {
		log.Printf("env: scan error: %v", err)
	}
	log.Printf("env: loaded %s", path)
}

// --------- Tunable strategy thresholds (initialized in main) ---------

var (
	buyThreshold  float64
	sellThreshold float64
	useMAFilter   bool
)

func initThresholdsFromEnv() {
	buyThreshold = getEnvFloat("BUY_THRESHOLD", 0.55)
	sellThreshold = getEnvFloat("SELL_THRESHOLD", 0.45)
	useMAFilter = getEnvBool("USE_MA_FILTER", true)
}
}}
