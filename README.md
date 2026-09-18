# Ordinary producer insufficient-funding deduplication

Changed files:

- `trader.go`
- `producer_parallel_entry.go`

Scope:

- Suppresses repeated ordinary producer decisions before a new `DecisionID` is
  created when the frozen resource snapshot remains below the exchange-minimum
  executable funding for that side.
- Clears suppression when the producer-side opportunity disappears or minimum
  executable funding becomes available.
- Explicitly excludes Case3A/Case3B replacement producers and obligations.
- Does not change producer signal evaluation, allocation priority, partial
  allocation, submission, pending orders, positions, refund obligations, or
  Case3 recovery behavior.

Validation after copying into the repository:

```bash
gofmt -w trader.go producer_parallel_entry.go
go test ./...
go build ./...
```

Runtime verification:

```bash
docker logs --since 10m monitoring-bot_binance-1 2>&1 |
grep -E 'producer.funding_suppressed|allocation_rejected' |
tail -100
```

Expected behavior: one initial `allocation_rejected` lifecycle is retained for
an ordinary producer opportunity. Consecutive evaluations below minimum funding
emit only `producer.funding_suppressed`; they do not create new DecisionIDs or
green trade-attempt markers.
