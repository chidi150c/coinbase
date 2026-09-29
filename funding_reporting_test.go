package main

import (
 "context"
 "encoding/json"
 "math"
 "os"
 "path/filepath"
 "testing"
 "time"
)

func fundingTestTrader(t *testing.T) *Trader {
 t.Helper()
 return &Trader{cfg: Config{PersistState: true}, stateFile: filepath.Join(t.TempDir(), "state.json"),
  books: map[OrderSide]*SideBook{SideBuy: {}, SideSell: {}},
  resourceManager: NewResourceManager(ResourceLedgerState{Reservations: map[string]ResourceReservation{
   "quote": {ID:"quote", QuoteUSD:20}, "base": {ID:"base", Base:0.2},
  }}), fundingMarkPrice:100}
}

func TestFundingReportMatchesAllocatorAndDoesNotMutateBookkeeping(t *testing.T) {
 tr := fundingTestTrader(t)
 tr.SpareBuyUSD, tr.SpareSellUSD = 999, 888
 now := time.Now()
 tr.setBalanceSnapshot(balanceSnapshot{SymQuote:"USDT", SymBase:"BTC", AvailQuote:100, AvailBase:1, QuoteStep:.01, BaseStep:.001, UpdatedAt:now})
 tr.mu.Lock()
 quote, base := tr.producerResourceReservationsLocked()
 allocation, valid := tr.buildResourceSnapshotLocked(balanceSnapshotMaxAge, quote, base, 100, 10)
 report := tr.fundingReportLocked(now)
 if !valid || !report.Fresh || report.SpareBuyUSD != allocation.SpareQuote || math.Abs(report.SpareSellUSD-allocation.SpareBase*allocation.Price)>1e-9 { t.Fatalf("report=%+v allocation=%+v",report,allocation) }
 bs, _, err := tr.encodeStateLocked()
 tr.mu.Unlock()
 if err != nil { t.Fatal(err) }
 var saved BotState
 if err := json.Unmarshal(bs,&saved); err != nil { t.Fatal(err) }
 if saved.SpareBuyUSD != 80 || math.Abs(saved.SpareSellUSD-80)>1e-9 { t.Fatalf("saved spares %+v",saved.FundingSnapshot) }
 if tr.SpareBuyUSD != 999 || tr.SpareSellUSD != 888 { t.Fatal("runtime bookkeeping changed") }
 tr.mu.RLock()
 stale := tr.fundingReportLocked(now.Add(4*time.Second))
 tr.mu.RUnlock()
 if stale.Fresh || stale.SpareBuyUSD != 0 || stale.SpareSellUSD != 0 { t.Fatalf("stale report=%+v",stale) }
}

func TestStateWriteRejectsOlderSnapshotAndOwnsEncodedPositions(t *testing.T) {
 tr := fundingTestTrader(t)
 tr.book(SideBuy).Lots = []*Position{{OpenPrice:100}}
 tr.mu.Lock()
 old, oldSeq, err := tr.encodeStateLocked()
 if err != nil { tr.mu.Unlock(); t.Fatal(err) }
 tr.book(SideBuy).Lots[0].OpenPrice = 200
 newer, newSeq, err := tr.encodeStateLocked()
 tr.mu.Unlock()
 if err != nil { t.Fatal(err) }
 if err := tr.writeStateBytes(newer,newSeq); err != nil { t.Fatal(err) }
 if err := tr.writeStateBytes(old,oldSeq); err != nil { t.Fatal(err) }
 stored, err := os.ReadFile(tr.stateFile)
 if err != nil { t.Fatal(err) }
 var saved, detached BotState
 if err := json.Unmarshal(stored,&saved); err != nil { t.Fatal(err) }
 if err := json.Unmarshal(old,&detached); err != nil { t.Fatal(err) }
 if saved.BookBuy.Lots[0].OpenPrice != 200 || detached.BookBuy.Lots[0].OpenPrice != 100 { t.Fatal("snapshot ordering or ownership failed") }
}

func TestBackgroundFundingSaveWithoutEntryAndRestart(t *testing.T) {
 tr := fundingTestTrader(t)
 tr.setBalanceSnapshot(balanceSnapshot{SymQuote:"USDT",SymBase:"BTC",AvailQuote:100,AvailBase:1,QuoteStep:.01,BaseStep:.001,UpdatedAt:time.Now()})
 ctx,cancel := context.WithCancel(context.Background())
 defer cancel()
 tr.startReportSaver(ctx)
 for i:=0;i<100;i++ { tr.requestReportSave() }
 deadline := time.Now().Add(3*time.Second)
 for {
  if _,err := os.Stat(tr.stateFile); err == nil { break }
  if time.Now().After(deadline) { t.Fatal("background save did not run without an entry") }
  time.Sleep(10*time.Millisecond)
 }
 stored,err := os.ReadFile(tr.stateFile)
 if err != nil { t.Fatal(err) }
 var saved BotState
 if err := json.Unmarshal(stored,&saved); err != nil { t.Fatal(err) }
 if !saved.FundingSnapshot.Fresh || saved.SpareBuyUSD != 80 || len(saved.ResourceLedger.Reservations)!=2 { t.Fatalf("bad funding/reservation persistence: %+v",saved.FundingSnapshot) }
 restored := fundingTestTrader(t)
 restored.stateFile = tr.stateFile
 if err := restored.loadState(); err != nil { t.Fatal(err) }
 q,b := restored.producerResourceReservationsLocked()
 if q != 20 || b != .2 { t.Fatalf("restart lost reservations: %v %v",q,b) }
}

func TestConcurrentReportingAndLifecycleSaves(t *testing.T) {
 tr := fundingTestTrader(t)
 ctx,cancel:=context.WithCancel(context.Background())
 defer cancel()
 tr.startReportSaver(ctx)
 done:=make(chan struct{})
 go func(){
  defer close(done)
  for i:=0;i<100;i++ {
   tr.setBalanceSnapshot(balanceSnapshot{SymQuote:"USDT",SymBase:"BTC",AvailQuote:100,AvailBase:1,QuoteStep:.01,BaseStep:.001,UpdatedAt:time.Now()})
   tr.requestReportSave()
   if err:=tr.saveState(); err!=nil { t.Error(err); return }
  }
 }()
 for i:=0;i<100;i++ {
  tr.mu.Lock()
  tr.dailyPnL=float64(i)
  err:=tr.saveStateNoLock()
  tr.mu.Unlock()
  if err!=nil { t.Fatal(err) }
 }
 <-done
 if err:=tr.saveState(); err!=nil { t.Fatal(err) }
 bs,err:=os.ReadFile(tr.stateFile)
 if err!=nil { t.Fatal(err) }
 var saved BotState
 if err:=json.Unmarshal(bs,&saved); err!=nil { t.Fatal(err) }
 if saved.DailyPnL!=99 { t.Fatalf("newer lifecycle state lost: %v",saved.DailyPnL) }
}
