// FILE: main.go
// Package main – Program entrypoint and HTTP/metrics server.
//
// Boot sequence:
//   1) loadBotEnv()                – read .env (no shell exports required)
//   2) initThresholdsFromEnv()     – tune BUY/SELL thresholds & MA filter
//   3) cfg := loadConfigFromEnv()  – build runtime Config
//   4) wire broker/model/trader
//   5) start Prometheus /healthz server on cfg.Port
//   6) runBacktest or runLive based on flags
//
// Flags:
//   -backtest <csv>   Run a simple backtest using CSV candles
//   -live             Run the real-time loop (default cadence 60s)
//   -interval <sec>   Live loop interval in seconds (default 60)
//
// Example:
//   go run . -live -interval 15
//
// Notes:
//   - The FastAPI sidecar must be running for live mode (bridge URL in .env).
//   - No environment exports are needed; keep editing .env and restart.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// ---- Flags ----
	var csvBacktest string
	var live bool
	var intervalSec int
	flag.StringVar(&csvBacktest, "backtest", "", "Path to CSV (time,open,high,low,close,volume)")
	flag.BoolVar(&live, "live", false, "Run live loop (ignores -backtest)")
	flag.IntVar(&intervalSec, "interval", 60, "Live loop interval in seconds")
	flag.Parse()

	// ---- Environment & Config ----
	loadBotEnv()
	initThresholdsFromEnv()
	cfg := loadConfigFromEnv()

	// ---- Broker wiring ----
	var broker Broker
	if cfg.BridgeURL != "" {
		broker = NewBridgeBroker(cfg.BridgeURL)
	} else {
		broker = NewPaperBroker()
	}

	model := newModel()
	trader := NewTrader(cfg, broker, model)

	// ---- HTTP metrics/health ----
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port), Handler: mux}
	go func() {
		log.Printf("serving metrics on :%d/metrics", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// ---- Run selected mode ----
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if csvBacktest != "" && !live {
+        // Start /metrics & /healthz in backtest too so Prometheus can scrape
+        go serveHTTPMetricsAndHealthz()  // <- whatever your metrics/http starter is named
+        runBacktest(ctx, csvBacktest, trader, model)
+        return
+    } else {
		runLive(ctx, trader, model, intervalSec)
	}

	// ---- Graceful shutdown for HTTP server ----
	shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	_ = srv.Shutdown(shutdownCtx)
}
