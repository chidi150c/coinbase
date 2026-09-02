// FILE: observability.go
// ProducerAttempt is the single lifecycle object keyed by DecisionID.
// Events map[ProducerStage]ProducerEvent lets one attempt accumulate produced → pending → filled → committed, or failure/cleanup stages.
// recordProducerAttemptLocked() is deliberately mechanical and does not classify failures or alter trading behavior.
// Retention is exposure-aware: non-exposure attempts use CreatedAt; exited exposure uses the terminal exited event time.
// Persistence is isolated in producerHistoryFile and uses temp-file + rename.
// Loading repairs nil maps, restores durable economics, and applies exposure-aware pruning.
// The additional drain/poller/commit stages are compatible with the lifecycle work already in trader.go.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ProducerStage string

const (
	ProducerStageDecision  ProducerStage = "decision"
	ProducerStageProduced  ProducerStage = "produced"
	ProducerStagePending   ProducerStage = "pending"
	ProducerStageFilled    ProducerStage = "filled"
	ProducerStageCommitted ProducerStage = "committed"
	ProducerStageExited    ProducerStage = "exited"

	ProducerStageCancelRequested ProducerStage = "cancel_requested"

	ProducerStageEntryFailed ProducerStage = "entry_failed"

	ProducerStageDecisionFailed   ProducerStage = "decision_failed"
	ProducerStageDecisionBlocked  ProducerStage = "decision_blocked"
	ProducerStageDecisionDeferred ProducerStage = "decision_deferred"
	ProducerStageSizingReduced    ProducerStage = "sizing_reduced"

	// Resource-allocation lifecycle. These stages are authoritative backend
	// evidence for BOT OPS and exist before any exchange OrderID is created.
	ProducerStageAllocationRequested ProducerStage = "allocation_requested"
	ProducerStageAllocationApproved  ProducerStage = "allocation_approved"
	ProducerStageAllocationPartial   ProducerStage = "allocation_partial"
	ProducerStageAllocationRejected  ProducerStage = "allocation_rejected"

	// Case3A recovery-mode evaluation stages preserve the sequential
	// decision path under one Case3AReplacement DecisionID.
	ProducerStageCase3AModeABlocked ProducerStage = "case3a_mode_a_blocked"
	ProducerStageCase3AModeBBlocked ProducerStage = "case3a_mode_b_blocked"

	// Case3A realized-recovery lifecycle stages.
	//
	// Partial recovery is a first-class NON-TERMINAL exit event:
	// an exchange exit filled, realized PnL was booked, recovery debt was
	// reduced, and the original producer entry remains live in SideBook.Lots.
	//
	// Final recovery is the Case3A recovery-completion event. Canonical
	// ProducerStageExited remains the terminal producer-exposure stage.
	ProducerStageCase3APartialRecovery ProducerStage = "case3a_partial_recovery"
	ProducerStageCase3AFinalRecovery   ProducerStage = "case3a_final_recovery"

	ProducerStageCleanupCancelled    ProducerStage = "cleanup_cancelled"
	ProducerStageCleanupCancelFailed ProducerStage = "cleanup_cancel_failed"

	ProducerStagePollerGetOrderFailed ProducerStage = "poller_get_order_failed"
	ProducerStageRejected             ProducerStage = "rejected"
	ProducerStageExpired              ProducerStage = "expired"

	ProducerStageDrainMissingProducer    ProducerStage = "drain_missing_producer"
	ProducerStageDrainProducerMismatch   ProducerStage = "drain_producer_mismatch"
	ProducerStageDrainChannelClosed      ProducerStage = "drain_channel_closed"
	ProducerStageFillOrderMismatch       ProducerStage = "fill_order_mismatch"
	ProducerStageCommitFailed            ProducerStage = "commit_failed"
	ProducerStageDrainPersistStateFailed ProducerStage = "drain_persist_state_failed"
	ProducerStageCommitSparePointerNil   ProducerStage = "commit_spare_pointer_nil"
	ProducerStageRefundConsumed          ProducerStage = "refund_consumed"
)

type ProducerHistory struct {
	Attempts map[string]*ProducerAttempt
}

type ProducerEconomics struct {
	Producer EntryProducer

	Attempts          uint64
	Fills             uint64
	CancelledAttempts uint64
	FailedAttempts    uint64

	ClosedPositions uint64
	Wins            uint64
	Losses          uint64

	NetPnLUSD float64

	LastAttemptAt  time.Time
	LastProducedAt time.Time
	LastFillAt     time.Time
	LastExitAt     time.Time

	// LastActivity* is bounded durable metadata for BOT OPS dormancy/
	// "why has this producer not fired" checks after detailed attempts expire.
	LastActivityAt        time.Time
	LastActivityStage     ProducerStage
	LastActivityReason    string
	LastActivityErrorCode EntryProduceErrorCode
	LastActivityError     string

	// PrunedErrorCodeCounts is the durable histogram of ErrorCode values from
	// every detailed attempt removed by either retention policy. Its key-space
	// is bounded by EntryProduceErrorCode, so it does not grow with DecisionIDs.
	PrunedErrorCodeCounts map[EntryProduceErrorCode]uint64
}

// ProducerObservabilityState is the durable producer-observability snapshot.
//
// History contains bounded detailed ProducerAttempt lifecycle evidence.
//
// Economics contains one long-lived aggregate record per producer and
// therefore grows only with the number of producers, not the number of
// producer decisions or completed trades.
//
// Both belong to the same producer observability persistence domain and are
// written atomically to producerHistoryFile.
type ProducerObservabilityState struct {
	History   map[EntryProducer]*ProducerHistory
	Economics map[EntryProducer]*ProducerEconomics
}

