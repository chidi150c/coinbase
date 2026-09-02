package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ResourceKind identifies the shared account resource consumed by a new entry.
type ResourceKind string

const (
	ResourceKindNone  ResourceKind = ""
	ResourceKindQuote ResourceKind = "quote"
	ResourceKindBase  ResourceKind = "base"
)

// ResourceSnapshot is the single immutable funding/capacity view used by every
// producer participating in one entry-allocation cycle.
//
// Existing pending reservations are already included exactly once in
// ReservedQuote/ReservedBase before SpareQuote/SpareBase are derived. The
// coordinator must never subtract those existing reservations again.
type ResourceSnapshot struct {
	UpdatedAt time.Time

	SymQuote string
	SymBase  string

	AvailQuote float64
	AvailBase  float64

	QuoteStep float64
	BaseStep  float64

	ReservedQuote float64
	ReservedBase  float64

	SpareQuote float64
	SpareBase  float64

	Price       float64
	MinNotional float64

	CurrentLots       int
	AvailableLotSlots int // -1 means unlimited.
}

// buildResourceSnapshotLocked performs the ONE balance/spare lookup for an
// ordinary entry cycle. The caller must hold t.mu because the reservation
// totals and lot count are derived from Trader-owned state before this call.
func (t *Trader) buildResourceSnapshotLocked(
	maxAge time.Duration,
	reservedShortQuoteWithFee float64,
	reservedLongBase float64,
	price float64,
	minNotional float64,
) (ResourceSnapshot, bool) {
	balance, ok := t.getBalanceSpare(
		maxAge,
		reservedShortQuoteWithFee,
		reservedLongBase,
	)

	snapshot := ResourceSnapshot{
		UpdatedAt:         balance.Snapshot.UpdatedAt,
		SymQuote:          balance.Snapshot.SymQuote,
		SymBase:           balance.Snapshot.SymBase,
		AvailQuote:        balance.AvailQuote,
		AvailBase:         balance.AvailBase,
		QuoteStep:         balance.QuoteStep,
		BaseStep:          balance.BaseStep,
		ReservedQuote:     reservedShortQuoteWithFee,
		ReservedBase:      reservedLongBase,
		SpareQuote:        balance.SpareQuote,
		SpareBase:         balance.SpareBase,
		Price:             price,
		MinNotional:       minNotional,
		AvailableLotSlots: -1,
	}

	if t != nil {
		snapshot.CurrentLots =
			len(t.book(SideBuy).Lots) +
				len(t.book(SideSell).Lots)

		if t.cfg.MaxConcurrentLots > 0 {
			snapshot.AvailableLotSlots =
				t.cfg.MaxConcurrentLots - snapshot.CurrentLots
			if snapshot.AvailableLotSlots < 0 {
				snapshot.AvailableLotSlots = 0
			}
		}
	}

	return snapshot, ok
}

type ProducerResourceRequest struct {
	Decision EntryDecision
	Intent   *PendingIntent
	Attempt  *ProducerAttempt

	Producer EntryProducer
	Side     OrderSide
	Priority ProducerPriority

	RequestedQuote float64
	RequestedBase  float64

	ResourceKind      ResourceKind
	RequestedResource float64
	MinimumResource   float64
	ResourceStep      float64

	ConsumesLotSlot bool

	ConfidenceMult float64
	ProfitGateUSD  float64
	EntryMethod    string
	Take           float64

	EquityStageChosen int
	EquityStageNext   int
	EquityStageValid  bool

	RefundRequestedUSD float64
	CoreQuote          float64
	CoreBase           float64
}

type AllocationStatus string

const (
	AllocationApproved AllocationStatus = "approved"
	AllocationPartial  AllocationStatus = "partial"
	AllocationRejected AllocationStatus = "rejected"
)

type AllocationReason string

const (
	AllocationReasonApproved            AllocationReason = "approved"
	AllocationReasonPartial             AllocationReason = "partial_funding"
	AllocationReasonBalanceUnavailable  AllocationReason = "balance_unavailable"
	AllocationReasonInsufficientFunding AllocationReason = "insufficient_funding"
	AllocationReasonBelowMinNotional    AllocationReason = "below_min_notional"
	AllocationReasonLotCapacity         AllocationReason = "lot_capacity"
	AllocationReasonInvalidRequest      AllocationReason = "invalid_request"
)

