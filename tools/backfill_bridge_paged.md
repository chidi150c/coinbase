// Build a larger CSV by paging the FastAPI bridge /candles endpoint backward in time.
//
// Usage (from ~/coinbase/monitoring):
//   docker compose run --rm bot go run /app/tools/backfill_bridge_paged.go \
//     -product BTC-USD -granularity ONE_MINUTE -limit 300 -pages 20 -out /app/data/BTC-USD.csv
//
// Notes:
// - Assumes bridge URL is http://bridge:8787 inside the compose network.
// - /candles returns [{"start","open","high","low","close","volume"}] with all fields as strings.
// - We page backward using start/end UNIX seconds. Dedupe & sort ascending, write RFC3339 timestamps.

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
		limit       = flag.Int("limit", 300, "Candles per page (API max typically 350)")
		pages       = flag.Int("pages", 20, "How many pages to fetch (backwards)")
		outPath     = flag.String("out", "data/BTC-USD.csv", "Output CSV path (inside repo)")
	)
	flag.Parse()

	bridge := getenv("BRIDGE_URL", "http://bridge:8787")
	secPer := granularitySeconds(*granularity)
	if secPer <= 0 {
		panic("unsupported granularity: " + *granularity)
	}

	end := time.Now().UTC()
	all := make([]candleRow, 0, (*limit)*(*pages))

	for p := 0; p < *pages; p++ {
		// Window for this page (pad a bit to avoid edge filtering on server)
		start := end.Add(-time.Duration((*limit+5)*int(secPer)) * time.Second)

		url := fmt.Sprintf("%s/candles?product_id=%s&granularity=%s&limit=%d&start=%d&end=%d",
			trimRightSlash(bridge), *product, *granularity, *limit, start.Unix(), end.Unix())

		resp, err := http.Get(url)
		if err != nil {
			panic(fmt.Errorf("GET %s: %w", url, err))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			panic(fmt.Errorf("bridge status %d for %s", resp.StatusCode, url))
		}

		var raw any
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			resp.Body.Close()
			panic(fmt.Errorf("decode JSON: %w", err))
		}
		resp.Body.Close()

		batch := normalizeList(raw)
		if len(batch) == 0 {
			// No more data in this window — stop early.
			break
		}

		all = append(all, batch...)
		// Move window back
		end = start
	}

	// Dedupe by Start and sort ascending
	dedup := make(map[string]candleRow, len(all))
	for _, r := range all {
		if r.Start != "" {
			dedup[r.Start] = r
		}
	}
	all = all[:0]
	for _, r := range dedup {
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		ti, _ := strconv.ParseInt(all[i].Start, 10, 64)
		tj, _ := strconv.ParseInt(all[j].Start, 10, 64)
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
	for _, r := range all {
		sec, _ := strconv.ParseInt(r.Start, 10, 64)
		ts := time.Unix(sec, 0).UTC().Format(time.RFC3339)
		if err := w.Write([]string{ts, r.Open, r.High, r.Low, r.Close, r.Volume}); err != nil {
			panic(err)
		}
	}
	fmt.Printf("Wrote %s (%d rows)\n", *outPath, len(all))
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

func granularitySeconds(g string) int64 {
	switch g {
	case "ONE_MINUTE":
		return 60
	case "FIVE_MINUTE":
		return 5 * 60
	case "FIFTEEN_MINUTE":
		return 15 * 60
	case "THIRTY_MINUTE":
		return 30 * 60
	case "ONE_HOUR":
		return 60 * 60
	case "TWO_HOUR":
		return 2 * 60 * 60
	case "FOUR_HOUR":
		return 4 * 60 * 60
	case "SIX_HOUR":
		return 6 * 60 * 60
	case "ONE_DAY":
		return 24 * 60 * 60
	default:
		return 0
	}
}

func normalizeList(raw any) []candleRow {
	// Accept either a bare array or {"candles":[...]}
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
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}
