# fastincc-v0.4.1-14 — connection admission, user updates and smoother rate limits

## Fixes

Three independently committed fixes follow the -13 baseline:

- `59e4d17`: isolate independent Hysteria2 device admission by authenticated QUIC connection. Check admission before authentication succeeds; a rejected connection cannot be re-enabled by another connection using the same account. Retain authorization-generation attribution and in-flight TCP tail accounting until callbacks finish, then reclaim connection state.
- `8c1813a`: collect changed AnyTLS identities and scan the session table once per user update instead of once per changed user. Preserve immutable authentication snapshots, password/re-add revocation, audit-only updates and failed-publication atomicity. Close affected transports outside the users lock, once per update.
- `e4818e8`: replace one-second shared token refill with lazy 10 ms refill while preserving configured Mbps, one-second burst capacity and per-user sharing across TCP/UDP and both directions. TCP rate waits now respond to close, half-close and deadlines without losing transferred-byte counts. Xray buffered waits respond to close/interrupt and release unsent buffers; canceled buffers waiting for rate budget do not increment traffic statistics.

## Compatibility

Existing configuration, subscriptions, source-IP normalization and proxy trust, audit identities, device-limit policies, panel APIs, traffic directions and durable queue formats remain compatible. TrafficBatchMode still defaults to auto. No database migration, panel change or installer update is required. Independent Hysteria2's native bandwidth/congestion controls are unchanged. Unlimited connections keep the existing path without an added rate wrapper.

Over-limit independent Hy2 sources are now rejected at authentication. Password changes and re-added AnyTLS identities continue to invalidate old transports. Limited TCP waits can now return promptly on close/deadline while preserving positive byte counts and underlying I/O errors. Xray buffers canceled before forwarding are no longer pre-counted; other existing downstream error/partial-delivery accounting conventions remain unchanged.

## Validation and measured costs

Local isolated validation includes repeated race tests, actual two-source Hy2 QUIC TCP/UDP, concurrent AnyTLS additions during active traffic, real TLS/AnyTLS/PROXY-v2/HTTPS204 traffic, device admission, audit identity and exact bidirectional accounting. The combined 14-package race suite, related go vet checks and four target cross-builds passed. Negative controls on unchanged -13 reproduce the Hy2 admission and coarse-refill/canceled-wait problems.

For 5,000 new AnyTLS users and 10,000 synthetic existing sessions, three alternating comparisons reduced update elapsed time from 273.6–276.1 ms to 1.40–1.65 ms, with approximately 224 KiB more temporary allocations. This is a session-table test, not 10,000 simultaneous live connections.

Across three alternating 5-second actual AnyTLS/HTTPS comparisons, median P95 under download load changed from about 975 to 245 ms at 1 Mbps/one download and to 104 ms at 10 Mbps/four downloads. More frequent wakeups increased CPU by approximately 0.6 and 2.3 single-core percentage points in the combined local client/proxy/target test process. Peak RSS medians differed by less than 1 MiB. The preserved initial one-second burst affects short-run throughput; sustained shared rate is tested separately after exhausting it.

Unlimited AnyTLS profiles showed no stable throughput reduction. Hy2 5-second throughput medians were approximately 2.3% lower at one stream and 0.7% lower at eight streams; paired differences changed sign across runs. These short loopback observations neither establish a stable regression nor guarantee zero overhead. Connection admission adds a per-connection map and a lookup within the existing traffic lock. No WAN-speed or production-capacity claim is made.

The release workflow gates the four builds on existing regressions and the new Hy2 admission, AnyTLS batch-update and rate-cancellation tests on Linux. Cross-build success does not certify runtime behavior on every target.

## Remaining boundaries

Shared rate limiting does not provide per-stream fairness or idle latency under saturation. Canceled pre-send Xray/UDP reservations are not refunded because the bucket API has no safe reservation-cancellation operation; their debt expires through normal refill. Existing TCP buffering and post-I/O charging semantics remain. Short tests do not certify long-term leak freedom.

Windows reliable-queue locking remains unsupported; auto negotiation of reliable mode can still prevent Windows startup. Legacy reporting retains its lack of batch-level idempotency. Other previously recorded certificate, installer and lifecycle limitations are outside these three fixes.

Packages: Linux amd64, Linux arm64, Linux armv7 and Windows amd64. This release does not deploy nodes or change the panel, test VPS or origin server. No new remote-host or production validation is claimed. See HY2-CONNECTION-ADMISSION.md, ANYTLS-BATCH-USER-UPDATES.md and RATE-WAIT-SMOOTHING.md for details.
