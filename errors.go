// FILE: errors.go

package main

import (
	"fmt"
)

// EntryProduceErrorCode identifies the stage at which entry production
// failed. The code describes what happened; producer-specific callers
// decide what action to take.
type EntryProduceErrorCode string

const (
	EntryProduceErrInvalidIntent EntryProduceErrorCode = "invalid_intent"
	EntryProduceErrSubmit        EntryProduceErrorCode = "submit_failed"
	EntryProduceErrBuild         EntryProduceErrorCode = "build_failed"
	EntryProduceErrRegister      EntryProduceErrorCode = "register_failed"
	EntryProduceErrPersist       EntryProduceErrorCode = "persist_failed"
	EntryProduceErrCleanupCancel EntryProduceErrorCode = "cleanup_cancel_failed"
)

// EntryProduceError carries stable entry-production failure information
// from produceEntry() to the producer wrapper.
//
// CleanupRequired means an exchange order may exist and the caller should
// resolve that order before deciding whether to retry.
type EntryProduceError struct {
	Code EntryProduceErrorCode

	Producer EntryProducer
	Side     string
	OrderID  string

	CleanupRequired bool

	Cause error
}

func (e *EntryProduceError) Error() string {
	if e == nil {
		return ""
	}

	return fmt.Sprintf(
		"entry production failed: code=%s producer=%s side=%s "+
			"order_id=%s cleanup_required=%t cause=%v",
		e.Code,
		e.Producer,
		e.Side,
		e.OrderID,
		e.CleanupRequired,
		e.Cause,
	)
}

func (e *EntryProduceError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}
