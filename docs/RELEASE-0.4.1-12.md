# fastincc-v0.4.1-12 — authentication, recovery and reliable queue catch-up

## Fixes

Four independently committed fixes follow the -11 baseline:

- `520073b`: publish immutable Shadowsocks authentication tables and stable user identities. Concurrent updates cannot mix user-table generations; final admission rejects revoked generations. The pinned sing-shadowsocks dependency is included with a focused patch and its license, without a dependency version upgrade.
- `2b5c474`: recover panel-driven node rebuild failures without panicking. Retain applied configuration until successful replacement, restore the last working node when possible, and retry pending recovery. Successful revocations survive rollback; reliable journals and accounting ownership are retained.
- `1b6c75d`: commit user ETags only after successful application. Partial updates are compensated or retried; failed application cannot be hidden by a subsequent 304 response. Healthy unchanged responses retain existing caching.
- `b5a978d`: drain reliable traffic backlogs through a cancellable, serial sender per node. New sealed batches wake delivery; ordinary reporting takes priority. Extra catch-up attempts are spaced by at least 500ms and share an 8/s budget per panel origin within one process. This budget excludes ordinary periodic reports and is not a fleet-wide limit. Failures back off, valid Retry-After is respected, and ambiguous acknowledgements retain the original batch for replay.

An additional independently committed release-blocker fix (`cc4ad0e`) closes a pre-existing UDP rejection gap. The original -11 also reproduced it: closing a UDP NAT connection could still allow buffered reads and delegated writes. Rejected device/rule/identity paths now return a terminal connection that rejects I/O and exposes no optimized read or unwrapping path. The first -12 CI attempt caught this issue and was not bypassed; the unpublished tag is updated only after the additional fix and acceptance.

## Compatibility and behavior

Existing configuration, subscriptions, credentials, panel APIs, real-IP sources, device admission limits, upload/download directions and durable batch format remain compatible. TrafficBatchMode still defaults to auto. No new panel endpoint, database migration or fractional-remainder accounting is required. Legacy reporting remains without batch-level idempotency guarantees.

Reliable delivery no longer holds the controller operation lock during HTTP. Catch-up does not generate extra device reports. Online-device minimum-traffic filtering now uses newly persisted traffic in the current reporting window instead of historical backlog traffic: online display for low-volume devices during backlog recovery can change, while configured thresholds and admission rules stay the same. Batch IDs, payloads and billing directions are unchanged; batches are not merged.

Failed rebuilds may interrupt existing connections, which clients must reconnect. Restoring service after a failure does not restore an already-closed stream. A local acknowledgement-storage error pauses the sender until restart; do not delete pending batches to bypass it.

## Validation

The four originally planned fixes passed local race/vet checks and isolated regression on two Linux amd64 test hosts. Tests include concurrent Shadowsocks/SS2022 user changes, real bind-failure recovery, ETag retries, queue cancellation/reopen, shared pacing, normal-report priority and device reporting thresholds. Release CI includes these regression tests and the additional terminal UDP rejection tests.

The additional UDP rejection fix passed 30 rounds of real Shadowsocks UDP device-limit, rate-limit and exact-accounting regression on each Linux host, plus 3,000 pre-buffered rejection cases per host. The unmodified -11 reproduced the defect on both hosts. Normal admission adds no new wrapper or lock; the terminal connection is created only after rejection.

With identical 16-batch workloads, a 2-second regular reporting interval and 100ms server response delay, median drain time fell from 33.655 to 8.417 seconds on one host and 33.843 to 8.456 seconds on the other (approximately 75% less elapsed time). Catch-up increases requests per unit time; these measurements are not a production capacity guarantee.

An isolated current-panel HTTP/controller/database path with a real AnyTLS proxy passed outage, lost-acknowledgement, restart, HTTP 429, revocation, configuration-failure, trusted PROXY v2 address and two-IP device-limit tests. It also completed 100 sequential short connections, 32 short connections at concurrency 8 and a 180-second stream. 136 unique batches across 139 accepted requests reconciled exactly: 17,551,360 bytes in each direction. Within 1.055 seconds after the stream-completion record, the first complete observation found both sealed batches and active journal empty; this is an observation bound, not exact last-byte acknowledgement latency.

The panel fixture used real database billing with synchronous jobs, isolated online state and a private CLI HTTP entry point, not formal FPM/shared-worker load testing. Test account fields were restored, accepted receipts retained, and temporary processes stopped. No production verification or full public-client matrix is claimed. Short CPU/RSS observations do not establish long-term leak freedom.

Release targets remain Linux amd64, Linux arm64, Linux armv7 and Windows amd64. Non-amd64 targets are build-validated, not runtime-certified.

## Known remaining limitations

- Reliable queue locking remains unsupported on Windows. Auto negotiation of reliable mode can prevent Windows startup; this release does not fix that limitation.
- Core traffic snapshots can still hold shared locks across persistence; slow storage can affect new-connection latency. Catch-up moves delivery HTTP out of the controller lock but does not remove these storage locks.
- Storage failures, queue capacity and termination after the last checkpoint retain existing durability boundaries. Recovery speed is bounded by panel latency, pacing and backoff.
- Previously observed intermittent macOS sing-tun monitor races were not reproduced in the latest complete run and were not fixed by these changes.
- Certificate-file permissions and other independently documented installer/rollback limitations are outside these four fixes.

Publication does not deploy nodes, change a panel or touch the origin host. Earlier release tags remain unchanged.
