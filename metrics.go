// FILE: metrics.go
// Package main – Prometheus metrics for observability.
//
// Exposes three primary metrics the bot updates during operation:
//   • bot_orders_total{mode,side}   – Count of orders placed (mode: paper|live)
//   • bot_decisions_total{signal}   – Count of decisions (buy|sell|flat)
//   • bot_equity_usd                – Current equity snapshot (gauge)
//
// These are registered in init() and served by the HTTP handler started in main.go
// at /metrics (Prometheus text exposition format).

package main

import "github.com/prometheus/client_golang/prometheus"

var (
	mtxOrders = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_orders_total",
			Help: "Orders placed",
		},
		[]string{"mode", "side"},
	)

	mtxDecisions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_decisions_total",
			Help: "Decisions taken",
		},
		[]string{"signal"},
	)

	mtxPnL = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "bot_equity_usd",
			Help: "Equity in USD",
		},
	)

	// ---- New metrics (non-breaking; appended) ----

	// bot_model_mode indicates which model path is active; we expose two labeled
	// time series and flip them between 0/1 to keep dashboards simple.
	botModelMode = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bot_model_mode",
			Help: "Model mode indicator (baseline/extended as separate labeled series).",
		},
		[]string{"mode"},
	)

	// bot_vol_risk_factor reports the current multiplicative factor applied to RiskPerTradePct.
	botVolRiskFactor = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "bot_vol_risk_factor",
			Help: "Volatility-adjusted risk factor applied to per-trade sizing.",
		},
	)

	// bot_walk_forward_fits_total counts how many walk-forward refits have occurred.
	botWalkForwardFits = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "bot_walk_forward_fits_total",
			Help: "Number of walk-forward refits performed in live mode.",
		},
	)
)

func init() {
	prometheus.MustRegister(mtxOrders, mtxDecisions, mtxPnL)
	// Register new metrics without altering existing ones.
	prometheus.MustRegister(botModelMode, botVolRiskFactor, botWalkForwardFits)
}

// Helper setters (optional use by other files; do not impact existing behavior)
func SetModelModeMetric(mode string) {
	if mode == "extended" {
		botModelMode.WithLabelValues("extended").Set(1)
		botModelMode.WithLabelValues("baseline").Set(0)
	} else {
		botModelMode.WithLabelValues("baseline").Set(1)
		botModelMode.WithLabelValues("extended").Set(0)
	}
}

func SetVolRiskFactorMetric(v float64) { botVolRiskFactor.Set(v) }
func IncWalkForwardFits()              { botWalkForwardFits.Inc() }