type ProducerResourceAllocation struct {
	Request ProducerResourceRequest

	Status AllocationStatus
	Reason AllocationReason

	AllocatedQuote float64
	AllocatedBase  float64

	AllocationFraction float64
	AllocationMethod   string

	PriorityGroupRequested float64
	PriorityGroupAvailable float64
	PriorityGroupMembers   int

	// FundingShortfallUSD preserves the historical true-funding-failure
	// refund semantics. It remains zero for successful allocations and
	// non-funding rejection reasons.
	FundingShortfallUSD float64
}

type AllocationPlan struct {
	CreatedAt time.Time
	Snapshot  ResourceSnapshot

	Allocations []ProducerResourceAllocation

	ReservedQuote float64
	ReservedBase  float64
	ReservedLots  int
}

// ProducerResourceCoordinator is the concrete V1 shared-resource arbiter.
// It is intentionally not an interface: there is one authoritative policy.
type ProducerResourceCoordinator struct{}

func snapDownResource(value, step float64) float64 {
	if value <= 0 {
		return 0
	}
	if step <= 0 {
		return value
	}
	return math.Floor(value/step) * step
}

func (c ProducerResourceCoordinator) Allocate(
	snapshot ResourceSnapshot,
	requests []ProducerResourceRequest,
	balanceAvailable bool,
) AllocationPlan {
	plan := AllocationPlan{
		CreatedAt:   time.Now().UTC(),
		Snapshot:    snapshot,
		Allocations: make([]ProducerResourceAllocation, 0, len(requests)),
	}

	if len(requests) == 0 {
		return plan
	}

	// Highest priority first. Stable producer/DecisionID ordering is used only
	// for deterministic diagnostics; equal-priority funding is proportional.
	sorted := append([]ProducerResourceRequest(nil), requests...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		if sorted[i].Producer != sorted[j].Producer {
			return sorted[i].Producer < sorted[j].Producer
		}
		left, right := "", ""
		if sorted[i].Intent != nil {
			left = sorted[i].Intent.DecisionID
		}
		if sorted[j].Intent != nil {
			right = sorted[j].Intent.DecisionID
		}
		return left < right
	})

	spareQuote := math.Max(0, snapshot.SpareQuote)
	spareBase := math.Max(0, snapshot.SpareBase)
	lotSlots := snapshot.AvailableLotSlots

	for start := 0; start < len(sorted); {
		priority := sorted[start].Priority
		end := start + 1
		for end < len(sorted) && sorted[end].Priority == priority {
			end++
		}
		group := sorted[start:end]

		for _, resourceKind := range []ResourceKind{ResourceKindQuote, ResourceKindBase, ResourceKindNone} {
			members := make([]ProducerResourceRequest, 0, len(group))
			groupRequested := 0.0
			for _, req := range group {
				if req.ResourceKind != resourceKind {
					continue
				}
				members = append(members, req)
				groupRequested += math.Max(0, req.RequestedResource)
			}
			if len(members) == 0 {
				continue
			}

			groupAvailable := groupRequested
			switch resourceKind {
			case ResourceKindQuote:
				groupAvailable = spareQuote
			case ResourceKindBase:
				groupAvailable = spareBase
			}

			ratio := 1.0
			if groupRequested > 0 && groupAvailable < groupRequested {
				ratio = groupAvailable / groupRequested
				if ratio < 0 {
					ratio = 0
				}
			}

			for _, req := range members {
				allocation := ProducerResourceAllocation{
					Request:                req,
					Status:                 AllocationRejected,
					Reason:                 AllocationReasonInvalidRequest,
					AllocationFraction:     0,
					AllocationMethod:       "priority",
					PriorityGroupRequested: groupRequested,
					PriorityGroupAvailable: groupAvailable,
					PriorityGroupMembers:   len(members),
				}

				if !balanceAvailable && resourceKind != ResourceKindNone {
					allocation.Reason = AllocationReasonBalanceUnavailable
					plan.Allocations = append(plan.Allocations, allocation)
					continue
				}

				if req.ConsumesLotSlot && lotSlots == 0 {
					allocation.Reason = AllocationReasonLotCapacity
					plan.Allocations = append(plan.Allocations, allocation)
					continue
				}

				if req.RequestedQuote <= 0 || req.RequestedBase <= 0 ||
					req.RequestedQuote < snapshot.MinNotional {
					allocation.Reason = AllocationReasonBelowMinNotional
					plan.Allocations = append(plan.Allocations, allocation)
					continue
				}

				allocatedResource := req.RequestedResource
				if resourceKind != ResourceKindNone {
					rawAllocatedResource := req.RequestedResource * ratio
					allocatedResource = snapDownResource(rawAllocatedResource, req.ResourceStep)
					if allocatedResource+1e-12 < req.MinimumResource {
						if groupAvailable <= 0 {
							allocation.Reason = AllocationReasonInsufficientFunding
						} else {
							allocation.Reason = AllocationReasonBelowMinNotional
						}

						// Preserve the historical refund definition: only the
						// producer's missing resource becomes a refund obligation.
						shortResource := req.RequestedResource - rawAllocatedResource
						if shortResource < 0 {
							shortResource = 0
						}
						switch resourceKind {
						case ResourceKindQuote:
							allocation.FundingShortfallUSD = shortResource
						case ResourceKindBase:
							allocation.FundingShortfallUSD = shortResource * snapshot.Price
						}

						plan.Allocations = append(plan.Allocations, allocation)
						continue
					}
				}

				switch resourceKind {
				case ResourceKindQuote:
					allocation.AllocatedQuote = allocatedResource
					allocation.AllocatedBase = allocatedResource / snapshot.Price
				case ResourceKindBase:
					allocation.AllocatedBase = allocatedResource
					allocation.AllocatedQuote = allocatedResource * snapshot.Price
				case ResourceKindNone:
					allocation.AllocatedQuote = req.RequestedQuote
					allocation.AllocatedBase = req.RequestedBase
				}

				if allocation.AllocatedQuote+1e-9 < snapshot.MinNotional || allocation.AllocatedBase <= 0 {
					allocation.AllocatedQuote = 0
					allocation.AllocatedBase = 0
					allocation.Reason = AllocationReasonBelowMinNotional
					plan.Allocations = append(plan.Allocations, allocation)
					continue
				}

				allocation.AllocationFraction = 1
				if req.RequestedResource > 0 && resourceKind != ResourceKindNone {
					allocation.AllocationFraction = allocatedResource / req.RequestedResource
				}

				if allocation.AllocationFraction+1e-9 < 1 {
					allocation.Status = AllocationPartial
					allocation.Reason = AllocationReasonPartial
					allocation.AllocationMethod = "proportional"
				} else {
					allocation.Status = AllocationApproved
					allocation.Reason = AllocationReasonApproved
					if len(members) > 1 && ratio < 1 {
						allocation.AllocationMethod = "proportional"
					}
				}

				if resourceKind == ResourceKindQuote {
					spareQuote -= allocation.AllocatedQuote
					if spareQuote < 0 {
						spareQuote = 0
					}
					plan.ReservedQuote += allocation.AllocatedQuote
				}
				if resourceKind == ResourceKindBase {
					spareBase -= allocation.AllocatedBase
					if spareBase < 0 {
						spareBase = 0
					}
					plan.ReservedBase += allocation.AllocatedBase
				}

				if req.ConsumesLotSlot && lotSlots > 0 {
					lotSlots--
					plan.ReservedLots++
				}

				plan.Allocations = append(plan.Allocations, allocation)
			}
		}

		start = end
	}

	return plan
}

