// FILE: trader.go
// Package main – Position/risk management and the synchronized trading loop.
//
// What’s here:
//   • Position state (open price/side/size/stop/take)
//   • Trader: holds config, broker, model, equity/PnL, and mutex
//   • step(): the core synchronized tick that may OPEN, HOLD, or EXIT
//
// Concurrency design:
//   - We take the trader mutex to read/update in-memory state,
//     but RELEASE the lock around any network I/O (placing orders,
//     fetching prices via the broker). That prevents stalls/blocking.
//   - On EXIT, we actually place a closing market order (unless DryRun).
//
// Safety:
//   - Daily circuit breaker: MaxDailyLossPct
//   - Long-only guard (Config.LongOnly): prevents new SELL entries on spot
//   - OrderMinUSD floor and proportional risk per trade

package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---- Position & Trader ----

type Position struct {
	OpenPrice float64
	Side      OrderSide
	SizeBase  float64
	Stop      float64
	Take      float64
	OpenTime  time.Time
}

type Trader struct {
	cfg        Config
	broker     Broker
	model      *AIMicroModel
	pos        *Position
	dailyStart time.Time
	dailyPnL   float64
	mu         sync.Mutex
	equityUSD  float64
}

func NewTrader(cfg Config, broker Broker, model *AIMicroModel) *Trader {
	return &Trader{
		cfg:        cfg,
		broker:     broker,
		model:      model,
		equityUSD:  cfg.USDEquity,
		dailyStart: midnightUTC(time.Now().UTC()),
	}
}

func (t *Trader) EquityUSD() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.equityUSD
}

// SetEquityUSD safely updates trader equity and the equity metric.
func (t *Trader) SetEquityUSD(v float64) {
	t.mu.Lock()
	t.equityUSD = v
	t.mu.Unlock()

	// update the metric with same naming style
	mtxPnL.Set(v)
}

func midnightUTC(ts time.Time) time.Time {
	y, m, d := ts.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (t *Trader) updateDaily(date time.Time) {
	if midnightUTC(date) != t.dailyStart {
		t.dailyStart = midnightUTC(date)
		t.dailyPnL = 0
	}
}

func (t *Trader) canTrade() bool {
	limit := t.cfg.MaxDailyLossPct / 100.0 * t.equityUSD
	return t.dailyPnL > -limit
}

// ---- Core tick ----

// step consumes the current candle history and may place/close a position.
// It returns a human-readable status string for logging.
func (t *Trader) step(ctx context.Context, c []Candle) (string, error) {
	if len(c) == 0 {
		return "NO_DATA", nil
	}

	// Acquire lock (no defer): we will release it around network calls.
	t.mu.Lock()

	now := c[len(c)-1].Time
	t.updateDaily(now)

	// Keep paper broker price in sync with the latest close so paper fills are realistic.
	if pb, ok := t.broker.(*PaperBroker); ok {
		if len(c) > 0 {
			pb.mu.Lock()
			pb.price = c[len(c)-1].Close
			pb.mu.Unlock()
		}
	}

	// If we have an open position, check stop/take and close if triggered.
	if t.pos != nil {
		price := c[len(c)-1].Close
		triggerExit := false
		if t.pos.Side == SideBuy && (price <= t.pos.Stop || price >= t.pos.Take) {
			triggerExit = true
		}
		if t.pos.Side == SideSell && (price >= t.pos.Stop || price <= t.pos.Take) {
			triggerExit = true
		}

		if triggerExit {
			closeSide := SideSell
			if t.pos.Side == SideSell {
				closeSide = SideBuy
			}
			base := t.pos.SizeBase
			quote := base * price

			// Release lock before network I/O (placing the closing order).
			t.mu.Unlock()
			if !t.cfg.DryRun {
				if _, err := t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, closeSide, quote); err != nil {
					return "", fmt.Errorf("close order failed: %w", err)
				}
				mtxOrders.WithLabelValues("live", string(closeSide)).Inc()
			}

			// Re-lock and update state.
			t.mu.Lock()
			price = c[len(c)-1].Close // refresh snapshot after I/O

			pl := (price - t.pos.OpenPrice) * base
			if t.pos.Side == SideSell {
				pl = (t.pos.OpenPrice - price) * base
			}
			t.dailyPnL += pl
			t.equityUSD += pl
			t.pos = nil

			msg := fmt.Sprintf("EXIT %s at %.2f P/L=%.2f", now.Format(time.RFC3339), price, pl)
			t.mu.Unlock()
			return msg, nil
		}

		// Still in position; nothing to do this tick.
		t.mu.Unlock()
		return "HOLD", nil
	}

	// Flat: enforce daily loss circuit breaker.
	if !t.canTrade() {
		t.mu.Unlock()
		return "CIRCUIT_BREAKER_DAILY_LOSS", nil
	}

	// Make a decision.
	d := decide(c, t.model)
	mtxDecisions.WithLabelValues(signalLabel(d.Signal)).Inc()

	// Long-only: veto SELL entries when flat (spot constraint).
	if d.Signal == Sell && t.cfg.LongOnly {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT (long-only) [%s]", d.Reason), nil
	}
	if d.Signal == Flat {
		t.mu.Unlock()
		return fmt.Sprintf("FLAT [%s]", d.Reason), nil
	}

	// Sizing
	price := c[len(c)-1].Close
	quote := (t.cfg.RiskPerTradePct / 100.0) * t.equityUSD
	if quote < t.cfg.OrderMinUSD {
		quote = t.cfg.OrderMinUSD
	}
	base := quote / price

	// Stops/takes
	stop := price * (1.0 - t.cfg.StopLossPct/100.0)
	take := price * (1.0 + t.cfg.TakeProfitPct/100.0)
	side := d.SignalToSide()
	if side == SideSell {
		stop = price * (1.0 + t.cfg.StopLossPct/100.0)
		take = price * (1.0 - t.cfg.TakeProfitPct/100.0)
	}

	// Release lock around live network I/O.
	t.mu.Unlock()
	if !t.cfg.DryRun {
		if _, err := t.broker.PlaceMarketQuote(ctx, t.cfg.ProductID, side, quote); err != nil {
			return "", err
		}
		mtxOrders.WithLabelValues("live", string(side)).Inc()
	}

	// Re-lock to mutate state.
	t.mu.Lock()
	t.pos = &Position{
		OpenPrice: price,
		Side:      side,
		SizeBase:  base,
		Stop:      stop,
		Take:      take,
		OpenTime:  now,
	}
	msg := ""
	if t.cfg.DryRun {
		mtxOrders.WithLabelValues("paper", string(side)).Inc()
		msg = fmt.Sprintf("PAPER %s quote=%.2f base=%.6f stop=%.2f take=%.2f [%s]",
			d.Signal, quote, base, stop, take, d.Reason)
	} else {
		msg = fmt.Sprintf("LIVE ORDER %s quote=%.2f stop=%.2f take=%.2f [%s]",
			d.Signal, quote, stop, take, d.Reason)
	}
	t.mu.Unlock()
	return msg, nil
}

// ---- labels ----

func signalLabel(s Signal) string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	default:
		return "flat"
	}
}
