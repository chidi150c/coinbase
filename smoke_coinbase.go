//go:build smoke

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

func main() {
	product := flag.String("product", "BTC-USD", "product id, e.g. BTC-USD")
	gran := flag.String("gran", "ONE_MINUTE", "granularity")
	limit := flag.Int("limit", 3, "candle limit")
	place := flag.Float64("place", 0, "market order quote-size (USD); 0 = no order")
	sideStr := flag.String("side", "BUY", "BUY|SELL")
	flag.Parse()

	// hard fail early if creds missing (same envs you used in bridge)
	if os.Getenv("COINBASE_API_KEY_NAME") == "" || os.Getenv("COINBASE_API_PRIVATE_KEY") == "" {
		log.Fatal("COINBASE_API_KEY_NAME/COINBASE_API_PRIVATE_KEY must be set (load /opt/coinbase/env/bridge.env)")
	}

	b := NewCoinbaseBroker()

	if *place > 0 {
		var side OrderSide
		switch strings.ToUpper(strings.TrimSpace(*sideStr)) {
		case "SELL":
			side = SideSell
		default:
			side = SideBuy
		}
		fmt.Printf("Placing market %s by quote $%.2f on %s ...\n", side, *place, *product)
		po, err := b.PlaceMarketQuote(nil, *product, side, *place)
		if err != nil {
			log.Fatalf("order error: %v", err)
		}
		fmt.Printf("OK id=%s filled_base=%.8f avg_price=%.2f commission=%.4f\n",
			po.ID, po.BaseSize, po.Price, po.CommissionUSD)
		return
	}

	cs, err := b.GetRecentCandles(nil, *product, *gran, *limit)
	if err != nil {
		log.Fatalf("candles error: %v", err)
	}
	fmt.Printf("candles: %d\n", len(cs))
	for i, c := range cs {
		fmt.Printf("%d) %s O=%.2f H=%.2f L=%.2f C=%.2f V=%.6f\n",
			i, c.Time.UTC().Format("15:04:05"), c.Open, c.High, c.Low, c.Close, c.Volume)
	}
}
