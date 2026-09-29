package main

import (
 "context"
 "encoding/json"
 "log"
 "math"
 "os"
 "sync/atomic"
 "time"
)

// FundingReport is observational; the cache and reservation ledger remain
// authoritative. Refund obligations are not additional cash or inventory.
type FundingReport struct {
 ObservedAt time.Time `json:"observed_at"`
 BalanceUpdatedAt time.Time `json:"balance_updated_at"`
 BalanceAgeMS int64 `json:"balance_age_ms"`
 Fresh bool `json:"fresh"`
 MarkPrice float64 `json:"mark_price"`
 AvailableQuote float64 `json:"available_quote"`
 AvailableBase float64 `json:"available_base"`
 ReservedQuote float64 `json:"reserved_quote"`
 ReservedBase float64 `json:"reserved_base"`
 SpareBuyUSD float64 `json:"spare_buy_usd"`
 SpareSellUSD float64 `json:"spare_sell_usd"`
}

// Caller holds t.mu. Use the allocator's cache, ledger totals and formula.
func (t *Trader) fundingReportLocked(now time.Time) FundingReport {
 balance, valid := t.getBalanceSnapshot(0)
 quote, base := t.producerResourceReservationsLocked()
 report := FundingReport{ObservedAt: now.UTC(), BalanceUpdatedAt: balance.UpdatedAt,
  MarkPrice: t.fundingMarkPrice, AvailableQuote: balance.AvailQuote,
  AvailableBase: balance.AvailBase, ReservedQuote: quote, ReservedBase: base}
 if !balance.UpdatedAt.IsZero() { report.BalanceAgeMS = now.Sub(balance.UpdatedAt).Milliseconds() }
 report.Fresh = valid && !balance.UpdatedAt.After(now) && now.Sub(balance.UpdatedAt) <= balanceSnapshotMaxAge && report.MarkPrice > 0
 if report.Fresh {
  report.SpareBuyUSD = math.Max(0, balance.AvailQuote-quote)
  report.SpareSellUSD = math.Max(0, balance.AvailBase-base)*report.MarkPrice
 }
 return report
}

// Encoding under t.mu detaches mutable maps, slices and positions before IO.
func (t *Trader) encodeStateLocked() ([]byte, uint64, error) {
 seq := atomic.AddUint64(&t.stateSequence, 1)
 bs, err := json.MarshalIndent(t.snapshotStateLocked(), "", " ")
 return bs, seq, err
}

func (t *Trader) writeStateBytes(bs []byte, seq uint64) error {
 t.statePersistMu.Lock()
 defer t.statePersistMu.Unlock()
 if seq <= t.stateWrittenSequence { return nil }
 tmp := t.stateFile + ".tmp"
 if err := os.WriteFile(tmp, bs, 0644); err != nil { return err }
 if err := os.Rename(tmp, t.stateFile); err != nil { return err }
 t.stateWrittenSequence = seq
 return nil
}

func (t *Trader) requestReportSave() {
 if t.stateFile == "" || !t.cfg.PersistState || t.reportSaveCh == nil { return }
 select { case t.reportSaveCh <- struct{}{}: default: }
}

// Refresh requests are coalesced. Never wait for the trading lock; retry on
// the next worker cadence. Disk IO runs without t.mu.
func (t *Trader) startReportSaver(ctx context.Context) {
 t.reportSaveOnce.Do(func() {
  t.reportSaveCh = make(chan struct{}, 1)
  go func() {
   ticker := time.NewTicker(time.Second)
   defer ticker.Stop()
   pending := false
   for {
    select {
    case <-ctx.Done(): return
    case <-t.reportSaveCh: pending = true
    case <-ticker.C:
     if !pending { continue }
     if !t.mu.TryRLock() { continue }
     bs, seq, err := t.encodeStateLocked()
     t.mu.RUnlock()
     if err == nil { err = t.writeStateBytes(bs, seq) }
     if err != nil { log.Printf("[WARN] funding.report.save_failed err=%v", err); continue }
     pending = false
    }
   }
  }()
 })
}
