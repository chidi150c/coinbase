// FILE: observability.go

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type ProducerStage string

const (
	ProducerStageProduced  ProducerStage = "produced"
	ProducerStagePending   ProducerStage = "pending"
	ProducerStageFilled    ProducerStage = "filled"
	ProducerStageCommitted ProducerStage = "committed"

	ProducerStageCancelRequested ProducerStage = "cancel_requested"

	ProducerStageEntryFailed ProducerStage = "entry_failed"

	ProducerStageCleanupCancelled    ProducerStage = "cleanup_cancelled"
	ProducerStageCleanupCancelFailed ProducerStage = "cleanup_cancel_failed"
)

type ProducerHistory struct {
	Attempts map[string]*ProducerAttempt
}
type ProducerEvent struct {
	Time      time.Time
	CreatedAt time.Time

	Producer EntryProducer
	Side     string
	Stage    ProducerStage

	DecisionID string
	OrderID    string

	Reason string

	ErrorCode       EntryProduceErrorCode
	Error           string
	CleanupRequired bool

	PnL float64
}
type ProducerAttempt struct {
	DecisionID string
	CreatedAt  time.Time

	Producer EntryProducer
	Side     string

	Events map[ProducerStage]ProducerEvent
}

func FormatDecisionID(
	producer EntryProducer,
	createdAt time.Time,
) string {
	return fmt.Sprintf(
		"%s_%sM%03d",
		producer,
		createdAt.Format("20060102T150405"),
		createdAt.Nanosecond()/1_000_000,
	)
}

// recordProducerAttemptLocked records one producer attempt in the in-memory
// producer history. The caller MUST already hold t.mu.
//
// This helper owns only the mechanical producer -> DecisionID map update.
// It does not apply retry policy, perform cleanup, persist state, or mutate
// trading behavior. A nil attempt is intentionally ignored.
func (t *Trader) recordProducerAttemptLocked(attempt *ProducerAttempt) {
	if t == nil || attempt == nil {
		return
	}

	if t.producerHistory == nil {
		t.producerHistory = make(map[EntryProducer]*ProducerHistory)
	}

	history := t.producerHistory[attempt.Producer]
	if history == nil {
		history = &ProducerHistory{
			Attempts: make(map[string]*ProducerAttempt),
		}
		t.producerHistory[attempt.Producer] = history
	}

	if history.Attempts == nil {
		history.Attempts = make(map[string]*ProducerAttempt)
	}

	history.Attempts[attempt.DecisionID] = attempt
}

const ProducerHistoryRetention = 24 * time.Hour

func (t *Trader) pruneProducerHistoryLocked(
	now time.Time,
) bool {
	if t == nil || len(t.producerHistory) == 0 {
		return false
	}

	cutoff := now.UTC().Add(-ProducerHistoryRetention)
	changed := false

	for producer, history := range t.producerHistory {
		if history == nil {
			delete(t.producerHistory, producer)
			changed = true
			continue
		}

		if history.Attempts == nil {
			delete(t.producerHistory, producer)
			changed = true
			continue
		}

		for decisionID, attempt := range history.Attempts {
			if attempt == nil {
				delete(
					history.Attempts,
					decisionID,
				)
				changed = true
				continue
			}

			/*
				Retention is based only on the original producer-decision
				creation time.

				Do not:
				  - use ProducerEvent.Time;
				  - parse time from DecisionID;
				  - retain an old attempt merely because it received a later event.
			*/
			if attempt.CreatedAt.Before(cutoff) {
				delete(
					history.Attempts,
					decisionID,
				)
				changed = true
			}
		}

		// Do not retain an empty producer container.
		if len(history.Attempts) == 0 {
			delete(
				t.producerHistory,
				producer,
			)
			changed = true
		}
	}

	return changed
}

// saveProducerHistoryNoLock persists the current producer history.
//
// The caller MUST already hold t.mu or otherwise guarantee that
// t.producerHistory is stable for the duration of pruning and serialization.
//
// This function:
//   - prunes attempts older than ProducerHistoryRetention;
//   - serializes only producer observability state;
//   - writes through a temporary file and atomic rename;
//   - does not mutate trading behavior or critical trader state.
func (t *Trader) saveProducerHistoryNoLock() error {
	if t == nil {
		return errors.New(
			"save producer history: nil Trader",
		)
	}

	if strings.TrimSpace(t.producerHistoryFile) == "" {
		return nil
	}

	// Keep the persisted file itself inside the 24-hour window.
	t.pruneProducerHistoryLocked(
		time.Now().UTC(),
	)

	bs, err := json.MarshalIndent(
		t.producerHistory,
		"",
		" ",
	)
	if err != nil {
		return fmt.Errorf(
			"save producer history: marshal: %w",
			err,
		)
	}

	tmp := t.producerHistoryFile + ".tmp"

	if err := os.WriteFile(
		tmp,
		bs,
		0644,
	); err != nil {
		return fmt.Errorf(
			"save producer history: write temp file: %w",
			err,
		)
	}

	if err := os.Rename(
		tmp,
		t.producerHistoryFile,
	); err != nil {
		return fmt.Errorf(
			"save producer history: rename temp file: %w",
			err,
		)
	}

	return nil
}

func (t *Trader) loadProducerHistory() error {
	if t == nil {
		return errors.New(
			"load producer history: nil Trader",
		)
	}

	if strings.TrimSpace(t.producerHistoryFile) == "" {
		return nil
	}

	bs, err := os.ReadFile(
		t.producerHistoryFile,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.producerHistory =
				make(map[EntryProducer]*ProducerHistory)

			return nil
		}

		return fmt.Errorf(
			"load producer history: read: %w",
			err,
		)
	}

	history := make(
		map[EntryProducer]*ProducerHistory,
	)

	if len(bs) > 0 {
		if err := json.Unmarshal(
			bs,
			&history,
		); err != nil {
			return fmt.Errorf(
				"load producer history: unmarshal: %w",
				err,
			)
		}
	}

	// Repair nil containers from old/partial JSON.
	for producer, producerHistory := range history {
		if producerHistory == nil {
			delete(history, producer)
			continue
		}

		if producerHistory.Attempts == nil {
			producerHistory.Attempts =
				make(map[string]*ProducerAttempt)
		}

		for decisionID, attempt := range producerHistory.Attempts {

			if attempt == nil {
				delete(
					producerHistory.Attempts,
					decisionID,
				)
				continue
			}

			if attempt.Events == nil {
				attempt.Events =
					make(map[ProducerStage]ProducerEvent)
			}
		}
	}

	t.producerHistory = history

	// Startup retention cleanup.
	t.pruneProducerHistoryLocked(
		time.Now().UTC(),
	)

	return nil
}