func resourceRequestMinBase(snapshot ResourceSnapshot) float64 {
	if snapshot.Price <= 0 || snapshot.MinNotional <= 0 {
		return 0
	}
	base := snapshot.MinNotional / snapshot.Price
	if snapshot.BaseStep > 0 {
		base = math.Ceil(base/snapshot.BaseStep) * snapshot.BaseStep
	}
	return base
}

// buildProducerResourceRequestLocked translates one admitted producer decision
// into its desired resource footprint. It never grants resources and never
// consumes shared spare. Every call in a batch receives the SAME snapshot.
func (t *Trader) buildProducerResourceRequestLocked(
	d EntryDecision,
	intent *PendingIntent,
	attempt *ProducerAttempt,
	snapshot ResourceSnapshot,
	equity EquityResult,
	execHistory []Candle,
	price float64,
	minNotional float64,
) (ProducerResourceRequest, error) {
	side, ok := d.SignalToSide()
	if !ok {
		return ProducerResourceRequest{}, fmt.Errorf("invalid producer side: %v", d.Signal)
	}
	if price <= 0 {
		return ProducerResourceRequest{}, fmt.Errorf("invalid entry price %.8f", price)
	}
	if d.Confidence <= 0 {
		return ProducerResourceRequest{}, fmt.Errorf("decision confidence must be > 0: %.8f", d.Confidence)
	}

	priority := d.ProducerPriority
	if priority <= 0 {
		priority = producerPriorityFor(d.Producer)
	}
	if priority <= 0 {
		return ProducerResourceRequest{}, fmt.Errorf("missing resource priority for producer %s", d.Producer)
	}

	baseUSD := t.cfg.RiskPerTradeUSD
	if baseUSD <= 0 {
		baseUSD = minNotional
	}
	quote := baseUSD

	if t.cfg.VolRiskAdjust {
		f := volRiskFactor(execHistory)
		if f <= 0 {
			f = 1
		}
		quote *= f
	}

	book := t.book(side)
	isEquity := d.Producer == EntryProducerEquity

	if t.cfg.RampEnable && !isEquity {
		k := rampCount(book, price, minNotional)
		if rc := runnerCount(book); rc > 0 && k >= rc {
			k -= rc
		}
		switch strings.ToLower(strings.TrimSpace(t.cfg.RampMode)) {
		case "exp":
			start := t.cfg.RampStartPct
			growth := t.cfg.RampGrowth
			if start <= 0 {
				start = 100
			}
			if growth <= 0 {
				growth = 1
			}
			factor := start
			for i := 0; i < k; i++ {
				factor *= growth
			}
			if max := t.cfg.RampMaxPct; max > 0 && factor > max {
				factor = max
			}
			quote = baseUSD * factor / 100
		default:
			start := t.cfg.RampStartPct
			if start <= 0 {
				start = 100
			}
			factor := start + float64(k)*t.cfg.RampStepPct
			if max := t.cfg.RampMaxPct; max > 0 && factor > max {
				factor = max
			}
			if factor <= 0 {
				factor = 100
			}
			quote = baseUSD * factor / 100
		}
	}

	confMult := d.Confidence
	profitGateMultiplier := d.ProfitGateMultiplier
	if d.Producer == EntryProducerCase3AReplacement {
		if profitGateMultiplier <= 0 {
			profitGateMultiplier = 1
		}
	} else if profitGateMultiplier <= 0 {
		return ProducerResourceRequest{}, fmt.Errorf(
			"ordinary producer missing standardized ProfitGateMultiplier: producer=%s tier=%s continuation=%t",
			d.Producer, d.ProducerTier, d.IsContinuation,
		)
	}

	baseEntryProfitGateUSD := t.cfg.ProfitGateUSD * confMult
	if baseEntryProfitGateUSD < 0.30 {
		baseEntryProfitGateUSD = 0.30
	}
	entryProfitGateUSD := baseEntryProfitGateUSD*profitGateMultiplier + t.recoveryTargetAddUSD()

	equityStageChosen := -1
	equityStageNext := -1
	equityStageValid := false

	if isEquity {
		switch side {
		case SideSell:
			spareBase := equity.ProposedSellBase
			if spareBase <= 0 {
				spareBase = snapshot.SpareBase
			}
			stages := equityStagesSell()
			start := clampStage(t.equityStageSell, len(stages))
			lastCandidate := 0.0
			lastStage := start
			for s := start; s < len(stages); s++ {
				candidate := snapToStep(spareBase*stages[s]*confMult, snapshot.BaseStep)
				lastCandidate = candidate
				lastStage = s
				if candidate > 0 && candidate <= spareBase && candidate*price >= minNotional {
					quote = candidate * price
					equityStageChosen = s
					equityStageNext = clampStage(s+1, len(stages))
					equityStageValid = true
					break
				}
			}
			if equityStageChosen < 0 {
				// Preserve the best available staged request even when it is below
				// exchange minimum. The coordinator owns the authoritative
				// allocation_rejected/below_min_notional outcome.
				quote = lastCandidate * price
				equityStageChosen = lastStage
				equityStageNext = clampStage(lastStage+1, len(stages))
				// No exchange-valid Equity stage was selected. Keep the
				// fallback request for coordinator diagnostics, but do not
				// consume an Equity stage if it is rejected.
				equityStageValid = false
			}
		case SideBuy:
			spareQuote := equity.ProposedBuyQuote
			if spareQuote <= 0 {
				spareQuote = snapshot.SpareQuote
			}
			stages := equityStagesBuy()
			start := clampStage(t.equityStageBuy, len(stages))
			lastCandidate := 0.0
			lastStage := start
			for s := start; s < len(stages); s++ {
				candidate := snapToStep(spareQuote*stages[s]*confMult, snapshot.QuoteStep)
				lastCandidate = candidate
				lastStage = s
				if candidate > 0 && candidate <= spareQuote && candidate >= minNotional {
					quote = candidate
					equityStageChosen = s
					equityStageNext = clampStage(s+1, len(stages))
					equityStageValid = true
					break
				}
			}
			if equityStageChosen < 0 {
				quote = lastCandidate
				equityStageChosen = lastStage
				equityStageNext = clampStage(lastStage+1, len(stages))
				// No exchange-valid Equity stage was selected. Keep the
				// fallback request for coordinator diagnostics, but do not
				// consume an Equity stage if it is rejected.
				equityStageValid = false
			}
		}

		// Preserve the historical Equity-stage mutation point. The old inline
		// path advanced the side stage immediately after selecting a valid staged
		// size, before the later funding gate and before order submission.
		if equityStageValid && equityStageNext >= 0 {
			switch side {
			case SideBuy:
				t.equityStageBuy = equityStageNext
			case SideSell:
				t.equityStageSell = equityStageNext
			}
		}
	} else {
		quote *= confMult
	}

	if quote < minNotional {
		quote = minNotional
	}
	base := quote / price

	coreQuote := quote
	coreBase := base
	refundRequestedUSD := 0.0
	const refundMinConf = 0.60
	if confMult >= refundMinConf {
		if side == SideSell && t.refundBuyUSD > 0 {
			refundRequestedUSD = t.refundBuyUSD
			extraBase := refundRequestedUSD / price
			if snapshot.BaseStep > 0 {
				extraBase = snapDownResource(extraBase, snapshot.BaseStep)
			}
			base += extraBase
			quote = base * price
		} else if side == SideBuy && t.refundSellUSD > 0 {
			refundRequestedUSD = t.refundSellUSD
			extraQuote := refundRequestedUSD
			if snapshot.QuoteStep > 0 {
				extraQuote = snapDownResource(extraQuote, snapshot.QuoteStep)
			}
			quote += extraQuote
			base = quote / price
		}
	}

	take := 0.0
	if t.cfg.ScalpTPDecayEnable && !isEquity {
		k := rampCount(book, price, minNotional)
		if rc := runnerCount(book); rc > 0 && k >= rc {
			k = len(book.Lots) - rc
		}
		tpPct := t.cfg.TakeProfitPct
		switch strings.ToLower(strings.TrimSpace(t.cfg.ScalpTPDecMode)) {
		case "exp", "exponential":
			factor := t.cfg.ScalpTPDecayFactor
			if factor <= 0 {
				factor = 1
			}
			pow := 1.0
			for i := 0; i < k; i++ {
				pow *= factor
			}
			tpPct *= pow
		default:
			tpPct -= float64(k) * t.cfg.ScalpTPDecPct
		}
		if tpPct < t.cfg.ScalpTPMinPct {
			tpPct = t.cfg.ScalpTPMinPct
		}
		if side == SideBuy {
			take = price * (1 + tpPct/100)
		} else {
			take = price * (1 - tpPct/100)
		}
	}

	req := ProducerResourceRequest{
		Decision:           d,
		Intent:             intent,
		Attempt:            attempt,
		Producer:           d.Producer,
		Side:               side,
		Priority:           priority,
		RequestedQuote:     quote,
		RequestedBase:      base,
		ConsumesLotSlot:    d.Producer != EntryProducerEquity,
		ConfidenceMult:     confMult,
		ProfitGateUSD:      entryProfitGateUSD,
		EntryMethod:        string(d.Producer),
		Take:               take,
		EquityStageChosen:  equityStageChosen,
		EquityStageNext:    equityStageNext,
		EquityStageValid:   equityStageValid,
		RefundRequestedUSD: refundRequestedUSD,
		CoreQuote:          coreQuote,
		CoreBase:           coreBase,
	}
	if req.EntryMethod == "" {
		req.EntryMethod = "UNKNOWN"
	}

	if side == SideBuy {
		req.ResourceKind = ResourceKindQuote
		req.RequestedResource = quote
		req.MinimumResource = minNotional
		req.ResourceStep = snapshot.QuoteStep
	} else if t.cfg.RequireBaseForShort {
		req.ResourceKind = ResourceKindBase
		req.RequestedResource = base
		req.MinimumResource = resourceRequestMinBase(snapshot)
		req.ResourceStep = snapshot.BaseStep
	} else {
		req.ResourceKind = ResourceKindNone
		req.RequestedResource = base
		req.MinimumResource = resourceRequestMinBase(snapshot)
		req.ResourceStep = snapshot.BaseStep
	}

	return req, nil
}
