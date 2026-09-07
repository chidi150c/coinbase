// FILE: resource_manager.go
package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResourceManager is the single in-process resource ledger and allocation
// authority. Trader retains lifecycle objects, but spendable quote/base is
// derived only from this actor-owned ledger.
type ResourceManager struct {
	commands chan resourceManagerCommand
}

type resourceManagerCommandKind uint8

const (
	resourceManagerReserve resourceManagerCommandKind = iota + 1
	resourceManagerRelease
	resourceManagerTotals
	resourceManagerAllocate
	resourceManagerBuildSnapshot
	resourceManagerQuarantine
)

type ResourceReservationKind string

const (
	ResourceReservationTransient   ResourceReservationKind = "transient"
	ResourceReservationLot         ResourceReservationKind = "lot"
	ResourceReservationPendingEntry ResourceReservationKind = "pending_entry"
	ResourceReservationPendingExit ResourceReservationKind = "pending_exit"
	ResourceReservationRefund      ResourceReservationKind = "refund"
	ResourceReservationCase3A      ResourceReservationKind = "case3a"
	ResourceReservationQuarantine  ResourceReservationKind = "quarantine"
)

// ResourceExposure is an immutable copy of Trader-owned lot or pending-entry
// exposure. ResourceManager consumes copies so its actor never reads Trader
// maps/books or needs Trader.mu.
type ResourceExposure struct {
	ID       string
	Kind     ResourceReservationKind
	Producer EntryProducer
	Side     OrderSide
	Base     float64
	QuoteUSD float64
	// Informational records establish ownership/lineage but do not subtract
	// funds already represented by another ledger record.
	Informational bool
}

type ResourceSnapshotInput struct {
	Balance balanceSnapshot
	MaxAge  time.Duration

	Price       float64
	MinNotional float64
	FeeRatePct  float64

	RequireBaseForShort bool
	MaxConcurrentLots   int

	Exposures []ResourceExposure
}

type resourceManagerCommand struct {
	kind resourceManagerCommandKind

	reservation ProducerResourceReservation
	decisionID  string

	snapshot         ResourceSnapshot
	requests         []ProducerResourceRequest
	balanceAvailable bool
	snapshotInput    ResourceSnapshotInput

	reply chan resourceManagerResponse
}

type resourceManagerResponse struct {
	err        error
	quoteUSD   float64
	base       float64
	plan       AllocationPlan
	snapshot   ResourceSnapshot
	snapshotOK bool
}

func newResourceManager() *ResourceManager {
	m := &ResourceManager{
		commands: make(chan resourceManagerCommand),
	}
	go m.run()
	return m
}

func (m *ResourceManager) run() {
	reservations := make(map[string]ProducerResourceReservation)
	coordinator := ProducerResourceCoordinator{}

	for command := range m.commands {
		response := resourceManagerResponse{}

		switch command.kind {
		case resourceManagerReserve:
			reservation := command.reservation
			if _, exists := reservations[reservation.DecisionID]; exists {
				response.err = fmt.Errorf(
					"resource manager: duplicate DecisionID=%s",
					reservation.DecisionID,
				)
			} else {
				reservations[reservation.DecisionID] = reservation
			}

		case resourceManagerRelease:
			delete(reservations, command.decisionID)

		case resourceManagerTotals:
			for _, reservation := range reservations {
				if reservation.Informational {
					continue
				}
				switch reservation.Side {
				case SideBuy:
					response.quoteUSD += reservation.QuoteUSD
				case SideSell:
					response.base += reservation.Base
				}
			}

		case resourceManagerAllocate:
			response.plan = coordinator.Allocate(
				command.snapshot,
				command.requests,
				command.balanceAvailable,
			)

	case resourceManagerBuildSnapshot:
			// Replace state-derived ledger records atomically. Transient grants and
			// quarantines survive reconciliation until an explicit terminal action.
			for id, reservation := range reservations {
				switch reservation.Kind {
				case ResourceReservationLot,
					ResourceReservationPendingEntry,
					ResourceReservationPendingExit,
					ResourceReservationRefund,
					ResourceReservationCase3A:
					delete(reservations, id)
				}
			}
			for _, exposure := range command.snapshotInput.Exposures {
				id := strings.TrimSpace(exposure.ID)
				if id == "" {
					continue
				}
				reservations[id] = ProducerResourceReservation{
					DecisionID: id,
					Producer: exposure.Producer,
					Side: exposure.Side,
					QuoteUSD: exposure.QuoteUSD,
					Base: exposure.Base,
					CreatedAt: time.Now().UTC(),
					Kind: exposure.Kind,
					Informational: exposure.Informational,
				}
			}
			response.snapshot, response.snapshotOK =
				buildResourceSnapshot(command.snapshotInput, reservations)

		case resourceManagerQuarantine:
			reservation := command.reservation
			reservation.Kind = ResourceReservationQuarantine
			reservation.Informational = false
			reservations[reservation.DecisionID] = reservation

		default:
			response.err = errors.New("resource manager: unknown command")
		}

		command.reply <- response
	}
}

