package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	gateAnalysisSampleInterval = 10 * time.Second
	gateAnalysisRetention      = 48 * time.Hour
)

type GateAnalysisPoint struct {
	Time          int64   `json:"time"`
	Price         float64 `json:"price"`
	LogicEPS      float64 `json:"logic_eps"`
	LogicMACDTurn float64 `json:"logic_macd_turn"`
}

// Gate Analysis history is intentionally disk-backed. The trading process
// keeps no 48-hour slice/map of telemetry in memory.
var gateAnalysisWriteMu sync.Mutex

// recordGateAnalysisPointLocked records at most one point per 10-second bucket.
//
// The caller MUST already hold t.mu.
//
// Only the one last-sample timestamp is retained on Trader. Once a point is
// accepted for sampling, file I/O is handed to a short-lived goroutine so the
// trading mutex and decision/order path are not blocked on disk.
func (t *Trader) recordGateAnalysisPointLocked(
	now time.Time,
	price float64,
	logicEPS float64,
	logicMACDTurn float64,
) {
	if t == nil ||
		price <= 0 ||
		math.IsNaN(price) ||
		math.IsInf(price, 0) ||
		math.IsNaN(logicEPS) ||
		math.IsInf(logicEPS, 0) ||
		math.IsNaN(logicMACDTurn) ||
		math.IsInf(logicMACDTurn, 0) {
		return
	}

	now = now.UTC()
	unix := now.Unix()
	if unix <= 0 {
		return
	}

	previous := t.gateAnalysisLastSampleUnix

	bucketSeconds := int64(
		gateAnalysisSampleInterval / time.Second,
	)

	if previous > 0 &&
		previous/bucketSeconds == unix/bucketSeconds {
		return
	}

	t.gateAnalysisLastSampleUnix = unix

	point := GateAnalysisPoint{
		Time:          unix,
		Price:         price,
		LogicEPS:      logicEPS,
		LogicMACDTurn: logicMACDTurn,
	}

	go persistGateAnalysisPoint(
		point,
		previous,
	)
}

func gateAnalysisDir() string {
	dir := strings.TrimSpace(
		os.Getenv("GATE_ANALYSIS_DIR"),
	)
	if dir == "" {
		dir = "/opt/coinbase/state/gate_analysis"
	}
	return dir
}

func persistGateAnalysisPoint(
	point GateAnalysisPoint,
	previousSampleUnix int64,
) {
	gateAnalysisWriteMu.Lock()
	defer gateAnalysisWriteMu.Unlock()

	dir := gateAnalysisDir()

	if err := os.MkdirAll(
		dir,
		0755,
	); err != nil {
		return
	}

	pointTime := time.Unix(
		point.Time,
		0,
	).UTC()

	path := filepath.Join(
		dir,
		fmt.Sprintf(
			"gate_analysis_%s.jsonl",
			pointTime.Format("20060102"),
		),
	)

	f, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		return
	}

	encodeErr := json.NewEncoder(f).Encode(point)
	closeErr := f.Close()

	if encodeErr != nil || closeErr != nil {
		return
	}

	/*
		Cleanup is needed only on first successful sample after startup or
		when UTC day changes. Daily segmentation avoids rewriting a growing
		48-hour JSON document every 10 seconds.

		The oldest overlapping UTC-day segment may contain records slightly
		older than 48 hours; BOT OPS filters individual records against the
		exact 48-hour cutoff while plotting.
	*/
	if previousSampleUnix == 0 ||
		time.Unix(previousSampleUnix, 0).UTC().Format("20060102") !=
			pointTime.Format("20060102") {
		pruneGateAnalysisSegments(
			dir,
			pointTime.Add(-gateAnalysisRetention),
		)
	}
}

func pruneGateAnalysisSegments(
	dir string,
	cutoff time.Time,
) {
	paths, err := filepath.Glob(
		filepath.Join(
			dir,
			"gate_analysis_*.jsonl",
		),
	)
	if err != nil {
		return
	}

	for _, path := range paths {
		base := filepath.Base(path)

		dateText := strings.TrimSuffix(
			strings.TrimPrefix(
				base,
				"gate_analysis_",
			),
			".jsonl",
		)

		dayStart, err := time.Parse(
			"20060102",
			dateText,
		)
		if err != nil {
			continue
		}

		// Delete only segments that cannot contain any point in the
		// rolling 48-hour plotting window.
		if !dayStart.Add(24 * time.Hour).After(
			cutoff.UTC(),
		) {
			_ = os.Remove(path)
		}
	}
}
