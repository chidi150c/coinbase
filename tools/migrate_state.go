// tools/migrate_state.go
// CLI to migrate legacy bot state (aggregate Lots) -> SideBooks (BookBuy/BookSell).
//
// Usage:
//   go run tools/migrate_state.go -in <legacy.json> -out <new.json>
//   go run tools/migrate_state.go -in <legacy.json> -inplace
//
// Notes:
// - Preserves equity, model blobs, walk-forward, and side-aware pyramiding memory.
// - Splits legacy Lots into BUY/SELL books; RunnerID=0 if that side has lots, else -1.
// - If a runner lot has zero TrailPeak, seeds it to OpenPrice (safety).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ----- Minimal shared types (compatible with persisted JSON) -----

type OrderSide string

const (
	SideBuy  OrderSide = "BUY"
	SideSell OrderSide = "SELL"
)

type Position struct {
	OpenPrice  float64    `json:"OpenPrice"`
	Side       OrderSide  `json:"Side"`
	SizeBase   float64    `json:"SizeBase"`
	Stop       float64    `json:"Stop"`
	Take       float64    `json:"Take"`
	OpenTime   time.Time  `json:"OpenTime"`
	EntryFee   float64    `json:"EntryFee"`
	TrailActive bool      `json:"TrailActive"`
	TrailPeak  float64    `json:"TrailPeak"`
	TrailStop  float64    `json:"TrailStop"`
	Reason     string     `json:"reason,omitempty"`
}

type SideBook struct {
	RunnerID int          `json:"runner_id"`
	Lots     []*Position  `json:"lots"`
}

// Old (legacy) persisted state. Lots was global aggregate; runnerIdx was NOT persisted.
type OldBotState struct {
	EquityUSD      float64         `json:"EquityUSD"`
	DailyStart     time.Time       `json:"DailyStart"`
	DailyPnL       float64         `json:"DailyPnL"`
	Lots           []*Position     `json:"Lots"`          // legacy aggregate
	Model          json.RawMessage `json:"Model"`         // keep opaque
	MdlExt         json.RawMessage `json:"MdlExt"`        // keep opaque
	WalkForwardMin int             `json:"WalkForwardMin"`
	LastFit        time.Time       `json:"LastFit"`

	// These may or may not exist in older files; tolerate absence.
	LastAddBuy      *time.Time `json:"LastAddBuy,omitempty"`
	LastAddSell     *time.Time `json:"LastAddSell,omitempty"`
	WinLowBuy       *float64   `json:"WinLowBuy,omitempty"`
	WinHighSell     *float64   `json:"WinHighSell,omitempty"`
	LatchedGateBuy  *float64   `json:"LatchedGateBuy,omitempty"`
	LatchedGateSell *float64   `json:"LatchedGateSell,omitempty"`
}

// New state schema (SideBooks only + side-aware memory).
type NewBotState struct {
	EquityUSD      float64         `json:"EquityUSD"`
	DailyStart     time.Time       `json:"DailyStart"`
	DailyPnL       float64         `json:"DailyPnL"`
	Model          json.RawMessage `json:"Model"`
	MdlExt         json.RawMessage `json:"MdlExt"`
	WalkForwardMin int             `json:"WalkForwardMin"`
	LastFit        time.Time       `json:"LastFit"`

	BookBuy  SideBook `json:"BookBuy"`
	BookSell SideBook `json:"BookSell"`

	LastAddBuy      time.Time `json:"LastAddBuy"`
	LastAddSell     time.Time `json:"LastAddSell"`
	WinLowBuy       float64   `json:"WinLowBuy"`
	WinHighSell     float64   `json:"WinHighSell"`
	LatchedGateBuy  float64   `json:"LatchedGateBuy"`
	LatchedGateSell float64   `json:"LatchedGateSell"`
}

