# fastincc-v0.4.1-17 — Hy2 lifecycle, receive window, shared limits and UDP accounting

## Four independent fixes

This release contains four independently committed fixes after -16:

- `e9c4327`: own independent Hy2 HTTP/HTTPS masquerade listeners for the entire node lifecycle. Bind failures and QUIC-construction failures release partially acquired resources. Node replacement, deletion and core shutdown close listeners, HTTP connections and hijacked connections. An individual serving error no longer exits the whole process; initial startup still follows existing controller policy.
- `12a8575`: apply an explicitly configured `quic.initStreamReceiveWindow` to the initial stream receive window instead of the connection-window field. Unspecified defaults, maximum windows and congestion control remain unchanged.
- `5af193f`: enforce the existing static node/user rate limit in independent Hy2. A per-user bucket is shared across QUIC connections, TCP streams, UDP sessions and both traffic directions. Limited waits are cancellable and fair; unlimited traffic does not enter the pacing path.
- `befac2e`: count each downstream UDP logical payload exactly once when QUIC requires fragmentation. Fragment retries retain zero-byte authorization checks, while billing and rate quota belong only to the original logical message. This also handles a retry that has only one fragment, without trusting wire fragment metadata to decide quota ownership.

## Compatibility and behavior changes

Configuration, subscriptions, real-IP extraction and proxy trust, authentication, audit identity, device admission, panel APIs, accounting direction and durable batch formats remain compatible. Automatic reliable-mode negotiation is unchanged. No panel change, database migration or installer update is required. The sing-box Hy2 path, AnyTLS and Xray forwarding are unchanged.

**Existing nonzero static limits now take effect in independent Hy2.** The smaller positive node/user setting applies in decimal Mbps, with the existing one-second burst and 10 ms refill. The node setting is a per-user ceiling, not an aggregate cap across all users. Upload and download share that user's quota. This can intentionally reduce throughput previously allowed above the configured limit. Hy2 UpMbps/DownMbps negotiation is separate. Existing dynamic-limit scheduling is outside this fix.

Explicitly customized small initial stream windows can constrain high-RTT throughput; larger windows may increase buffer demand. This is a configuration correction, not automatic window tuning. Default window behavior is unchanged.

Masquerade content, certificate reload, redirects and Alt-Svc remain compatible. Closing or rebuilding a node intentionally disconnects its masquerade connections. Shutdown ownership introduces no proxy packet-copy hook.

Downstream fragmented payloads previously could be billed twice. Newly collected traffic now counts the original payload once. Accounting retains the existing ingress convention: bytes read from the target still count once after cancellation, failed or partially completed sends. This is not client-acknowledged delivery accounting. Historical persisted/reported batches are not rewritten, and queued retries retain their immutable payloads. Fragmentation sizes, MTU, QUIC congestion control and buffers are unchanged. Each retry still checks revocation, generation and paused state.

## Validation and performance

Local acceptance includes fixed-baseline negative controls and real local QUIC TCP/UDP, HTTP/TLS masquerade responses, proxying and redirects, repeated node replacement, partial bind rollback, construction failure, other-node traffic, concurrent close and hijacked-connection cleanup. Window tests cover defaults, field separation and actual transfer with 16 KiB/16 MiB initial stream windows.

Rate tests cover 1/2/8 Mbps, the node/user minimum, shared TCP/UDP and multiple-connection quotas, sniffed bytes, cancellation, revocation, drain/resume, unlimited isolation, real source IP/device rejection and snapshot retry/commit. Fragment accounting tests cover small and fragmented payloads, IPv6 destinations, shared limits, actual QUIC send errors, denial before/after a fragment and persistence between original accounting and fragment retries. Final fragment-specific and transport-failure race tests passed three rounds. The combined 15-package race suite, go vet, four local cross-builds and scoped secret scans passed before release preparation. Linux CI reruns the existing regression gates before building packages.

The first three repairs versus -16 measured final local TCP median throughput changes of -0.23%/-0.19% for Hy2 and +1.61%/+0.41% for AnyTLS at one/eight streams. Extended 30-second UDP comparisons measured +0.70%/-0.41%; an initial short single-stream -9.98% sample was retained and did not recur in the extended median. At 10 Mbps under four downloads, HTTPS 204 P95 was 105.215 to 103.643 ms.

For the subsequent fragment-accounting repair versus its immediate parent, 36 valid UDP comparisons (30 seconds, one/eight connections, small/fragmented/mixed traffic, three rounds per version) measured median changes from -0.63% to +1.40%. TCP controls measured Hy2 -0.96%/+0.33% and AnyTLS +1.00%/+0.10%; HTTPS 204 P95 was 104.895 to 105.045 ms. Candidate accounting reconciled every valid case and limited reservations consumed each logical payload once. CPU/RSS were recorded; UDP peak-RSS median differences were approximately -0.56 to +0.36 MiB. CPU/RSS include the local clients and servers, not only a production node.

These measurements show no material throughput decline in the tested local workloads, not a universal zero-overhead guarantee. TCP controls were 10-second samples; HTTPS was one 10-second run per version. Performance throughput uses application payload, never the baseline's inflated billing counter. A baseline eight-connection mixed-UDP run hit its receive guard with one 1000-byte message counted but not echoed. The same immutable binary passed one retry; the failed log was retained and its cause was not localized. It is not counted as an initially passing reconciliation.

## Known boundaries and rollback

The existing UDP reassembler can reject a consecutive fragmented datagram that reuses the same PacketID and fragment count. Characterization reproduces this behavior in both the prior commit and this release. This release does not repair that separate ambiguity. Large-payload performance tests alternate 1800/3000-byte payloads (different fragment counts); mixed tests use 1000/1800/1000/3000 bytes. These do not prove loss-free continuous fixed-size fragmented traffic. UDP overload beyond a configured rate can also exhaust existing bounded receive queues.

Windows reliable-queue locking remains unsupported: explicit on or auto negotiation to reliable mode may prevent startup. Legacy reporting still lacks batch-level idempotency. No production, remote VPS, actual panel database/Redis, complete subscription client, Windows runtime, WAN/high-RTT/loss or hours-long validation is claimed by these local results. The 185-second opt-in device-lease test was not rerun for this repair series.

There is no new persistent schema or queue format. The four source fixes remain individually committed; revert in reverse dependency order when reverting interacting quota/accounting code. Reverting the corresponding change restores the listener defect, window misassignment, unenforced limits or fragment overbilling. Do not delete the spool or rewrite historical queued batches. Existing reliable-queue downgrade constraints still apply.

Packages: Linux amd64, Linux arm64, Linux armv7 and Windows amd64. See HY2-MASQUERADE-LIFECYCLE.md, HY2-INITIAL-STREAM-WINDOW.md, HY2-USER-RATE-LIMIT.md and HY2-UDP-FRAGMENT-ACCOUNTING.md. Publishing this release does not deploy nodes or modify origin servers.