func buildResourceSnapshot(
	input ResourceSnapshotInput,
	reservations map[string]ProducerResourceReservation,
) (ResourceSnapshot, bool) {
	reservedQuote := 0.0
	reservedBase := 0.0
	currentLots := 0
	for _, reservation := range reservations {
		if reservation.Kind == ResourceReservationLot {
			currentLots++
		}
		if reservation.Informational {
			continue
		}
		switch reservation.Side {
		case SideBuy:
			reservedQuote += reservation.QuoteUSD
		case SideSell:
			reservedBase += reservation.Base
		}
	}

	spareQuote := input.Balance.AvailQuote - reservedQuote
	spareBase := input.Balance.AvailBase - reservedBase
	if spareQuote < 0 {
		spareQuote = 0
	}
	if spareBase < 0 {
		spareBase = 0
	}

	snapshot := ResourceSnapshot{
		UpdatedAt:         input.Balance.UpdatedAt,
		SymQuote:          input.Balance.SymQuote,
		SymBase:           input.Balance.SymBase,
		AvailQuote:        input.Balance.AvailQuote,
		AvailBase:         input.Balance.AvailBase,
		QuoteStep:         input.Balance.QuoteStep,
		BaseStep:          input.Balance.BaseStep,
		ReservedQuote:     reservedQuote,
		ReservedBase:      reservedBase,
		SpareQuote:        spareQuote,
		SpareBase:         spareBase,
		Price:             input.Price,
		MinNotional:       input.MinNotional,
		CurrentLots:       currentLots,
		AvailableLotSlots: -1,
	}

	if input.MaxConcurrentLots > 0 {
		snapshot.AvailableLotSlots =
			input.MaxConcurrentLots - snapshot.CurrentLots
		if snapshot.AvailableLotSlots < 0 {
			snapshot.AvailableLotSlots = 0
		}
	}

	balanceOK := !input.Balance.UpdatedAt.IsZero() &&
		(input.MaxAge <= 0 || time.Since(input.Balance.UpdatedAt) <= input.MaxAge) &&
		input.Balance.SymQuote != "" &&
		input.Balance.SymBase != "" &&
		input.Balance.QuoteStep > 0 &&
		input.Balance.BaseStep > 0

	return snapshot, balanceOK
}

func (m *ResourceManager) reserve(
	reservation ProducerResourceReservation,
) error {
	if m == nil {
		return errors.New("resource manager: nil manager")
	}
	reservation.DecisionID = strings.TrimSpace(reservation.DecisionID)
	if reservation.Kind == "" {
		reservation.Kind = ResourceReservationTransient
	}
	reply := make(chan resourceManagerResponse, 1)
	m.commands <- resourceManagerCommand{
		kind:        resourceManagerReserve,
		reservation: reservation,
		reply:       reply,
	}
	return (<-reply).err
}

func (m *ResourceManager) quarantine(reservation ProducerResourceReservation) error {
	if m == nil {
		return errors.New("resource manager: nil manager")
	}
	reservation.DecisionID = strings.TrimSpace(reservation.DecisionID)
	if reservation.DecisionID == "" {
		return errors.New("resource manager: quarantine missing stable ID")
	}
	reply := make(chan resourceManagerResponse, 1)
	m.commands <- resourceManagerCommand{
		kind: resourceManagerQuarantine,
		reservation: reservation,
		reply: reply,
	}
	return (<-reply).err
}

func (m *ResourceManager) release(decisionID string) {
	if m == nil {
		return
	}
	decisionID = strings.TrimSpace(decisionID)
	if decisionID == "" {
		return
	}
	reply := make(chan resourceManagerResponse, 1)
	m.commands <- resourceManagerCommand{
		kind:       resourceManagerRelease,
		decisionID: decisionID,
		reply:      reply,
	}
	<-reply
}

func (m *ResourceManager) totals() (quoteUSD float64, base float64) {
	if m == nil {
		return 0, 0
	}
	reply := make(chan resourceManagerResponse, 1)
	m.commands <- resourceManagerCommand{
		kind:  resourceManagerTotals,
		reply: reply,
	}
	response := <-reply
	return response.quoteUSD, response.base
}

// allocateAsync queues pure allocation-plan calculation on the resource actor.
// The returned channel is buffered so the manager never depends on when the
// caller begins receiving. Current synchronous behavior is preserved by the
// coordinator caller waiting before it installs reservations.
func (m *ResourceManager) allocateAsync(
	snapshot ResourceSnapshot,
	requests []ProducerResourceRequest,
	balanceAvailable bool,
) <-chan AllocationPlan {
	result := make(chan AllocationPlan, 1)
	if m == nil {
		result <- AllocationPlan{Snapshot: snapshot}
		close(result)
		return result
	}

	reply := make(chan resourceManagerResponse, 1)
	m.commands <- resourceManagerCommand{
		kind:             resourceManagerAllocate,
		snapshot:         snapshot,
		requests:         requests,
		balanceAvailable: balanceAvailable,
		reply:            reply,
	}
	go func() {
		response := <-reply
		result <- response.plan
		close(result)
	}()
	return result
}

func (m *ResourceManager) buildSnapshotAsync(
	input ResourceSnapshotInput,
) <-chan resourceManagerResponse {
	result := make(chan resourceManagerResponse, 1)
	if m == nil {
		result <- resourceManagerResponse{}
		close(result)
		return result
	}

	reply := make(chan resourceManagerResponse, 1)
	m.commands <- resourceManagerCommand{
		kind:          resourceManagerBuildSnapshot,
		snapshotInput: input,
		reply:         reply,
	}
	go func() {
		result <- <-reply
		close(result)
	}()
	return result
}
