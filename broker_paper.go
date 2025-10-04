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
//   • GetAvailableBase(...) (string, float64, float64, error)   // env-based
//   • GetAvailableQuote(...) (string, float64, float64, error)  // env-based
//
// Minimal additions for maker-first routing compatibility:
//   • PlaceLimitPostOnly(ctx, product, side, limitPrice, baseSize) (string, error)  // returns "not supported"
//   • GetOrder(ctx, product, orderID) (*PlacedOrder, error)                          // returns "not supported"
//   • CancelOrder(ctx, product, orderID) error                                       // returns "not supported"
package main

import (
	"context"
	"errors"
	"strings"
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
		p.price = 108000 // default bootstrap price if none seen yet
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

// GetAvailableBase returns env-driven paper balances for the base asset.
// - asset: parsed from product "BASE-QUOTE" (fallback to BASE_ASSET env if parsing fails)
// - available: PAPER_BASE_BALANCE
// - step: BASE_STEP
func (p *PaperBroker) GetAvailableBase(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	base, _ := parseProductSymbols(product)
	if strings.TrimSpace(base) == "" {
		base = strings.TrimSpace(baseAssetOverride()) // fallback for paper
	}
	return base, paperBaseBalance(), baseStepOverride(), nil
}

// GetAvailableQuote returns env-driven paper balances for the quote asset.
// - asset: parsed from product "BASE-QUOTE"
// - available: PAPER_QUOTE_BALANCE
// - step: QUOTE_STEP
func (p *PaperBroker) GetAvailableQuote(ctx context.Context, product string) (asset string, available float64, step float64, err error) {
	_, quote := parseProductSymbols(product)
	return quote, paperQuoteBalance(), quoteStepOverride(), nil
}

// --- Maker-first additions (stubs to satisfy the Broker interface) ---

func (p *PaperBroker) PlaceLimitPostOnly(ctx context.Context, product string, side OrderSide, limitPrice, baseSize float64) (string, error) {
	return "", errors.New("limit_post_only not supported on paper")
}

func (p *PaperBroker) GetOrder(ctx context.Context, product, orderID string) (*PlacedOrder, error) {
	return nil, errors.New("get order not supported on paper")
}

func (p *PaperBroker) CancelOrder(ctx context.Context, product, orderID string) error {
	return errors.New("cancel not supported on paper")
}

// parseProductSymbols splits a product like "BTC-USD" into ("BTC", "USD").
func parseProductSymbols(product string) (base string, quote string) {
	product = strings.TrimSpace(product)
	parts := strings.Split(product, "-")
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	// unknown format
	return "", ""
}