// ProducerOpsSummary is a read-only dashboard projection. It combines durable
// folded economics with the currently retained detailed attempts without
// mutating either store. Derived rates are calculated at read time.
type ProducerOpsSummary struct {
	Producer EntryProducer

	Attempts          uint64
	Fills             uint64
	CancelledAttempts uint64
	FailedAttempts    uint64

	OpenPositions   uint64
	ClosedPositions uint64
	Wins            uint64
	Losses          uint64

	NetPnLUSD      float64
	RealizedPnLUSD float64

	WinRatePct    float64
	AveragePnLUSD float64

	LastAttemptAt  time.Time
	LastProducedAt time.Time
	LastFillAt     time.Time
	LastExitAt     time.Time

	LastActivityAt        time.Time
	LastActivityStage     ProducerStage
	LastActivityDecision  string
	LastActivityOrderID   string
	LastActivityReason    string
	LastActivityErrorCode EntryProduceErrorCode
	LastActivityError     string
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

	PnL   float64
	Price float64

	BaseSize   float64
	QuoteValue float64

	// Resource-allocation evidence. Zero values are unused for non-allocation
	// lifecycle stages.
	ProducerPriority int
	AllocationStatus string
	AllocationReason string
	AllocationMethod string

	RequestedQuote float64
	RequestedBase  float64
	AllocatedQuote float64
	AllocatedBase  float64

	AvailableQuote float64
	AvailableBase  float64

	AllocationFraction       float64
	PriorityGroupRequested   float64
	PriorityGroupAvailable   float64
	PriorityGroupMemberCount int
}

