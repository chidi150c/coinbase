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
	EntryProduceErrNilTrader                  EntryProduceErrorCode = "nil_trader"
	EntryProduceErrNilPendingIntent           EntryProduceErrorCode = "nil_pending_intent"
	EntryProduceErrInvalidSide                EntryProduceErrorCode = "invalid_side"
	EntryProduceErrMissingProductID           EntryProduceErrorCode = "missing_product_id"
	EntryProduceErrInvalidPrice               EntryProduceErrorCode = "invalid_price"
	EntryProduceErrInvalidQuantity            EntryProduceErrorCode = "invalid_quantity"
	EntryProduceErrInvalidQuote               EntryProduceErrorCode = "invalid_quote"
	EntryProduceErrInvalidTake                EntryProduceErrorCode = "invalid_take"
	EntryProduceErrInvalidRefundPortion       EntryProduceErrorCode = "invalid_refund_portion"
	EntryProduceErrInvalidConfidenceMult      EntryProduceErrorCode = "invalid_confidence_mult"
	EntryProduceErrInvalidProfitGate          EntryProduceErrorCode = "invalid_profit_gate"
	EntryProduceErrMissingProducer            EntryProduceErrorCode = "missing_producer"
	EntryProduceErrMissingProducerReason      EntryProduceErrorCode = "missing_producer_reason"
	EntryProduceErrMissingPendingCancelPolicy EntryProduceErrorCode = "missing_pending_cancel_policy"

	EntryProduceErrSubmitNilTrader         EntryProduceErrorCode = "submit_nil_trader"
	EntryProduceErrSubmitNilContext        EntryProduceErrorCode = "submit_nil_context"
	EntryProduceErrSubmitNilPendingIntent  EntryProduceErrorCode = "submit_nil_pending_intent"
	EntryProduceErrSubmitNilBroker         EntryProduceErrorCode = "submit_nil_broker"
	EntryProduceErrPostOnlySubmit          EntryProduceErrorCode = "post_only_submit_failed"
	EntryProduceErrMissingSubmittedOrderID EntryProduceErrorCode = "submitted_order_id_missing"

	EntryProduceErrBuildNilTrader        EntryProduceErrorCode = "build_nil_trader"
	EntryProduceErrBuildNilPendingIntent EntryProduceErrorCode = "build_nil_pending_intent"
	EntryProduceErrBuildMissingOrderID   EntryProduceErrorCode = "build_missing_order_id"
	EntryProduceErrBuildUnsupportedSide  EntryProduceErrorCode = "build_unsupported_side"

	EntryProduceErrRegisterNilTrader        EntryProduceErrorCode = "register_nil_trader"
	EntryProduceErrRegisterNilPendingEntry  EntryProduceErrorCode = "register_nil_pending_entry"
	EntryProduceErrRegisterNilPendingIntent EntryProduceErrorCode = "register_nil_pending_intent"
	EntryProduceErrRegisterMissingOrderID   EntryProduceErrorCode = "register_missing_order_id"
	EntryProduceErrRegisterDuplicateOrderID EntryProduceErrorCode = "register_duplicate_order_id"

	EntryProduceErrPersistState  EntryProduceErrorCode = "persist_state_failed"
	EntryProduceErrCleanupCancel EntryProduceErrorCode = "cleanup_cancel_failed"
)

// EntryProduceError carries stable entry-production failure information
// from produceEntry() to the producer wrapper.
//
// CleanupRequired means an exchange order may exist and the caller should
// resolve that order before deciding whether to retry.
type EntryProduceError struct {
	Code            EntryProduceErrorCode
	Producer        EntryProducer
	Side            string
	OrderID         string
	CleanupRequired bool
	Err             error
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
		e.Err,
	)
}

func (e *EntryProduceError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}
