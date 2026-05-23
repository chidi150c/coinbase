// FILE: mined_labels.go
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
)

type MinedLabelRow struct {
	TS         string    `json:"ts"`
	Symbol     string    `json:"symbol"`
	Y          float64   `json:"y"`
	X          []float64 `json:"x"`
	LabelType  string    `json:"label_type"`
	ProfitUSD  float64   `json:"profit_usd"`
	BaseUSD    float64   `json:"base_usd"`
	Horizon    int       `json:"horizon"`
	FeatureDim int       `json:"feature_dim"`
}

func appendMinedLabel(path string, row MinedLabelRow, maxRows int) {
	if path == "" {
		return
	}

	rows := loadMinedLabels(path)
	key := row.TS + "|" + row.Symbol + "|" + stringLabel(row.Y)

	seen := make(map[string]bool, len(rows)+1)
	for _, r := range rows {
		seen[r.TS+"|"+r.Symbol+"|"+stringLabel(r.Y)] = true
	}

	if seen[key] {
		return
	}

	rows = append(rows, row)

	if maxRows <= 0 {
		maxRows = 10000
	}
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}

	if err := writeMinedLabels(path, rows); err != nil {
		log.Printf("[WARN] mined_labels write failed path=%s err=%v", path, err)
		return
	}

	log.Printf("[MINED_LABEL] appended path=%s total=%d y=%.0f ts=%s", path, len(rows), row.Y, row.TS)
}

func loadMinedLabels(path string) []MinedLabelRow {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var rows []MinedLabelRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r MinedLabelRow
		if err := json.Unmarshal(sc.Bytes(), &r); err == nil && len(r.X) > 0 {
			rows = append(rows, r)
		}
	}
	return rows
}

func writeMinedLabels(path string, rows []MinedLabelRow) error {
	tmp := path + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(f)
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}

	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

func stringLabel(y float64) string {
	if y >= 0.5 {
		return "1"
	}
	return "0"
}