func main() {
	in := flag.String("in", "", "path to legacy state JSON")
	out := flag.String("out", "", "path to write migrated state JSON (ignored if -inplace)")
	inplace := flag.Bool("inplace", false, "overwrite input file in place (creates .bak)")
	flag.Parse()

	if *in == "" {
		exitf("missing -in <file>")
	}
	if !*inplace && *out == "" {
		exitf("either specify -out <file> or use -inplace")
	}

	raw, err := os.ReadFile(*in)
	if err != nil {
		exitf("read input: %v", err)
	}

	var old OldBotState
	if err := json.Unmarshal(raw, &old); err != nil {
		exitf("parse legacy JSON: %v", err)
	}

	// Partition legacy Lots by side.
	var buyLots, sellLots []*Position
	for _, p := range old.Lots {
		if p == nil {
			continue
		}
		switch normalizeSide(p.Side) {
		case SideBuy:
			buyLots = append(buyLots, p)
		case SideSell:
			sellLots = append(sellLots, p)
		default:
			// If unknown, skip silently (or route to BUY as a conservative default).
			continue
		}
	}

	bookBuy := SideBook{RunnerID: -1, Lots: buyLots}
	bookSell := SideBook{RunnerID: -1, Lots: sellLots}

	// Default runner per side: index 0 if that side has lots (mirrors old loader default).
	if len(bookBuy.Lots) > 0 {
		bookBuy.RunnerID = 0
		seedRunnerTrail(bookBuy.Lots[0])
	}
	if len(bookSell.Lots) > 0 {
		bookSell.RunnerID = 0
		seedRunnerTrail(bookSell.Lots[0])
	}

	// Carry forward side-aware memory if present; else zero values.
	nb := NewBotState{
		EquityUSD:      old.EquityUSD,
		DailyStart:     old.DailyStart,
		DailyPnL:       old.DailyPnL,
		Model:          old.Model,
		MdlExt:         old.MdlExt,
		WalkForwardMin: old.WalkForwardMin,
		LastFit:        old.LastFit,

		BookBuy:  bookBuy,
		BookSell: bookSell,
	}

	// Optional fields (populate if present)
	if old.LastAddBuy != nil {
		nb.LastAddBuy = *old.LastAddBuy
	}
	if old.LastAddSell != nil {
		nb.LastAddSell = *old.LastAddSell
	}
	if old.WinLowBuy != nil {
		nb.WinLowBuy = *old.WinLowBuy
	}
	if old.WinHighSell != nil {
		nb.WinHighSell = *old.WinHighSell
	}
	if old.LatchedGateBuy != nil {
		nb.LatchedGateBuy = *old.LatchedGateBuy
	}
	if old.LatchedGateSell != nil {
		nb.LatchedGateSell = *old.LatchedGateSell
	}

	// Marshal pretty for readability.
	outBytes, err := json.MarshalIndent(nb, "", " ")
	if err != nil {
		exitf("marshal new JSON: %v", err)
	}

	// Write output (in place or new file)
	if *inplace {
		backup := *in + ".bak"
		if err := copyFile(*in, backup); err != nil {
			exitf("create backup: %v", err)
		}
		if err := os.WriteFile(*in, outBytes, 0644); err != nil {
			exitf("write new state: %v", err)
		}
		fmt.Printf("Migrated in-place. Backup: %s\n", backup)
	} else {
		// Ensure dir exists
		if err := os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
			exitf("ensure out dir: %v", err)
		}
		if err := os.WriteFile(*out, outBytes, 0644); err != nil {
			exitf("write out: %v", err)
		}
		fmt.Printf("Migrated state written to: %s\n", *out)
	}
}

func normalizeSide(s OrderSide) OrderSide {
	switch stringsToUpper(string(s)) {
	case "BUY":
		return SideBuy
	case "SELL":
		return SideSell
	default:
		return OrderSide(stringsToUpper(string(s)))
	}
}

func seedRunnerTrail(p *Position) {
	if p == nil {
		return
	}
	if p.TrailPeak == 0 {
		p.TrailPeak = p.OpenPrice
	}
	// Do not force TrailActive/TrailStop; runtime logic will manage them.
}

// small helpers

func stringsToUpper(s string) string {
	// local tiny impl to avoid importing strings just for ToUpper
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] = b[i] - 'a' + 'A'
		}
	}
	return string(b)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0644)
}

func exitf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "migrate_state: "+format+"\n", a...)
	os.Exit(1)
}
