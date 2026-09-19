# Maker exit price handoff fix

## Problem

`lot.Take` is refreshed as the fee-adjusted profit/recovery activation target.
The exit scan also calculated a current-market maker price, but conditionally
overwrote `lot.Take` with it. When that comparison failed, `closeLot()` submitted
the old positive `lot.Take`, which could cross the book and be rejected as
`Order would immediately match and take`.

## Fix

- Keep `lot.Take` exclusively as the activation/preview target.
- Add `makerLimitPx` to `exitCandidate`.
- Carry the freshly calculated epsilon-adjusted price through exit fan-out.
- Make `closeLot()` use that carried price, with its existing live-price fallback.
- Clear `FixedTPWorking` when maker submission fails.
- Apply the same handoff to profit protection, L1 stop exits, ordinary profit
  exits, and AI-transition rollover exits.

## Verification

Run in the repository after replacing `step.go` and `trader.go`:

```bash
gofmt -w step.go trader.go
go test ./...
```

This environment does not contain the Go toolchain, so those commands could not
be executed here.

After deployment, confirm that `pending_exit.start_failed` no longer repeatedly
reports post-only crossing at a persisted activation target and that submitted
SELL limits are above the current price while submitted BUY limits are below it.
