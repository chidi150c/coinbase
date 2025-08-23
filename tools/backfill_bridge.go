// Fetch candles from the FastAPI bridge and write CSV for backtests.
//
// Usage examples:
//   # In Docker Compose (inside the same network as the bridge):
//   docker compose run --rm bot go run ./tools/backfill_bridge.go \
//     -product BTC-USD -granularity ONE_MINUTE -limit 300 -out data/BTC-USD.csv
//
//   # On host (if bridge is published on localhost:8787):
//   BRIDGE_URL=http://localhost:8787 go run ./tools/backfill_bridge.go \
//     -product BTC-USD -granularity ONE_MINUTE -limit 300 -out data/BTC-USD.csv
//
// Notes:
// - Bridge /candles returns a list of objects with fields: start (UNIX seconds, string),
//   open, high, low, close, volume (strings). We convert start -> RFC3339 for the CSV.
// - The CSV header is: time,open,high,low,close,volume (what your backtest loader wants).
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

type candleRow struct {
	Start  string `json:"start"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}

func main() {
	var (
		product     = flag.String("product", "BTC-USD", "Product ID (e.g., BTC-USD)")
		granularity = flag.String("granularity", "ONE_MINUTE", "Granularity (e.g., ONE_MINUTE)")
		limit       = flag.Int("limit", 300, "Candles to fetch (API max typically 350)")
		outPath     = flag.String("out", "data/BTC-USD.csv", "Output CSV path")
	)
	flag.Parse()

	bridgeURL := getenv("BRIDGE_URL", "http://bridge:8787") // default matches Compose service name
	url := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d",
		trimRightSlash(bridgeURL), *product, *granularity, *limit)

	resp, err := http.Get(url)
	if err != nil {
		panic(fmt.Errorf("GET %s: %w", url, err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		panic(fmt.Errorf("bridge /candles status %d", resp.StatusCode))
	}

	// The bridge returns a JSON array of objects; tolerate {"candles":[...]} too.
	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		panic(fmt.Errorf("decode JSON: %w", err))
	}
	rows := normalizeList(raw)
	if len(rows) == 0 {
		panic("no candles returned")
	}

	// Sort by time ascending
	sort.Slice(rows, func(i, j int) bool {
		ti, _ := strconv.ParseInt(rows[i].Start, 10, 64)
		tj, _ := strconv.ParseInt(rows[j].Start, 10, 64)
		return ti < tj
	})

	// Write CSV with RFC3339 timestamps
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		panic(err)
	}
	f, err := os.Create(*outPath)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{"time", "open", "high", "low", "close", "volume"}); err != nil {
		panic(err)
	}
	for _, r := range rows {
		sec, _ := strconv.ParseInt(r.Start, 10, 64)
		ts := time.Unix(sec, 0).UTC().Format(time.RFC3339)
		rec := []string{ts, r.Open, r.High, r.Low, r.Close, r.Volume}
		if err := w.Write(rec); err != nil {
			panic(err)
		}
	}

	fmt.Printf("Wrote %s (%d rows)\n", *outPath, len(rows))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func normalizeList(raw any) []candleRow {
	// Accept either:
	//   [ {...}, {...} ]  or  {"candles": [ {...} ] }
	switch v := raw.(type) {
	case []any:
		return toRows(v)
	case map[string]any:
		if c, ok := v["candles"]; ok {
			if arr, ok := c.([]any); ok {
				return toRows(arr)
			}
		}
	}
	return nil
}

func toRows(arr []any) []candleRow {
	out := make([]candleRow, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, candleRow{
				Start:  asString(m["start"]),
				Open:   asString(m["open"]),
				High:   asString(m["high"]),
				Low:    asString(m["low"]),
				Close:  asString(m["close"]),
				Volume: asString(m["volume"]),
			})
		}
	}
	return out
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON numbers come as float64; format without scientific notation.
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}
