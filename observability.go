// FILE: observability.go
// ProducerAttempt is the single lifecycle object keyed by DecisionID.
// Events map[ProducerStage]ProducerEvent lets one attempt accumulate produced → pending → filled → committed, or failure/cleanup stages.
// recordProducerAttemptLocked() is deliberately mechanical and does not classify failures or alter trading behavior.
// Retention is correctly based on attempt.CreatedAt, not the latest event time.
// Persistence is isolated in producerHistoryFile and uses temp-file + rename.
// Loading repairs nil maps and prunes expired attempts.
// The additional drain/poller/commit stages are compatible with the lifecycle work already in trader.go.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
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

	LastAttemptAt time.Time
	LastFillAt    time.Time
	LastExitAt    time.Time
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

	// Accumulated realized NET PnL attributable to this producer attempt.
	//
	// Every authoritative ExitRecord.PNLUSD for this entry contributes
	// exactly once here, including partial exits.
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

func newProducerDecisionLifecycle(
	d *EntryDecision,
) (*PendingIntent, *ProducerAttempt) {
	if d == nil ||
		d.Producer == EntryProducerNone {

		return nil, nil
	}

	createdAt := time.Now().UTC()

	var side OrderSide

	if resolvedSide, ok := d.SignalToSide(); ok {
		side = resolvedSide
	}

	intent := &PendingIntent{
		CreatedAt: createdAt,
		DecisionID: FormatDecisionID(
			d.Producer,
			createdAt,
		),

		Producer:            d.Producer,
		PendingCancelPolicy: d.PendingCancelPolicy,
		ProducerReason:      d.ProducerReason,

		Side: side,
	}

	attemptSide := fmt.Sprint(d.Signal)

	if side == SideBuy ||
		side == SideSell {

		attemptSide =
			fmt.Sprint(side)
	}

	attempt := &ProducerAttempt{
		DecisionID: intent.DecisionID,
		CreatedAt:  intent.CreatedAt,

		Producer: intent.Producer,
		Side:     attemptSide,

		Events: make(
			map[ProducerStage]ProducerEvent,
		),
	}

	return intent, attempt
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

	history :=
		t.producerHistory[lot.Producer]

	if history == nil ||
		history.Attempts == nil {

		return false
	}

	var matchedAttempt *ProducerAttempt

	for _, attempt := range history.Attempts {

		if attempt == nil ||
			attempt.Events == nil {

			continue
		}

		if event, ok :=
			attempt.Events[ProducerStageCommitted]; ok {

			if strings.TrimSpace(
				event.OrderID,
			) == entryOrderID {

				matchedAttempt = attempt
				break
			}
		}

		if event, ok :=
			attempt.Events[ProducerStageFilled]; ok {

			if strings.TrimSpace(
				event.OrderID,
			) == entryOrderID {

				matchedAttempt = attempt
				break
			}
		}
	}

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
			PnL: matchedAttempt.RealizedPnLUSD,
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

	history :=
		t.producerHistory[lot.Producer]

	if history == nil ||
		history.Attempts == nil {

		return false
	}

	var matchedAttempt *ProducerAttempt

	for _, attempt := range history.Attempts {

		if attempt == nil ||
			attempt.Events == nil {

			continue
		}

		/*
			Prefer committed because it represents exposure that was
			successfully incorporated into local trading state.
		*/
		if event, ok :=
			attempt.Events[ProducerStageCommitted]; ok {

			if strings.TrimSpace(
				event.OrderID,
			) == entryOrderID {

				matchedAttempt = attempt
				break
			}
		}

		/*
			Filled is the fallback correlation for a lifecycle where the
			exchange fill is known but committed is not available.
		*/
		if event, ok :=
			attempt.Events[ProducerStageFilled]; ok {

			if strings.TrimSpace(
				event.OrderID,
			) == entryOrderID {

				matchedAttempt = attempt
				break
			}
		}
	}

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
		Fold one ProducerAttempt into the permanent bounded aggregate.

		This is called exactly once: immediately before the attempt is
		deleted from producerHistory.

		The ProducerAttempt itself is therefore the idempotency boundary.
		No ProcessedExits map or other growing dedupe state is required.
	*/
	foldEconomics := func(
		producer EntryProducer,
		attempt *ProducerAttempt,
	) {
		if attempt == nil {
			return
		}

		econ := t.producerEconomics[producer]
		if econ == nil {
			econ = &ProducerEconomics{
				Producer: producer,
			}

			t.producerEconomics[producer] = econ
		}

		econ.Attempts++

		if econ.LastAttemptAt.IsZero() ||
			attempt.CreatedAt.After(
				econ.LastAttemptAt,
			) {

			econ.LastAttemptAt =
				attempt.CreatedAt
		}

		/*
			Fills counts actual producer fills, independently of whether
			the resulting exposure was later committed.

			This preserves visibility for:
			  - successful committed entries;
			  - refund-consumed fills;
			  - other filled attempts.
		*/
		if filled, ok :=
			attempt.Events[ProducerStageFilled]; ok {

			econ.Fills++

			if econ.LastFillAt.IsZero() ||
				filled.Time.After(
					econ.LastFillAt,
				) {

				econ.LastFillAt =
					filled.Time
			}
		}

		/*
			A completed cancellation is counted only when cleanup
			cancellation actually succeeded.

			cancel_requested alone is not sufficient because cleanup may
			have failed.
		*/
		if _, ok :=
			attempt.Events[
				ProducerStageCleanupCancelled
			]; ok {

			econ.CancelledAttempts++
		}

		/*
			FailedAttempts represents authoritative failed producer
			outcomes.

			Do not classify decision_blocked or decision_deferred as
			failures merely because they did not trade.
		*/
		_, decisionFailed :=
			attempt.Events[
				ProducerStageDecisionFailed
			]

		_, entryFailed :=
			attempt.Events[
				ProducerStageEntryFailed
			]

		_, rejected :=
			attempt.Events[
				ProducerStageRejected
			]

		_, cleanupFailed :=
			attempt.Events[
				ProducerStageCleanupCancelFailed
			]

		_, commitFailed :=
			attempt.Events[
				ProducerStageCommitFailed
			]

		if decisionFailed ||
			entryFailed ||
			rejected ||
			cleanupFailed ||
			commitFailed {

			econ.FailedAttempts++
		}

		/*
			Only ProducerStageExited represents completed producer
			exposure economics.

			RealizedPnLUSD already contains every authoritative partial
			and final ExitRecord.PNLUSD contribution.
		*/
		if exited, ok := attempt.Events[ProducerStageExited]; ok {

			econ.ClosedPositions++

			econ.NetPnLUSD +=
				attempt.RealizedPnLUSD

			switch {
			case attempt.RealizedPnLUSD > 0:
				econ.Wins++

			case attempt.RealizedPnLUSD < 0:
				econ.Losses++
			}

			if econ.LastExitAt.IsZero() ||
				exited.Time.After(
					econ.LastExitAt,
				) {

				econ.LastExitAt =
					exited.Time
			}
		}
	}

	for producer, history :=
		range t.producerHistory {

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

		for decisionID, attempt :=
			range history.Attempts {

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
				attempt.Events[
					ProducerStageFilled
				]

			_, committed :=
				attempt.Events[
					ProducerStageCommitted
				]

			_, refundConsumed :=
				attempt.Events[
					ProducerStageRefundConsumed
				]

			exitedEvent, exited :=
				attempt.Events[
					ProducerStageExited
				]

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

				foldEconomics(
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
			entryOrderID := ""

			if committedEvent, ok :=
				attempt.Events[
					ProducerStageCommitted
				]; ok {

				entryOrderID =
					strings.TrimSpace(
						committedEvent.OrderID,
					)
			}

			if entryOrderID == "" {
				if filledEvent, ok :=
					attempt.Events[
						ProducerStageFilled
					]; ok {

					entryOrderID =
						strings.TrimSpace(
							filledEvent.OrderID,
						)
				}
			}

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
			foldEconomics(
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

	if t.producerHistory == nil {
		t.producerHistory =
			make(map[EntryProducer]*ProducerHistory)
	}

	if t.producerEconomics == nil {
		t.producerEconomics =
			make(map[EntryProducer]*ProducerEconomics)
	}

	/*
		Pruning remains inside the producer-observability persistence
		boundary.

		For now this still executes the current pruning policy.

		The next pruning mutation will change that policy so:
		  - non-exposure attempts expire from CreatedAt;
		  - live exposure is retained;
		  - exited exposure expires from ProducerStageExited.Time;
		  - ProducerEconomics is updated immediately before deletion.
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
	for producer, producerHistory :=
		range state.History {

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

		for decisionID, attempt :=
			range producerHistory.Attempts {

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
	for producer, economics :=
		range state.Economics {

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
		Startup retention cleanup.

		IMPORTANT:
		Until the next mutation, pruneProducerHistoryLocked() still
		contains the old CreatedAt-only policy.

		Do not deploy this intermediate revision by itself.
	*/
	t.pruneProducerHistoryLocked(
		time.Now().UTC(),
	)

	return nil
}