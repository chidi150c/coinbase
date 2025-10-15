Generate a full copy of {{
// FILE: broker.go
// Package main – Broker abstractions shared by all execution backends.
//
// This file defines the minimal interface the trading loop needs to talk to a
// market-execution backend (paper or real):
//   - Broker interface: price lookup, place market order by quote USD, fetch candles
//   - Common types: OrderSide, PlacedOrder
//
// Two concrete implementations live in separate files:
//   - broker_paper.go   – in-memory paper broker (no external calls)
//   - broker_bridge.go  – HTTP client for the Python FastAPI sidecar
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

// (Make sure Candle is defined somewhere in your repo. If not, add it here.)
// type Candle struct {
// 	Time   time.Time
// 	Open   float64
// 	High   float64
// 	Low    float64
// 	Close  float64
// 	Volume float64
// }

// PlacedOrder is a normalized view of a filled/placed order (market or limit).
// JSON tags are required because bridges return snake_case fields.
type PlacedOrder struct {
	ID            string    `json:"order_id,omitempty"`
	ProductID     string    `json:"product_id,omitempty"`
	Side          OrderSide `json:"side,omitempty"`
	Price         float64   `json:"price,omitempty"`                // avg/exec price
	BaseSize      float64   `json:"base_size,omitempty"`            // filled base
	QuoteSpent    float64   `json:"quote_spent,omitempty"`          // spent quote
	CommissionUSD float64   `json:"commission_total_usd,string,omitempty"`
	Liquidity     string    `json:"liquidity,omitempty"`            // "M" or "T"
	Fills         []Fill    `json:"fills,omitempty"`
	CreateTime    time.Time `json:"-"` // optional client-side timestamp; not from bridge
	Status        string    `json:"status"`
}

// Fill is optional detail for post-trade analysis.
type Fill struct {
    Price         float64 `json:"price,string,omitempty"`
    BaseSize      float64 `json:"size,string,omitempty"`
    CommissionUSD float64 `json:"commission_usd,string,omitempty"`
	Liquidity     string  `json:"liquidity,omitempty"` // "M" or "T"
}
type ExFilters struct {
	StepSize  float64 // LOT_SIZE.stepSize (quantity)
	TickSize  float64 // PRICE_FILTER.tickSize (price)
}
// Broker is the minimal surface the bot needs to operate.
type Broker interface {
	Name() string
	GetNowPrice(ctx context.Context, product string) (float64, error)
	PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error)
	GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error)
	GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error)
	GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error)

	// Maker-first additions (post-only limit; poll; cancel)
	PlaceLimitPostOnly(ctx context.Context, product string, side OrderSide, limitPrice, baseSize float64) (orderID string, err error)
	GetOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error)
	CancelOrder(ctx context.Context, product, orderID string) error
	GetExchangeFilters(ctx context.Context, product string) (ExFilters, error)
}

}} with only the necessary minimal changes to implement {{extend ExFilters to include PriceTick, BaseStep, QuoteStep, and MinNotional}}. Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()). Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline. Only apply the minimal edits required to implement {{extend ExFilters to include PriceTick, BaseStep, QuoteStep, and MinNotional}}. Return the complete file, copy-paste ready, in IDE.
