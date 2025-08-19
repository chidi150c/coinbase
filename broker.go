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
	ID         string
	ProductID  string
	Side       OrderSide
	Price      float64 // average/assumed execution price
	BaseSize   float64 // filled base (e.g., BTC)
	QuoteSpent float64 // spent quote (e.g., USD)
	CreateTime time.Time
}

// Broker is the minimal surface the bot needs to operate.
type Broker interface {
	Name() string
	GetNowPrice(ctx context.Context, product string) (float64, error)
	PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error)
	GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error)
}
