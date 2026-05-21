// FILE: metrics.go
// Package main – Prometheus metrics for observability.
//
// Exposes primary metrics the bot updates during operation:
//   • bot_orders_total{mode,side}        – Count of orders placed (mode: paper|live)
//   • bot_decisions_total{signal}        – Count of decisions (buy|sell|flat)
//   • bot_equity_usd                     – Current equity snapshot (gauge)
//   • bot_trades_total{result}           – Trades by result (open|win|loss)
//   • bot_exit_reasons_total{reason,side} – Exits split by reason and side
//   • bot_vol_risk_factor                – Volatility-adjusted risk factor
//   • bot_walk_forward_fits_total        – Count of walk-forward refits
//   • bot_limit_orders_*_total{side}     – Post-only limit flow metrics
//
// The old bot_model_mode{mode} metric was removed because the AI architecture
// now has one unified logistic model path only.

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

	// bot_vol_risk_factor reports the current multiplicative factor applied to risk sizing.
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

	// --- Post-only limit flow ---
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
	prometheus.MustRegister(botVolRiskFactor, botWalkForwardFits)

	// Register post-only metrics
	prometheus.MustRegister(limitPlaced, limitFilled, limitTimeout)
}

// Helper setters
func SetVolRiskFactorMetric(v float64) { botVolRiskFactor.Set(v) }
func IncWalkForwardFits()              { botWalkForwardFits.Inc() }

// Limit-flow helpers
func IncLimitPlaced(side string)  { limitPlaced.WithLabelValues(side).Inc() }
func IncLimitFilled(side string)  { limitFilled.WithLabelValues(side).Inc() }
func IncLimitTimeout(side string) { limitTimeout.WithLabelValues(side).Inc() }
