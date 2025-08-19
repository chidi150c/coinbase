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
)

func init() {
	prometheus.MustRegister(mtxOrders, mtxDecisions, mtxPnL)
}
