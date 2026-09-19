# AITransitionTrader NORMAL-regime protection

## Implemented behavior

- Reuses `AITransitionRolloverPending`; no new persisted field is introduced.
- Captures a fresh opposite raw-AI transition before `previousAIRaw` advances.
- In `NORMAL`, requires raw AI to continue supporting the rollover direction.
- In `NORMAL`, requires the side-aware `activationPrice()` target calculated
  from `ProfitGateUSD * LowTierProducerMultiplier`.
- The target accounts for the source cost basis, confirmed entry fee, estimated
  exit fee, and required low-tier NET profit.
- Cancels an unsupported rollover only when no `FixedTPOrderID` owns the lot.
- Preserves the existing accepted-order, partial-fill, reconciliation, retry,
  and fresh maker-limit behavior.
- Leaves `UP` and `DOWN` rollover behavior unchanged.

## NORMAL authorization

For a BUY-side lot:

```go
aiResult.Raw == Sell && price >= requiredPrice
```

For a SELL-side lot:

```go
aiResult.Raw == Buy && price <= requiredPrice
```

## Verification

Run in the complete repository after replacement:

```bash
gofmt -w step.go
go test ./...
```

The supplied environment does not include the Go toolchain, so these commands
could not be executed here.