type ProducerAttempt struct {
	DecisionID string
	CreatedAt  time.Time

	Producer EntryProducer
	Side     string

	Events map[ProducerStage]ProducerEvent

	// Accumulated realized NET PnL attributable to this producer attempt.
	//
	// Every authoritative ExitRecord.PNLUSD for this entry contributes
	// exactly once here, including Case3A partial/final recovery exits.
	// Those exits are also retained as first-class ProducerEvent stages:
	// ProducerStageCase3APartialRecovery / ProducerStageCase3AFinalRecovery.
	//
	// This is not itself a terminal lifecycle state. The producer exposure
	// may remain live after RealizedPnLUSD has become non-zero.
	RealizedPnLUSD float64

	// Time of the most recent realized exit contribution.
	//
	// This may represent a partial realization. It must not be used as the
	// final retention anchor unless the producer exposure has actually
	// ceased to exist in the authoritative SideBook.Lots.
	LastRealizedAt time.Time
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

func newProducerIntentLifecycle(
	intent *PendingIntent,
) *ProducerAttempt {
	if intent == nil ||
		intent.Producer == EntryProducerNone {

		return nil
	}

	createdAt := time.Now().UTC()

	intent.CreatedAt = createdAt
	intent.DecisionID = FormatDecisionID(
		intent.Producer,
		createdAt,
	)

	return &ProducerAttempt{
		DecisionID: intent.DecisionID,
		CreatedAt:  intent.CreatedAt,
		Producer:   intent.Producer,
		Side:       fmt.Sprint(intent.Side),

		Events: make(
			map[ProducerStage]ProducerEvent,
		),
	}
}

func (t *Trader) addDecisionProducerEvent(
	intent *PendingIntent,
	attempt *ProducerAttempt,
	stage ProducerStage,
	code EntryProduceErrorCode,
	err error,
	cleanupRequired bool,
	persist bool,
) {
	if t == nil ||
		intent == nil ||
		attempt == nil {
		return
	}

	if attempt.Events == nil {
		attempt.Events =
			make(map[ProducerStage]ProducerEvent)
	}

	errorText := ""
	if err != nil {
		errorText = err.Error()
	}

	attempt.Events[stage] = ProducerEvent{
		Time:      time.Now().UTC(),
		CreatedAt: intent.CreatedAt,

		Producer: intent.Producer,
		Side:     fmt.Sprint(intent.Side),
		Stage:    stage,

		DecisionID: intent.DecisionID,

		Reason: intent.ProducerReason,

		ErrorCode:       code,
		Error:           errorText,
		CleanupRequired: cleanupRequired,
	}

	if !persist {
		return
	}

	t.recordProducerAttemptLocked(
		attempt,
	)

	if err := t.saveProducerHistoryNoLock(); err != nil {
		log.Printf(
			"[ERROR] producer.history.save_failed "+
				"stage=%s producer=%s decision_id=%s err=%v",
			stage,
			attempt.Producer,
			attempt.DecisionID,
			err,
		)
	}
}

// producerAttemptEntryOrderID returns the best-known entry order ID for one
// producer attempt. Committed correlation is preferred; filled is fallback.
func producerAttemptEntryOrderID(
	attempt *ProducerAttempt,
) string {
	if attempt == nil || attempt.Events == nil {
		return ""
	}

	if event, ok := attempt.Events[ProducerStageCommitted]; ok {
		if orderID := strings.TrimSpace(event.OrderID); orderID != "" {
			return orderID
		}
	}

	if event, ok := attempt.Events[ProducerStageFilled]; ok {
		return strings.TrimSpace(event.OrderID)
	}

	return ""
}

// latestProducerEvent returns the newest canonical lifecycle event stored on
// an attempt. Event.Time is authoritative; CreatedAt is fallback.
func latestProducerEvent(
	attempt *ProducerAttempt,
) (ProducerEvent, bool) {
	if attempt == nil || len(attempt.Events) == 0 {
		return ProducerEvent{}, false
	}

	var latest ProducerEvent
	found := false

	for _, event := range attempt.Events {
		eventTime := event.Time
		if eventTime.IsZero() {
			eventTime = event.CreatedAt
		}

		latestTime := latest.Time
		if latestTime.IsZero() {
			latestTime = latest.CreatedAt
		}

		if !found || eventTime.After(latestTime) {
			latest = event
			found = true
		}
	}

	return latest, found
}

// producerAttemptFailed reports whether an attempt contains an authoritative
// failure stage. Blocked/deferred decisions are not counted as failures.
func producerAttemptFailed(
	attempt *ProducerAttempt,
) bool {
	if attempt == nil || attempt.Events == nil {
		return false
	}

	failureStages := [...]ProducerStage{
		ProducerStageDecisionFailed,
		ProducerStageEntryFailed,
		ProducerStageRejected,
		ProducerStageCleanupCancelFailed,
		ProducerStageCommitFailed,
	}

	for _, stage := range failureStages {
		if _, ok := attempt.Events[stage]; ok {
			return true
		}
	}

	return false
}

// findProducerAttemptByEntryOrderIDLocked resolves an existing producer
// attempt from producer ownership plus entry order correlation.
//
// The caller MUST already hold t.mu (read or write).
func (t *Trader) findProducerAttemptByEntryOrderIDLocked(
	producer EntryProducer,
	entryOrderID string,
) *ProducerAttempt {
	if t == nil || producer == EntryProducerNone {
		return nil
	}

	entryOrderID = strings.TrimSpace(entryOrderID)
	if entryOrderID == "" {
		return nil
	}

	history := t.producerHistory[producer]
	if history == nil || history.Attempts == nil {
		return nil
	}

	for _, attempt := range history.Attempts {
		if producerAttemptEntryOrderID(attempt) == entryOrderID {
			return attempt
		}
	}

	return nil
}

// producerEntryOrderLiveLocked reports whether the original producer
// EntryOrderID still exists in either authoritative live SideBook.
//
// The caller MUST already hold t.mu.
//
// Only SideBook.Lots defines live producer exposure for this purpose.
// Dust bookkeeping outside the authoritative books is intentionally
// not considered here.
func (t *Trader) producerEntryOrderLiveLocked(
	entryOrderID string,
) bool {
	if t == nil {
		return false
	}

	entryOrderID = strings.TrimSpace(
		entryOrderID,
	)
	if entryOrderID == "" {
		return false
	}

	for _, side := range []OrderSide{
		SideBuy,
		SideSell,
	} {
		book := t.book(side)
		if book == nil {
			continue
		}

		for _, lot := range book.Lots {
			if lot == nil {
				continue
			}

			if strings.TrimSpace(
				lot.EntryOrderID,
			) == entryOrderID {

				return true
			}
		}
	}

	return false
}

// markProducerExitedIfNotLiveLocked marks the existing producer attempt
// economically exited only when its original EntryOrderID is no longer
// present in either authoritative SideBook.Lots.
//
// The caller MUST already hold t.mu.
//
// Realized PnL must already have been recorded through
// recordProducerRealizedPnLLocked() before this helper is called.
//
// This helper must never:
//   - create another ProducerAttempt;
//   - regenerate DecisionID or CreatedAt;
//   - add ExitRecord.PNLUSD again;
//   - treat LastRealizedAt alone as proof of terminal exit.
//
// ProducerStageExited.PnL is a terminal snapshot of the attempt's already
// accumulated RealizedPnLUSD.
func (t *Trader) markProducerExitedIfNotLiveLocked(
	lot *Position,
	rec ExitRecord,
) bool {
	if t == nil ||
		lot == nil ||
		lot.Producer == EntryProducerNone {

		return false
	}

	entryOrderID := strings.TrimSpace(
		rec.EntryOrderID,
	)

	if entryOrderID == "" {
		entryOrderID =
			strings.TrimSpace(
				lot.EntryOrderID,
			)
	}

	if entryOrderID == "" {
		return false
	}

	/*
		The producer exposure is still live according to the agreed
		authoritative definition.

		Do not create a terminal exited event.
	*/
	if t.producerEntryOrderLiveLocked(
		entryOrderID,
	) {
		return false
	}

	matchedAttempt :=
		t.findProducerAttemptByEntryOrderIDLocked(
			lot.Producer,
			entryOrderID,
		)

	if matchedAttempt == nil {
		return false
	}

	/*
		Do not rewrite an already-terminal exit.

		Once ProducerStageExited exists, its terminal time must remain
		the time at which this producer exposure first ceased to be live.
	*/
	if _, exists :=
		matchedAttempt.Events[ProducerStageExited]; exists {

		return false
	}

	exitTime :=
		matchedAttempt.LastRealizedAt

	if exitTime.IsZero() {
		exitTime = rec.Time
	}

	if exitTime.IsZero() {
		exitTime = time.Now().UTC()
	}

	matchedAttempt.Events[ProducerStageExited] =
		ProducerEvent{
			Time:      exitTime,
			CreatedAt: matchedAttempt.CreatedAt,

			Producer: matchedAttempt.Producer,
			Side:     matchedAttempt.Side,
			Stage:    ProducerStageExited,

			DecisionID: matchedAttempt.DecisionID,

			/*
				OrderID here is the exchange exit order that produced
				the latest realized contribution.

				Entry correlation remains available through the
				filled/committed lifecycle events.
			*/
			OrderID: strings.TrimSpace(
				rec.ExitOrderID,
			),

			Reason: rec.Reason,

			/*
				Do not add rec.PNLUSD here.

				recordProducerRealizedPnLLocked() already accumulated
				every partial/final realized contribution.
			*/
			PnL:   matchedAttempt.RealizedPnLUSD,
			Price: rec.ClosePrice,
		}

	return true
}

// recordProducerRealizedPnLLocked records realized NET PnL against the
// already-existing producer attempt.
//
// The caller MUST already hold t.mu.
//
// This helper does NOT:
//   - create a ProducerAttempt;
//   - create or regenerate DecisionID;
//   - create or regenerate CreatedAt;
//   - create ProducerStageExited;
//   - decide whether exposure is still open;
//   - prune history;
//   - persist producer history.
//
// Every authoritative ExitRecord.PNLUSD attributable to this entry is added
// to ProducerAttempt.RealizedPnLUSD, including partial exits.
//
// Correlation is:
//
//	lot.Producer
//	    +
//	ExitRecord.EntryOrderID
//	    ↓
//	existing ProducerAttempt committed/filled OrderID
func (t *Trader) recordProducerRealizedPnLLocked(
	lot *Position,
	rec ExitRecord,
) bool {
	if t == nil ||
		lot == nil ||
		lot.Producer == EntryProducerNone {

		return false
	}

	entryOrderID := strings.TrimSpace(
		rec.EntryOrderID,
	)

	if entryOrderID == "" {
		entryOrderID =
			strings.TrimSpace(
				lot.EntryOrderID,
			)
	}

	if entryOrderID == "" {
		return false
	}

	matchedAttempt :=
		t.findProducerAttemptByEntryOrderIDLocked(
			lot.Producer,
			entryOrderID,
		)

	if matchedAttempt == nil {
		return false
	}

	/*
		ExitRecord.PNLUSD is authoritative realized NET PnL.

		Partial exits accumulate here. This does not imply that the
		producer-created exposure has completely exited.
	*/
	matchedAttempt.RealizedPnLUSD +=
		rec.PNLUSD

	realizedAt := rec.Time
	if realizedAt.IsZero() {
		realizedAt =
			time.Now().UTC()
	}

	/*
		LastRealizedAt means exactly that: latest realized contribution.

		It is not automatically the final-exit retention anchor.
	*/
	if matchedAttempt.LastRealizedAt.IsZero() ||
		realizedAt.After(
			matchedAttempt.LastRealizedAt,
		) {

		matchedAttempt.LastRealizedAt =
			realizedAt
	}

	return true
}

// recordProducerAttemptLocked registers or enriches one producer attempt in
// the in-memory producer history.
//
// The caller MUST already hold t.mu.
//
// Behavior 1 — register:
// If DecisionID does not yet exist, store this ProducerAttempt as the
// authoritative attempt for that producer decision.
//
// Behavior 2 — enrich:
// If DecisionID already exists, preserve the original ProducerAttempt and
// merge lifecycle events from the incoming attempt into its Events map.
//
// DecisionID, CreatedAt, Producer, and Side are permanent attempt identity.
// They must never be regenerated or replaced during enrichment.
//
// This helper performs only the mechanical producer-history mutation.
// It does not classify errors, apply retry policy, perform cleanup, persist
// history, or alter trading behavior.
func (t *Trader) recordProducerAttemptLocked(
	attempt *ProducerAttempt,
) {
	if t == nil || attempt == nil {
		return
	}

	if t.producerHistory == nil {
		t.producerHistory =
			make(map[EntryProducer]*ProducerHistory)
	}

	history := t.producerHistory[attempt.Producer]
	if history == nil {
		history = &ProducerHistory{
			Attempts: make(
				map[string]*ProducerAttempt,
			),
		}

		t.producerHistory[attempt.Producer] =
			history
	}

	if history.Attempts == nil {
		history.Attempts =
			make(map[string]*ProducerAttempt)
	}

	existingAttempt, exists :=
		history.Attempts[attempt.DecisionID]

	/*
		Behavior 1 — REGISTER

		This DecisionID has not been seen before.

		The incoming ProducerAttempt becomes the authoritative attempt
		whose identity remains fixed for the entire producer lifecycle.
	*/
	if !exists || existingAttempt == nil {
		if attempt.Events == nil {
			attempt.Events =
				make(map[ProducerStage]ProducerEvent)
		}

		history.Attempts[attempt.DecisionID] =
			attempt

		return
	}

	/*
		Behavior 2 — ENRICH

		The producer decision already exists.

		Never replace the original ProducerAttempt. Merge only lifecycle
		events discovered later under the same permanent DecisionID.
	*/
	if existingAttempt.Events == nil {
		existingAttempt.Events =
			make(map[ProducerStage]ProducerEvent)
	}

	for stage, event := range attempt.Events {
		existingAttempt.Events[stage] = event
	}
}

const ProducerHistoryMaxAttemptsPerProducer = 500

func (t *Trader) foldProducerAttemptPrunedErrorCodesLocked(
	producer EntryProducer,
	attempt *ProducerAttempt,
) {
	if t == nil || attempt == nil {
		return
	}

	if t.producerEconomics == nil {
		t.producerEconomics =
			make(map[EntryProducer]*ProducerEconomics)
	}

	econ := t.producerEconomics[producer]
	if econ == nil {
		econ = &ProducerEconomics{Producer: producer}
		t.producerEconomics[producer] = econ
	}

	if econ.PrunedErrorCodeCounts == nil {
		econ.PrunedErrorCodeCounts =
			make(map[EntryProduceErrorCode]uint64)
	}

	// Events is keyed by ProducerStage, so one attempt can contribute at most
	// one count for a particular stage's ErrorCode. Empty codes are ignored.
	for _, event := range attempt.Events {
		if event.ErrorCode == "" {
			continue
		}

		econ.PrunedErrorCodeCounts[event.ErrorCode]++
	}
}

type producerAttemptByAge struct {
	decisionID string
	attempt    *ProducerAttempt
}

// producerAttemptCountPruneSafeLocked protects unresolved/live exposure
// evidence from count-based deletion. In normal operation these protected
// attempts are few; the 500 limit applies to the prune-safe detailed history.
func (t *Trader) producerAttemptCountPruneSafeLocked(
	attempt *ProducerAttempt,
) bool {
	if attempt == nil || attempt.Events == nil {
		return true
	}

	_, filled := attempt.Events[ProducerStageFilled]
	_, committed := attempt.Events[ProducerStageCommitted]
	_, refundConsumed := attempt.Events[ProducerStageRefundConsumed]
	_, exited := attempt.Events[ProducerStageExited]

	if !committed {
		return !filled || refundConsumed
	}

	entryOrderID := producerAttemptEntryOrderID(attempt)
	if entryOrderID != "" &&
		t.producerEntryOrderLiveLocked(entryOrderID) {
		return false
	}

	return exited
}

func (t *Trader) pruneProducerHistoryCountCapLocked(
	producer EntryProducer,
	history *ProducerHistory,
) bool {
	if t == nil ||
		history == nil ||
		history.Attempts == nil ||
		ProducerHistoryMaxAttemptsPerProducer <= 0 ||
		len(history.Attempts) <= ProducerHistoryMaxAttemptsPerProducer {
		return false
	}

	ordered := make(
		[]producerAttemptByAge,
		0,
		len(history.Attempts),
	)

	for decisionID, attempt := range history.Attempts {
		ordered = append(
			ordered,
			producerAttemptByAge{
				decisionID: decisionID,
				attempt:    attempt,
			},
		)
	}

	sort.Slice(
		ordered,
		func(i, j int) bool {
			left := ordered[i].attempt
			right := ordered[j].attempt

			if left == nil {
				return right != nil
			}
			if right == nil {
				return false
			}

			lt := left.CreatedAt
			rt := right.CreatedAt

			if lt.Equal(rt) {
				return ordered[i].decisionID <
					ordered[j].decisionID
			}
			if lt.IsZero() {
				return true
			}
			if rt.IsZero() {
				return false
			}

			return lt.Before(rt)
		},
	)

	changed := false

	for _, candidate := range ordered {
		if len(history.Attempts) <=
			ProducerHistoryMaxAttemptsPerProducer {
			break
		}

		attempt, exists :=
			history.Attempts[candidate.decisionID]
		if !exists {
			continue
		}

		if attempt == nil {
			delete(history.Attempts, candidate.decisionID)
			changed = true
			continue
		}

		if !t.producerAttemptCountPruneSafeLocked(attempt) {
			continue
		}

		// Exact-once accounting boundary for count-cap pruning: preserve the
		// normal economics and the durable ErrorCode histogram before deletion.
		t.foldProducerAttemptPrunedErrorCodesLocked(
			producer,
			attempt,
		)

		t.foldProducerAttemptEconomicsLocked(
			producer,
			attempt,
		)

		delete(history.Attempts, candidate.decisionID)
		changed = true
	}

	if len(history.Attempts) >
		ProducerHistoryMaxAttemptsPerProducer {
		log.Printf(
			"[WARN] producer.history.count_cap.protected "+
				"producer=%s retained=%d cap=%d "+
				"reason=protected_exposure_evidence",
			producer,
			len(history.Attempts),
			ProducerHistoryMaxAttemptsPerProducer,
		)
	}

	return changed
}

// foldProducerAttemptEconomicsLocked folds one soon-to-be-pruned attempt into
// the permanent bounded producer aggregate.
//
// The caller MUST already hold t.mu. Call this only immediately before deleting
// that attempt from producerHistory; the attempt itself is the exact-once
// accounting boundary.
func (t *Trader) foldProducerAttemptEconomicsLocked(
	producer EntryProducer,
	attempt *ProducerAttempt,
) {
	if t == nil || attempt == nil {
		return
	}

	if t.producerEconomics == nil {
		t.producerEconomics =
			make(map[EntryProducer]*ProducerEconomics)
	}

	econ := t.producerEconomics[producer]
	if econ == nil {
		econ = &ProducerEconomics{Producer: producer}
		t.producerEconomics[producer] = econ
	}

	econ.Attempts++

	if econ.LastAttemptAt.IsZero() || attempt.CreatedAt.After(econ.LastAttemptAt) {
		econ.LastAttemptAt = attempt.CreatedAt
	}

	if produced, ok := attempt.Events[ProducerStageProduced]; ok {
		if econ.LastProducedAt.IsZero() || produced.Time.After(econ.LastProducedAt) {
			econ.LastProducedAt = produced.Time
		}
	}

	if filled, ok := attempt.Events[ProducerStageFilled]; ok {
		econ.Fills++
		if econ.LastFillAt.IsZero() || filled.Time.After(econ.LastFillAt) {
			econ.LastFillAt = filled.Time
		}
	}

	if _, ok := attempt.Events[ProducerStageCleanupCancelled]; ok {
		econ.CancelledAttempts++
	}

	if producerAttemptFailed(attempt) {
		econ.FailedAttempts++
	}

	if exited, ok := attempt.Events[ProducerStageExited]; ok {
		econ.ClosedPositions++
		econ.NetPnLUSD += attempt.RealizedPnLUSD

		switch {
		case attempt.RealizedPnLUSD > 0:
			econ.Wins++
		case attempt.RealizedPnLUSD < 0:
			econ.Losses++
		}

		if econ.LastExitAt.IsZero() || exited.Time.After(econ.LastExitAt) {
			econ.LastExitAt = exited.Time
		}
	}

	if latest, ok := latestProducerEvent(attempt); ok {
		activityAt := latest.Time
		if activityAt.IsZero() {
			activityAt = latest.CreatedAt
		}

		if econ.LastActivityAt.IsZero() || activityAt.After(econ.LastActivityAt) {
			econ.LastActivityAt = activityAt
			econ.LastActivityStage = latest.Stage
			econ.LastActivityReason = latest.Reason
			econ.LastActivityErrorCode = latest.ErrorCode
			econ.LastActivityError = latest.Error
		}
	}
}

// ProducerOpsSummaries returns dashboard-ready producer aggregates. It combines
// permanent folded economics with retained detailed attempts, so BOT OPS does
// not have to wait for the 24-hour prune boundary to show current activity.
func (t *Trader) ProducerOpsSummaries() map[EntryProducer]ProducerOpsSummary {
	if t == nil {
		return map[EntryProducer]ProducerOpsSummary{}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.producerOpsSummariesLocked()
}

// producerOpsSummariesLocked is the lock-aware implementation used by future
// BOT OPS/frontend handlers that already own t.mu.
func (t *Trader) producerOpsSummariesLocked() map[EntryProducer]ProducerOpsSummary {
	out := make(map[EntryProducer]ProducerOpsSummary)
	if t == nil {
		return out
	}

	for producer, econ := range t.producerEconomics {
		if econ == nil {
			continue
		}

		s := ProducerOpsSummary{
			Producer:              producer,
			Attempts:              econ.Attempts,
			Fills:                 econ.Fills,
			CancelledAttempts:     econ.CancelledAttempts,
			FailedAttempts:        econ.FailedAttempts,
			ClosedPositions:       econ.ClosedPositions,
			Wins:                  econ.Wins,
			Losses:                econ.Losses,
			NetPnLUSD:             econ.NetPnLUSD,
			RealizedPnLUSD:        econ.NetPnLUSD,
			LastAttemptAt:         econ.LastAttemptAt,
			LastProducedAt:        econ.LastProducedAt,
			LastFillAt:            econ.LastFillAt,
			LastExitAt:            econ.LastExitAt,
			LastActivityAt:        econ.LastActivityAt,
			LastActivityStage:     econ.LastActivityStage,
			LastActivityReason:    econ.LastActivityReason,
			LastActivityErrorCode: econ.LastActivityErrorCode,
			LastActivityError:     econ.LastActivityError,
		}

		out[producer] = s
	}

	for producer, history := range t.producerHistory {
		if history == nil || history.Attempts == nil {
			continue
		}

		s := out[producer]
		s.Producer = producer

		for _, attempt := range history.Attempts {
			if attempt == nil {
				continue
			}

			s.Attempts++
			if s.LastAttemptAt.IsZero() || attempt.CreatedAt.After(s.LastAttemptAt) {
				s.LastAttemptAt = attempt.CreatedAt
			}

			if produced, ok := attempt.Events[ProducerStageProduced]; ok {
				if s.LastProducedAt.IsZero() || produced.Time.After(s.LastProducedAt) {
					s.LastProducedAt = produced.Time
				}
			}

			if filled, ok := attempt.Events[ProducerStageFilled]; ok {
				s.Fills++
				if s.LastFillAt.IsZero() || filled.Time.After(s.LastFillAt) {
					s.LastFillAt = filled.Time
				}
			}

			if _, ok := attempt.Events[ProducerStageCleanupCancelled]; ok {
				s.CancelledAttempts++
			}

			if producerAttemptFailed(attempt) {
				s.FailedAttempts++
			}

			s.RealizedPnLUSD += attempt.RealizedPnLUSD

			if exited, ok := attempt.Events[ProducerStageExited]; ok {
				s.ClosedPositions++
				s.NetPnLUSD += attempt.RealizedPnLUSD
				switch {
				case attempt.RealizedPnLUSD > 0:
					s.Wins++
				case attempt.RealizedPnLUSD < 0:
					s.Losses++
				}
				if s.LastExitAt.IsZero() || exited.Time.After(s.LastExitAt) {
					s.LastExitAt = exited.Time
				}
			} else if _, committed := attempt.Events[ProducerStageCommitted]; committed {
				entryOrderID := producerAttemptEntryOrderID(attempt)
				if entryOrderID != "" && t.producerEntryOrderLiveLocked(entryOrderID) {
					s.OpenPositions++
				}
			}

			if latest, ok := latestProducerEvent(attempt); ok {
				activityAt := latest.Time
				if activityAt.IsZero() {
					activityAt = latest.CreatedAt
				}
				if s.LastActivityAt.IsZero() || activityAt.After(s.LastActivityAt) {
					s.LastActivityAt = activityAt
					s.LastActivityStage = latest.Stage
					s.LastActivityDecision = latest.DecisionID
					s.LastActivityOrderID = latest.OrderID
					s.LastActivityReason = latest.Reason
					s.LastActivityErrorCode = latest.ErrorCode
					s.LastActivityError = latest.Error
				}
			}
		}

		out[producer] = s
	}

	for producer, s := range out {
		if s.ClosedPositions > 0 {
			s.WinRatePct = float64(s.Wins) / float64(s.ClosedPositions) * 100.0
			s.AveragePnLUSD = s.NetPnLUSD / float64(s.ClosedPositions)
		}
		out[producer] = s
	}

	return out
}

// ProducerObservabilitySnapshot returns a deep read-only copy suitable for
// API/BOT OPS serialization without exposing mutable in-memory maps.
func (t *Trader) ProducerObservabilitySnapshot() ProducerObservabilityState {
	state := ProducerObservabilityState{
		History:   make(map[EntryProducer]*ProducerHistory),
		Economics: make(map[EntryProducer]*ProducerEconomics),
	}

	if t == nil {
		return state
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	for producer, history := range t.producerHistory {
		if history == nil {
			continue
		}

		historyCopy := &ProducerHistory{
			Attempts: make(map[string]*ProducerAttempt),
		}

		for decisionID, attempt := range history.Attempts {
			if attempt == nil {
				continue
			}

			attemptCopy := *attempt
			attemptCopy.Events = make(map[ProducerStage]ProducerEvent)
			for stage, event := range attempt.Events {
				attemptCopy.Events[stage] = event
			}

			historyCopy.Attempts[decisionID] = &attemptCopy
		}

		state.History[producer] = historyCopy
	}

	for producer, economics := range t.producerEconomics {
		if economics == nil {
			continue
		}

		economicsCopy := *economics

		if economics.PrunedErrorCodeCounts != nil {
			economicsCopy.PrunedErrorCodeCounts =
				make(map[EntryProduceErrorCode]uint64, len(economics.PrunedErrorCodeCounts))
			for code, count := range economics.PrunedErrorCodeCounts {
				economicsCopy.PrunedErrorCodeCounts[code] = count
			}
		}

		state.Economics[producer] = &economicsCopy
	}

	return state
}

const ProducerHistoryRetention = 24 * time.Hour

func (t *Trader) pruneProducerHistoryLocked(
	now time.Time,
) bool {
	if t == nil || len(t.producerHistory) == 0 {
		return false
	}

	now = now.UTC()
	cutoff := now.Add(-ProducerHistoryRetention)
	changed := false

	if t.producerEconomics == nil {
		t.producerEconomics =
			make(map[EntryProducer]*ProducerEconomics)
	}

	/*
		ProducerAttempt deletion is the exact-once accounting boundary.
		Every eligible attempt is folded immediately before deletion.
	*/

	for producer, history := range t.producerHistory {

		if history == nil {
			delete(
				t.producerHistory,
				producer,
			)

			changed = true
			continue
		}

		if history.Attempts == nil {
			delete(
				t.producerHistory,
				producer,
			)

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

			if attempt.Events == nil {
				attempt.Events =
					make(map[ProducerStage]ProducerEvent)
			}

			_, filled :=
				attempt.Events[ProducerStageFilled]

			_, committed :=
				attempt.Events[ProducerStageCommitted]

			_, refundConsumed :=
				attempt.Events[ProducerStageRefundConsumed]

			exitedEvent, exited :=
				attempt.Events[ProducerStageExited]

			/*
				CASE A — NO COMMITTED EXPOSURE.

				A normal blocked, failed, cancelled, rejected, expired,
				deferred, produced-only, or refund-consumed attempt uses
				the original producer Decision CreatedAt as its retention
				anchor.

				Special protection:

				filled && !committed && !refundConsumed

				is NOT safely classifiable as "no exposure".

				The exchange reported a real fill, but we do not have
				proof that the lifecycle reached committed exposure or
				the known refund-consumed terminal path.

				Retain that attempt for reconciliation rather than
				destroying evidence after 24 hours.
			*/
			if !committed {
				if filled &&
					!refundConsumed {

					continue
				}

				if attempt.CreatedAt.IsZero() ||
					!attempt.CreatedAt.Before(
						cutoff,
					) {

					continue
				}

				t.foldProducerAttemptPrunedErrorCodesLocked(
					producer,
					attempt,
				)

				t.foldProducerAttemptEconomicsLocked(
					producer,
					attempt,
				)

				delete(
					history.Attempts,
					decisionID,
				)

				changed = true
				continue
			}

			/*
				CASE B — COMMITTED EXPOSURE.

				CreatedAt no longer controls retention.

				While the producer's original EntryOrderID is still in an
				authoritative SideBook, keep the detailed attempt for as
				long as necessary.
			*/
			entryOrderID := producerAttemptEntryOrderID(attempt)

			if entryOrderID != "" &&
				t.producerEntryOrderLiveLocked(
					entryOrderID,
				) {

				continue
			}

			/*
				A committed attempt that is no longer found in the live
				SideBooks still MUST NOT be deleted until the terminal
				ProducerStageExited event exists.

				This protects:
				  - incomplete observability;
				  - restart/reconciliation edge cases;
				  - any path where economic closure has not yet been
				    authoritatively recorded.
			*/
			if !exited {
				continue
			}

			if exitedEvent.Time.IsZero() ||
				!exitedEvent.Time.Before(
					cutoff,
				) {

				continue
			}

			/*
				The position is no longer live and its terminal exit is
				older than the detailed-retention window.

				This is the single accounting boundary:

				    fold economics
				        ↓
				    delete detailed attempt

				Because deletion occurs immediately after folding, this
				attempt cannot be counted again by a later prune pass.
			*/
			t.foldProducerAttemptPrunedErrorCodesLocked(
				producer,
				attempt,
			)

			t.foldProducerAttemptEconomicsLocked(
				producer,
				attempt,
			)

			delete(
				history.Attempts,
				decisionID,
			)

			changed = true
		}

		/*
			Second retention bound: after normal 24-hour/exposure pruning,
			prune the oldest safe attempts until this producer is at the
			configured detailed-history count ceiling.
		*/
		if t.pruneProducerHistoryCountCapLocked(
			producer,
			history,
		) {
			changed = true
		}

		/*
			Detailed history containers may disappear after all attempts
			have been folded.

			producerEconomics remains independently durable.
		*/
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

func (t *Trader) producerHistoryControlDir() string {
	if t == nil || strings.TrimSpace(t.producerHistoryFile) == "" {
		return ""
	}

	return filepath.Join(
		filepath.Dir(t.producerHistoryFile),
		"producer_controls",
	)
}

func producerResetRequestName(producer EntryProducer) string {
	return fmt.Sprintf("reset_pruned_errors_%s.request", producer)
}

func (t *Trader) applyProducerObservabilityControlRequestsLocked() {
	if t == nil || len(t.producerEconomics) == 0 {
		return
	}

	dir := t.producerHistoryControlDir()
	if dir == "" {
		return
	}

	for producer, economics := range t.producerEconomics {
		if economics == nil {
			continue
		}

		requestPath := filepath.Join(
			dir,
			producerResetRequestName(producer),
		)

		if _, err := os.Stat(requestPath); err != nil {
			continue
		}

		economics.PrunedErrorCodeCounts =
			make(map[EntryProduceErrorCode]uint64)

		if err := os.Remove(requestPath); err != nil &&
			!os.IsNotExist(err) {
			log.Printf(
				"[WARN] producer.history.reset_request.remove_failed "+
					"producer=%s path=%s err=%v",
				producer,
				requestPath,
				err,
			)
		}

		log.Printf(
			"[PRODUCER] pruned_error_counts_reset producer=%s",
			producer,
		)
	}
}

// saveProducerHistoryNoLock persists the current producer history.
//
// The caller MUST already hold t.mu or otherwise guarantee that
// t.producerHistory is stable for the duration of pruning and serialization.
//
// This function:
//   - applies exposure-aware detailed retention;
//   - folds pruned attempts into durable producer economics;
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

	if t.producerHistory == nil {
		t.producerHistory =
			make(map[EntryProducer]*ProducerHistory)
	}

	if t.producerEconomics == nil {
		t.producerEconomics =
			make(map[EntryProducer]*ProducerEconomics)
	}

	t.applyProducerObservabilityControlRequestsLocked()

	/*
		Pruning remains inside the producer-observability persistence
		boundary. Non-exposure attempts expire from CreatedAt; committed
		live exposure is retained; exited exposure expires from the
		terminal ProducerStageExited time; economics is folded exactly
		once immediately before detailed-attempt deletion.
	*/
	t.pruneProducerHistoryLocked(
		time.Now().UTC(),
	)

	state := ProducerObservabilityState{
		History:   t.producerHistory,
		Economics: t.producerEconomics,
	}

	bs, err := json.MarshalIndent(
		state,
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

			t.producerEconomics =
				make(map[EntryProducer]*ProducerEconomics)

			return nil
		}

		return fmt.Errorf(
			"load producer history: read: %w",
			err,
		)
	}

	state := ProducerObservabilityState{
		History: make(
			map[EntryProducer]*ProducerHistory,
		),
		Economics: make(
			map[EntryProducer]*ProducerEconomics,
		),
	}

	if len(bs) > 0 {
		/*
			Current format:

			{
			  "History":   { ... },
			  "Economics": { ... }
			}

			The production file may still contain the immediately
			preceding format where producer names were top-level keys.

			Do not silently interpret that old object as an empty
			ProducerObservabilityState.
		*/
		var envelopeProbe struct {
			History   json.RawMessage
			Economics json.RawMessage
		}

		if err := json.Unmarshal(
			bs,
			&envelopeProbe,
		); err != nil {
			return fmt.Errorf(
				"load producer history: inspect format: %w",
				err,
			)
		}

		isEnvelope :=
			len(envelopeProbe.History) > 0 ||
				len(envelopeProbe.Economics) > 0

		if isEnvelope {
			if err := json.Unmarshal(
				bs,
				&state,
			); err != nil {
				return fmt.Errorf(
					"load producer history: unmarshal state: %w",
					err,
				)
			}
		} else {
			/*
				Legacy producer-history-only format.

				This fallback preserves the currently retained detailed
				attempts while economics begins empty.

				The next successful save writes the new envelope format.
			*/
			legacyHistory := make(
				map[EntryProducer]*ProducerHistory,
			)

			if err := json.Unmarshal(
				bs,
				&legacyHistory,
			); err != nil {
				return fmt.Errorf(
					"load producer history: unmarshal legacy history: %w",
					err,
				)
			}

			state.History = legacyHistory
		}
	}

	if state.History == nil {
		state.History =
			make(map[EntryProducer]*ProducerHistory)
	}

	if state.Economics == nil {
		state.Economics =
			make(map[EntryProducer]*ProducerEconomics)
	}

	/*
		Repair detailed-history containers from empty/partial JSON.

		ProducerAttempt identity and lifecycle contents are preserved.
	*/
	for producer, producerHistory := range state.History {

		if producerHistory == nil {
			delete(
				state.History,
				producer,
			)
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

	/*
		Repair economics containers.

		The map key is authoritative producer ownership. If Producer is
		empty in persisted JSON, restore it from that key rather than
		dropping the accumulated economics.
	*/
	for producer, economics := range state.Economics {

		if economics == nil {
			delete(
				state.Economics,
				producer,
			)
			continue
		}

		if economics.Producer == EntryProducerNone {
			economics.Producer = producer
		}
	}

	t.producerHistory =
		state.History

	t.producerEconomics =
		state.Economics

	/*
		Startup retention cleanup uses the same exposure-aware policy as
		normal producer-history persistence.
	*/
	t.pruneProducerHistoryLocked(
		time.Now().UTC(),
	)

	return nil
}
