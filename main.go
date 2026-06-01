// FILE: main.go
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
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var csvBacktest string
	var live bool
	var intervalSec int
	var mine3m bool
	var mineLimit int
	var mineTF string

	flag.StringVar(&csvBacktest, "backtest", "", "Path to CSV (time,open,high,low,close,volume)")
	flag.BoolVar(&live, "live", false, "Run live loop (ignores -backtest)")
	flag.IntVar(&intervalSec, "interval", 60, "Live loop interval in seconds")
	flag.BoolVar(&mine3m, "mine3m", false, "Backfill/mine 3-minute labels and exit")
	flag.IntVar(&mineLimit, "limit", 50000, "Number of 1-minute candles to backfill for mining")
	flag.StringVar(&mineTF, "mine-tf", "", "Backfill/mine labels for timeframe: 3m,5m,15m,30m,1h")
	flag.Parse()

	loadBotEnv()
	cfg := loadConfigFromEnv()

	var broker Broker
	br := getEnv("BROKER", "")
	switch strings.ToLower(br) {
	case "binance":
		broker = NewBinanceBridge(cfg.BridgeURL)
	case "bridge":
		broker = NewBridgeBroker(cfg.BridgeURL)
	default:
		log.Fatalf("No Broker %s !!!!!!!!!", br)
	}

	if mine3m || mineTF != "" {
		if mineTF == "" {
			mineTF = "3m"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := runMineTF(ctx, cfg, broker, mineTF, mineLimit); err != nil {
			log.Fatalf("mine3m failed: %v", err)
		}
		return
	}

	trader := NewTrader(cfg, broker)

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer bootCancel()
	trader.RehydratePending(bootCtx, RehydrateModeResume)

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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if csvBacktest != "" && !live {
		runBacktest(ctx, csvBacktest, trader)
	} else {
		runLive(ctx, trader, intervalSec)
	}

	shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	_ = srv.Shutdown(shutdownCtx)
}
