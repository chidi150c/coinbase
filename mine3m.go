// FILE: mine3m.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

func runMine3m(ctx context.Context, cfg Config, broker Broker, limit int) error {
	if limit <= 0 {
		limit = 50000
	}

	if cfg.BridgeURL == "" {
		return fmt.Errorf("mine3m requires BRIDGE_URL for paged candle backfill")
	}

	log.Printf("[MINE3M] start product=%s limit_1m=%d bridge=%s", cfg.ProductID, limit, cfg.BridgeURL)

	// Pull large 1m history through existing bridge pager.
	history1m, err := fetchHistoryPaged(
		cfg.BridgeURL,
		cfg.ProductID,
		"ONE_MINUTE",
		300,
		limit,
	)
	if err != nil {
		return err
	}
	if len(history1m) == 0 {
		return fmt.Errorf("no 1m candles returned")
	}

	history3m := AggregateCandles(history1m, 3*time.Minute)
	if len(history3m) == 0 {
		return fmt.Errorf("no 3m candles produced from %d 1m candles", len(history1m))
	}

	labelCfg := cfg.FeatureLabelConfig()
	labelCfg.Horizon = getEnvInt("AI_LABEL_HORIZON_3M", 20)
	labelCfg.MinedLabelsFile = getEnv("AI_MINED_LABELS_FILE_3M", "/opt/coinbase/state/mined_labels_binance_3m.jsonl")
	labelCfg.Symbol = cfg.ProductID

	beforeRows := len(loadMinedLabels(labelCfg.MinedLabelsFile))

	feats, labels := BuildFeaturesAndLabels(history3m, labelCfg)

	afterRows := len(loadMinedLabels(labelCfg.MinedLabelsFile))

	up := 0
	down := 0
	for _, y := range labels {
		if y >= 0.5 {
			up++
		} else {
			down++
		}
	}

	log.Printf(
		"[MINE3M] done candles_1m=%d candles_3m=%d mined_before=%d mined_after=%d added=%d dataset_rows=%d up=%d down=%d file=%s",
		len(history1m),
		len(history3m),
		beforeRows,
		afterRows,
		afterRows-beforeRows,
		len(feats),
		up,
		down,
		labelCfg.MinedLabelsFile,
	)

	return nil
}
