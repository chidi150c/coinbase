// FILE: observability.go

package main

import (
	"fmt"
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
