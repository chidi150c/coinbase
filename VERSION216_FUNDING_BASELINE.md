# Version216 funding reporting baseline

Production behavior in version216 is authoritative for v2 development.

The balance refresher remains independent of ticks. No new network requests
are made. Funding reporting uses the balance cache and reservation ledger.
Both saved spare values are derived together, including freshness metadata.
Runtime spare bookkeeping and refund obligations retain their existing owners.

Successful balance refreshes enqueue one coalesced reporting save. One worker
runs at one-second cadence and defers if the trading lock is occupied. It
encodes under the read lock to detach mutable state, then writes outside that
lock. Sequence checks prevent an older snapshot replacing newer saved state.
Existing synchronous lifecycle saves retain their pre-action boundaries.

This does not promise zero lock contention: memory encoding briefly holds a
read lock, and the existing disk-write mutex serializes writers. No extra
network or disk operation is inserted in step(). Measure tick latency before
broader persistence changes. UI freshness must be checked from the timestamp
at display time; a stored fresh=true describes the moment of capture only.

State compatibility: existing SpareBuyUSD/SpareSellUSD JSON keys expose the
funding report. funding_snapshot adds cached balances, reservations, mark
price, timestamp, age and freshness. Stale/unknown funding exports zero spares
and fresh=false. No funding report is accepted as a restored balance cache.

v2: use ResourceSnapshot.SynchronizedFunding for both allocation and reporting;
never maintain independently incremented reporting spare counters. The helper
and its tests are supplied. A v2 live adapter is not yet wired in this checkout;
that integration must use this contract when the adapter is implemented.

Validation required before production deployment:

    gofmt -w funding_reporting.go funding_reporting_test.go trader.go step.go live.go producer_resource_coordinator.go
    go test ./...
    go test -race ./...
    go build ./...

The package was prepared without an available Go toolchain in the assistant
workspace. No build/test pass is claimed. Version remains 216.
