package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type ResourceReservationKind string
type ResourceReservationState string

const (
	ResourceReservationLot          ResourceReservationKind = "lot"
	ResourceReservationPendingEntry ResourceReservationKind = "pending_entry"
	ResourceReservationPendingExit  ResourceReservationKind = "pending_exit"
	ResourceReservationRefund       ResourceReservationKind = "refund"
	ResourceReservationCase3A       ResourceReservationKind = "case3a"
	ResourceReservationSubmission   ResourceReservationKind = "submission"
	ResourceReservationQuarantine   ResourceReservationKind = "quarantine"

	ResourceReservationReserved    ResourceReservationState = "reserved"
	ResourceReservationPending     ResourceReservationState = "pending"
	ResourceReservationReconciling ResourceReservationState = "reconciling"
)

// ResourceReservation is one durable ownership record. QuoteUSD and Base are
// expressed in the same units used by ResourceSnapshot. Informational records
// retain transaction lineage without subtracting a resource represented by a
// different record (for example, a pending exit whose source lot still owns
// the closing resource).
type ResourceReservation struct {
	ID            string                   `json:"id"`
	TransactionID string                   `json:"transaction_id,omitempty"`
	OwnerID       string                   `json:"owner_id,omitempty"`
	ClientOrderID string                   `json:"client_order_id,omitempty"`
	ProductID     string                   `json:"product_id,omitempty"`
	Kind          ResourceReservationKind  `json:"kind"`
	State         ResourceReservationState `json:"state"`
	Producer      EntryProducer            `json:"producer,omitempty"`
	Side          OrderSide                `json:"side"`
	QuoteUSD      float64                  `json:"quote_usd,omitempty"`
	Base          float64                  `json:"base,omitempty"`
	Informational bool                     `json:"informational,omitempty"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
}

type ResourceLedgerState struct {
	Reservations map[string]ResourceReservation `json:"reservations,omitempty"`
}

// ResourceManager is the sole runtime owner of resource reservations. Trader
// lifecycle maps remain authoritative for order/strategy semantics, while this
// ledger alone determines reserved and spare quote/base.
type ResourceManager struct {
	mu           sync.Mutex
	reservations map[string]ResourceReservation
}

func NewResourceManager(state ResourceLedgerState) *ResourceManager {
	m := &ResourceManager{reservations: make(map[string]ResourceReservation)}
	m.Restore(state)
	return m
}

func (m *ResourceManager) Restore(state ResourceLedgerState) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reservations = make(map[string]ResourceReservation, len(state.Reservations))
	for id, reservation := range state.Reservations {
		id = strings.TrimSpace(id)
		if id == "" {
			id = strings.TrimSpace(reservation.ID)
		}
		if id == "" {
			continue
		}
		reservation.ID = id
		m.reservations[id] = reservation
	}
}

func (m *ResourceManager) State() ResourceLedgerState {
	state := ResourceLedgerState{Reservations: make(map[string]ResourceReservation)}
	if m == nil {
		return state
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, reservation := range m.reservations {
		state.Reservations[id] = reservation
	}
	return state
}

func validateResourceReservation(reservation ResourceReservation) error {
	if strings.TrimSpace(reservation.ID) == "" {
		return errors.New("resource reservation missing ID")
	}
	if reservation.Side != SideBuy && reservation.Side != SideSell {
		return fmt.Errorf("resource reservation invalid side=%s", reservation.Side)
	}
	if reservation.QuoteUSD < 0 || reservation.Base < 0 {
		return errors.New("resource reservation cannot be negative")
	}
	if !reservation.Informational && reservation.QuoteUSD <= 0 && reservation.Base <= 0 {
		return errors.New("counted resource reservation must own quote or base")
	}
	return nil
}

// ReserveBatch atomically installs a complete transaction/batch or installs
// nothing. Duplicate IDs fail closed.
func (m *ResourceManager) ReserveBatch(reservations []ResourceReservation) error {
	if m == nil {
		return errors.New("nil ResourceManager")
	}
	now := time.Now().UTC()
	normalized := make([]ResourceReservation, 0, len(reservations))
	seen := make(map[string]struct{}, len(reservations))
	for _, reservation := range reservations {
		reservation.ID = strings.TrimSpace(reservation.ID)
		if err := validateResourceReservation(reservation); err != nil {
			return err
		}
		if _, exists := seen[reservation.ID]; exists {
			return fmt.Errorf("duplicate resource reservation ID=%s in batch", reservation.ID)
		}
		seen[reservation.ID] = struct{}{}
		if reservation.State == "" {
			reservation.State = ResourceReservationReserved
		}
		if reservation.CreatedAt.IsZero() {
			reservation.CreatedAt = now
		}
		reservation.UpdatedAt = now
		normalized = append(normalized, reservation)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, reservation := range normalized {
		if _, exists := m.reservations[reservation.ID]; exists {
			return fmt.Errorf("resource reservation already exists ID=%s", reservation.ID)
		}
	}
	for _, reservation := range normalized {
		m.reservations[reservation.ID] = reservation
	}
	return nil
}

func (m *ResourceManager) Upsert(reservation ResourceReservation) error {
	if m == nil {
		return errors.New("nil ResourceManager")
	}
	reservation.ID = strings.TrimSpace(reservation.ID)
	if err := validateResourceReservation(reservation); err != nil {
		return err
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior, exists := m.reservations[reservation.ID]; exists && reservation.CreatedAt.IsZero() {
		reservation.CreatedAt = prior.CreatedAt
	}
	if reservation.CreatedAt.IsZero() {
		reservation.CreatedAt = now
	}
	if reservation.State == "" {
		reservation.State = ResourceReservationReserved
	}
	reservation.UpdatedAt = now
	m.reservations[reservation.ID] = reservation
	return nil
}

func (m *ResourceManager) Release(ids ...string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.reservations, strings.TrimSpace(id))
	}
}

func (m *ResourceManager) Quarantine(id string) error {
	if m == nil {
		return errors.New("nil ResourceManager")
	}
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation, exists := m.reservations[id]
	if !exists {
		return fmt.Errorf("cannot quarantine missing reservation ID=%s", id)
	}
	reservation.Kind = ResourceReservationQuarantine
	reservation.State = ResourceReservationReconciling
	reservation.UpdatedAt = time.Now().UTC()
	m.reservations[id] = reservation
	return nil
}

func (m *ResourceManager) Quarantines() []ResourceReservation {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ResourceReservation, 0)
	for _, reservation := range m.reservations {
		if reservation.State == ResourceReservationReconciling ||
			reservation.Kind == ResourceReservationQuarantine {
			result = append(result, reservation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (m *ResourceManager) Totals() (quoteUSD, base float64) {
	if m == nil {
		return 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, reservation := range m.reservations {
		if reservation.Informational {
			continue
		}
		quoteUSD += reservation.QuoteUSD
		base += reservation.Base
	}
	return quoteUSD, base
}

// ReplaceDerived atomically refreshes state-derived records while preserving
// live submission and reconciliation quarantine ownership.
func (m *ResourceManager) ReplaceDerived(reservations []ResourceReservation) error {
	if m == nil {
		return errors.New("nil ResourceManager")
	}
	for _, reservation := range reservations {
		if err := validateResourceReservation(reservation); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, reservation := range m.reservations {
		switch reservation.Kind {
		case ResourceReservationLot, ResourceReservationPendingEntry,
			ResourceReservationPendingExit, ResourceReservationRefund,
			ResourceReservationCase3A:
			delete(m.reservations, id)
		}
	}
	now := time.Now().UTC()
	sort.Slice(reservations, func(i, j int) bool { return reservations[i].ID < reservations[j].ID })
	for _, reservation := range reservations {
		reservation.ID = strings.TrimSpace(reservation.ID)
		if reservation.State == "" {
			reservation.State = ResourceReservationPending
		}
		if reservation.CreatedAt.IsZero() {
			reservation.CreatedAt = now
		}
		reservation.UpdatedAt = now
		m.reservations[reservation.ID] = reservation
	}
	return nil
}
