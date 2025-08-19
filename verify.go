package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

type anymap map[string]any

func getJSON(url string, out any) (int, []byte, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "coinbot-verify-bridge/1.0")
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(b, out) // best-effort
	}
	return resp.StatusCode, b, nil
}

func main() {
	bridge := getenv("BRIDGE_URL", "http://127.0.0.1:8787")

	// 1) /health (your app returns {"ok": true} on success)
	var health anymap
	s, body, err := getJSON(bridge+"/health", &health)
	if err != nil {
		fmt.Printf("❌ GET /health error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("GET %s/health -> %d\n%s\n\n", bridge, s, truncate(body, 500))
	if s != 200 || (health["ok"] == false) {
		fmt.Println("⚠️  /health did not confirm ok==true; check sidecar logs.")
	}

	// 2) /accounts?limit=1 (auth sanity)
	var accts anymap
	s, body, err = getJSON(bridge+"/accounts?limit=1", &accts)
	if err != nil {
		fmt.Printf("❌ GET /accounts error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("GET %s/accounts?limit=1 -> %d\n%s\n\n", bridge, s, truncate(body, 900))
	if s != 200 {
		fmt.Println("❌ accounts call not 200 — credentials or IP allowlist may be wrong.")
		os.Exit(2)
	}

	// 3) /product/BTC-USD (product/ticker shape depends on the Python client)
	var prod anymap
	s, body, err = getJSON(bridge+"/product/BTC-USD", &prod)
	if err != nil {
		fmt.Printf("❌ GET /product/BTC-USD error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("GET %s/product/BTC-USD -> %d\n%s\n", bridge, s, truncate(body, 900))
	if s != 200 {
		fmt.Println("⚠️  product endpoint not 200; check bridge logs, but auth still looks OK.")
	}

	fmt.Println("\n✅ Bridge reachable; credentials verified via /accounts.")
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
