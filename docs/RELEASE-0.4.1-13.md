# fastincc-v0.4.1-13 — checkpoint isolation and batched revocation

## Fixes

Two independently committed fixes follow the -12 baseline:

- `8cb8cbd`: move durable traffic persistence outside shared core identity locks. Snapshot attribution and counter references are fixed under a short lock; disk work runs outside it; only successfully saved bytes are subtracted. Per-node gates serialize sampling, reset, reaping and destructive lifecycle operations without adding synchronization to packet I/O or ordinary user revocation. Slow storage no longer holds the sing admission lock or the independent Hysteria2 traffic callback lock.
- `7212cee`: batch user revocations before a panel-driven node rebuild. The controller filters applied users once and avoids rebuilding AnyTLS/Shadowsocks authentication tables for every removed user. If a deletion returns an error, treat the entire requested batch as uncertain, exclude it from rollback authorization, drain the affected node and retry cleanup before restoration. Completed revocations cannot be reactivated through an old paused snapshot.

## Compatibility and behavior

Existing configuration, subscriptions, real-IP sources, audit fields, device limits, panel APIs, traffic directions, durable batch IDs and queue formats remain compatible. TrafficBatchMode still defaults to auto. No database migration, panel change or installer update is required. Checkpoint frequency and fsync guarantees are unchanged.

A batch-deletion failure can temporarily interrupt the affected node until cleanup and restoration succeed. Other nodes are not deliberately paused. Configuration-driven rebuilds can still close existing streams; clients may need to reconnect. This release does not promise seamless listener replacement or remove disk/CPU resource contention.

## Validation

Local targeted race tests passed five rounds for each controller/concurrency group; real Shadowsocks partial-deletion recovery passed three race rounds with exact per-user bidirectional tail accounting. The combined common, limiter, panel API, configuration, node, core, dispatcher and command race suite passed, as did go vet and the four target cross-builds. Real local protocol regressions cover AnyTLS, Shadowsocks/SS2022, independent Hy2 QUIC and Xray VMess/VLESS.

Matched loopback AnyTLS profiles retain real TLS, PROXY v2, structured audit, production hooks and durable storage. Across repeated normal-storage runs, no stable throughput or latency regression was observed. In the extended 8-worker comparison, median aggregate echo throughput was 2799.38 versus 2802.39 MiB/s, and first-exchange P99 was 0.237 versus 0.239ms, with differences changing sign between rounds. Deliberately adding 300ms to each persistence callback changed P99 from 309.732 to 0.235ms; an additional 1000ms-delay run changed P99 from 1010.311 to 0.236ms. These are local fault-injection/echo results, not external network speed claims.

For actual AnyTLS table reconstruction with 10,000 users and 9,000 removals, three-run median deletion calls fell from 9,000 to one, and elapsed time from 7779.146 to 8.332ms. Cumulative allocations fell from approximately 26,393.68 to 10.84 MiB; this is allocation volume, not simultaneous resident memory.

A 30-second candidate run completed 173,882 logical streams and reconciled 45,587,687,232 bytes in each direction. These short local observations are not long-term leak certification. The release workflow additionally gates builds on the new checkpoint/batch race tests and actual AnyTLS slow-storage reconciliation on Linux.

## Platforms and remaining boundaries

Release packages: Linux amd64, Linux arm64, Linux armv7 and Windows amd64. Cross-build success does not certify runtime behavior on every target. Windows reliable-queue locking remains unsupported; auto negotiation of reliable mode can still prevent Windows startup. Legacy reporting retains its existing lack of batch-level idempotency. Other previously documented certificate, installer and rate-limiter limitations are outside these two fixes.

This release does not deploy nodes or modify the panel, test VPS or origin server. No new remote-host or production validation is claimed for these two fixes. See CHECKPOINT-LOCK-ISOLATION.md and BATCH-REBUILD-REVOCATION.md for implementation and failure-handling details.
