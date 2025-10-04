// FILE: metrics.go
// Package main – Prometheus metrics for observability.
//
// Exposes primary metrics the bot updates during operation:
//   • bot_orders_total{mode,side}   – Count of orders placed (mode: paper|live)
//   • bot_decisions_total{signal}   – Count of decisions (buy|sell|flat)
//   • bot_equity_usd                – Current equity snapshot (gauge)
//   • bot_trades_total{result}      – Trades by result (open|win|loss)
//   • bot_exit_reasons_total{reason,side} – Exits split by reason and side
//   • bot_model_mode{mode}          – Model mode indicator (baseline/extended)
//   • bot_vol_risk_factor           – Volatility-adjusted risk factor
//   • bot_walk_forward_fits_total   – Count of walk-forward refits
//   • bot_limit_orders_*_total{side} – Post-only limit flow metrics (placed/filled/timeout)
//
// These are registered in init() and served by the HTTP handler started in main.go
// at /metrics (Prometheus text exposition format).

package main

import "github.com/prometheus/client_golang/prometheus"

var (
	// --- Existing metrics ---
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

	// Counts exits split by reason; reasons are things like take_profit, stop_loss, trailing_stop, other.
	mtxExitReasons = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_exit_reasons_total",
			Help: "Total exits split by reason and side",
		},
		[]string{"reason", "side"}, // side: buy|sell (the side of the CLOSED lot)
	)

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

	// bot_trades_total counts trades by result (open|win|loss).
	mtxTrades = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_trades_total",
			Help: "Trades counted by result (open|win|loss).",
		},
		[]string{"result"},
	)

	// --- New: post-only limit flow ---
	limitPlaced = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_limit_orders_placed_total",
			Help: "Count of post-only limit orders placed",
		},
		[]string{"side"}, // BUY|SELL
	)

	limitFilled = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_limit_orders_filled_total",
			Help: "Count of post-only limit orders filled",
		},
		[]string{"side"}, // BUY|SELL
	)

	limitTimeout = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bot_limit_orders_timeout_total",
			Help: "Count of post-only limit orders that timed out and fell back to market",
		},
		[]string{"side"}, // BUY|SELL
	)
)

func init() {
	// Register existing
	prometheus.MustRegister(mtxOrders, mtxDecisions, mtxPnL)
	prometheus.MustRegister(mtxTrades)
	prometheus.MustRegister(mtxExitReasons)
	prometheus.MustRegister(botModelMode, botVolRiskFactor, botWalkForwardFits)

	// Register new post-only metrics
	prometheus.MustRegister(limitPlaced, limitFilled, limitTimeout)
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

// New helpers for the limit flow
func IncLimitPlaced(side string)  { limitPlaced.WithLabelValues(side).Inc() }
func IncLimitFilled(side string)  { limitFilled.WithLabelValues(side).Inc() }
func IncLimitTimeout(side string) { limitTimeout.WithLabelValues(side).Inc() }
