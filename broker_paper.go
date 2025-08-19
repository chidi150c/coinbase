// FILE: broker_paper.go
// Package main – In-memory paper broker (no external dependencies).
//
// This broker simulates execution using the latest known price. It’s used for
// dry runs and backtests. The live loop still pulls real candles/prices from
// the sidecar, but orders here never touch the exchange.
//
// Methods:
//   • Name() string
//   • GetNowPrice(ctx, product) (float64, error)
//   • PlaceMarketQuote(ctx, product, side, quoteUSD) (*PlacedOrder, error)
//   • GetRecentCandles(...) ([]Candle, error)  // unsupported in paper mode
package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PaperBroker keeps a single mutable price used to simulate fills.
type PaperBroker struct {
	price float64
	mu    sync.Mutex
}

func NewPaperBroker() *PaperBroker { return &PaperBroker{} }

func (p *PaperBroker) Name() string { return "paper" }

func (p *PaperBroker) GetNowPrice(ctx context.Context, product string) (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.price <= 0 {
		p.price = 50000 // default bootstrap price if none seen yet
	}
	return p.price, nil
}

// PlaceMarketQuote simulates a market order by converting quoteUSD at current price.
func (p *PaperBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quoteUSD float64) (*PlacedOrder, error) {
	if quoteUSD <= 0 {
		return nil, errors.New("quoteUSD must be > 0")
	}
	price, _ := p.GetNowPrice(ctx, product)
	base := quoteUSD / price
	return &PlacedOrder{
		ID:         uuid.New().String(),
		ProductID:  product,
		Side:       side,
		Price:      price,
		BaseSize:   base,
		QuoteSpent: quoteUSD,
		CreateTime: time.Now().UTC(),
	}, nil
}

// GetRecentCandles is not supported in paper mode; use the bridge for market data.
func (p *PaperBroker) GetRecentCandles(ctx context.Context, product string, granularity string, limit int) ([]Candle, error) {
	return nil, errors.New("paper broker has no candles (use bridge or CSV)")
}
