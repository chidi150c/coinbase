# AITransitionTrader implementation

This drop-in bundle implements the agreed one-time `$70` AI-transition seed and
the recurring fill-driven rollover lifecycle.

## Runtime contract

- Seed on raw-AI `SELL/FLAT -> BUY` or `BUY/FLAT -> SELL`.
- Highest allocation priority; the configured seed is never deliberately
  reduced by the coordinator (`AI_TRANSITION_SEED_USD`, default `70`).
- `AITransitionInitialized` becomes true only after a confirmed fill commits.
- Tagged lots bypass ordinary profit, stop-loss and Case3 exits.
- The existing exit scan retains the tagged candidate; AI fan-in authorizes an
  opposite transition without a second book scan.
- Only one rollover is active. Pending/partial work is resumed before a new
  transition can act, with a 30-second retry eligibility time.
- Only exchange-confirmed quantity moves. The same confirmed fill creates the
  exit record and the opposite-side tagged lot; partial fills leave the source
  remainder pending and consolidate confirmed quantity on the destination.
- Rollover exits update Daily PnL and signed recovery debt through the existing
  authoritative exit accounting path.
- The dedicated capital is persisted on the tagged lot and is used to size the
  SELL-to-BUY rollover so gains and losses compound into the next BTC quantity.
- Seed allocation shortages do not create refund obligations.

## Verification after copying into the repository

Run:

```sh
gofmt -w config.go env.go entry_producers.go producer_admission.go \
  producer_resource_coordinator.go producer_parallel_entry.go strategy.go \
  step.go trader.go
go test ./...
```

The packaging environment did not contain a Go toolchain, so these two commands
must be run in the repository before deployment.
