// FILE: mine_tf.go
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
)

func runMineTF(ctx context.Context, cfg Config, broker Broker, tf string, limit int) error {
	tf = strings.ToLower(strings.TrimSpace(tf))
	if tf == "" {
		tf = "3m"
	}

	if limit <= 0 {
		limit = 50000
	}

	if cfg.BridgeURL == "" {
		return fmt.Errorf("mine-tf requires BRIDGE_URL for paged candle backfill")
	}

	granularity := timeframeToGranularity(tf)
	if granularity == "" {
		return fmt.Errorf("unsupported mine timeframe: %s", tf)
	}

	log.Printf(
		"[MINE_TF] start product=%s tf=%s granularity=%s limit=%d bridge=%s",
		cfg.ProductID,
		tf,
		granularity,
		limit,
		cfg.BridgeURL,
	)

	candles, err := fetchHistoryPaged(
		cfg.BridgeURL,
		cfg.ProductID,
		granularity,
		300,
		limit,
	)
	if err != nil {
		return err
	}
	if len(candles) == 0 {
		return fmt.Errorf("no %s candles returned", tf)
	}

	envSuffix := timeframeEnvSuffix(tf)

	labelCfg := cfg.FeatureLabelConfig()
	labelCfg.Horizon = getEnvInt("AI_LABEL_HORIZON_"+envSuffix, defaultMineHorizon(tf))
	labelCfg.MinedLabelsFile = getEnv(
		"AI_MINED_LABELS_FILE_"+envSuffix,
		fmt.Sprintf("/opt/coinbase/state/mined_labels_binance_%s.jsonl", tf),
	)
	labelCfg.Symbol = cfg.ProductID

	beforeRows := len(loadMinedLabels(labelCfg.MinedLabelsFile))

	feats, labels := BuildFeaturesAndLabels(candles, labelCfg)

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
		"[MINE_TF] done tf=%s candles=%d mined_before=%d mined_after=%d added=%d dataset_rows=%d up=%d down=%d horizon=%d file=%s",
		tf,
		len(candles),
		beforeRows,
		afterRows,
		afterRows-beforeRows,
		len(feats),
		up,
		down,
		labelCfg.Horizon,
		labelCfg.MinedLabelsFile,
	)

	return nil
}

func timeframeToGranularity(tf string) string {
	switch strings.ToLower(strings.TrimSpace(tf)) {
	case "1m":
		return "ONE_MINUTE"
	case "3m":
		return "THREE_MINUTE"
	case "5m":
		return "FIVE_MINUTE"
	case "15m":
		return "FIFTEEN_MINUTE"
	case "30m":
		return "THIRTY_MINUTE"
	case "1h":
		return "ONE_HOUR"
	default:
		return ""
	}
}

func timeframeEnvSuffix(tf string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(tf), "-", "_"))
}

func defaultMineHorizon(tf string) int {
	switch strings.ToLower(strings.TrimSpace(tf)) {
	case "1m":
		return 30
	case "3m":
		return 20
	case "5m":
		return 12
	case "15m":
		return 8
	case "30m":
		return 6
	case "1h":
		return 4
	default:
		return 20
	}
}
