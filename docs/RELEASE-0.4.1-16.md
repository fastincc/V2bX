# fastincc-v0.4.1-16 — Hy2 sniff accounting, bounded cleanup and reliable delivery status

## Fixes

Three independently committed fixes follow the -15 baseline:

- `f8c0f45`: account TCP bytes consumed by independent Hysteria2 sniffing exactly once before forwarding them to the target. Previously those bytes updated stream statistics but bypassed the billing callback. A denied callback now also prevents forwarding that prefix; short or failed writes follow normal cleanup. This fixes an upstream-existing omission.
- `efd87cc`: reclaim retired Hy2 user generations even when a node has never created a traffic counter. A retired-only FIFO inspects at most 256 entries per collection and rotates entries with pending byte tails, avoiding scans of all active users. This fixes a historical integration issue introduced with accounting generations in -11.
- `e5bed4d`: distinguish confirmed reliable delivery from idle, backoff, pause, cancellation and failure. Only a matching panel acknowledgement followed by successful local acknowledgement permits the controller's completion log. A remote confirmation followed by a local storage failure is reported explicitly; skipped-state logs are emitted on transitions. This fixes a historical reporting issue in -12.

## Compatibility and behavior

Configuration, subscriptions, authentication, real-IP extraction and proxy trust, device admission, audit identity, panel APIs, traffic directions and durable queue formats remain compatible. Automatic reliable-mode negotiation is unchanged. No panel modification, database migration or installer update is required.

The intentional billing difference is that previously omitted sniffed upload bytes are now included. Metering follows the existing ingress-byte convention, including short/failed target writes after admission and successful dialing. Failed sniffing or failed dialing retains the previous failed-request behavior. Disabled sniffing and subsequent long-stream copy iterations have no added work. Sniff timeouts and destination rewriting are unchanged, including the existing timeout cost for server-first protocols that match sniffing.

Cleanup preserves active identities, pending traffic tails, failed-persistence data and independent generations after revocation/re-addition. It adds no packet-path registration or background worker. Collection cadence determines wall-clock cleanup time; bounded test collection counts are not elapsed seconds.

Reliable batch IDs, request bodies, replay, backoff, catch-up budgets, fsync and current-window online-device accounting are unchanged. Monitoring that treated skipped attempts as successful reports should instead distinguish pending states from confirmed completion.

## Validation and performance

Local isolated acceptance includes unchanged -15 negative controls, repeated race tests, real QUIC/TCP, HTTP and TLS/HTTPS 204, exact directional accounting, reliable snapshot reconciliation, short/failed writes and revocation before prefix forwarding. Zero-traffic cleanup passed 10,000 and 100,000 user-removal cycles in both reporting modes, with at most 256 inspections per collection. Pending tails were preserved and reconciled. Eight successive 10,000-cycle cleanup runs on one node showed stable post-GC heap in the bounded fixture.

Delivery tests cover zero HTTP calls during backoff/pause, no false completion logs, cancellation, incorrect acknowledgements, local acknowledgement failure, lost-confirmation replay, pacing, shared budgets and device-window separation. Six 128-batch HTTP/local-store profiles reconciled every unique batch. Combined 15-package race tests, go vet, four cross-builds and the new-commit secret scan passed before release preparation. Linux CI reruns the release gates before building packages.

Compared with -15, local 15-second Hy2 profiles measured median throughput changes of +4.17%/+0.57% at one/eight streams. Initial five-second AnyTLS profiles measured -10.21%/-1.21%; those results were retained. Rebuilt-baseline alternating 30-second profiles measured -0.44%/+1.51%, without reproducing the large short-profile decline. At 10 Mbps with four downloads, HTTPS 204 P95 was 104.679 to 104.698 ms. All 36 transport profiles reconciled traffic; AnyTLS real-IP/audit assertions passed. CPU and RSS include local clients and servers, not a production VPS alone.

Sniffing adds one accounting callback per matched TCP stream, not per subsequent packet. Fixed 1,000-stream comparisons showed no slowdown, but the callback has measurable allocation cost. An adaptive high-frequency new-stream benchmark hit local loopback dial errors (address unavailable) on both baseline and candidate; that pressure case is not counted as passed. An initial HTTP-cancellation fixture waited on an unread request body; after correcting the fixture, cancellation and repeated race regressions passed without changing product cancellation semantics.

These results do not establish a universal speedup or guarantee zero overhead. No production deployment, actual panel database/Redis, complete subscription/client, WAN/high-RTT/loss, Windows runtime or hours-long stability validation is claimed.

## Remaining boundaries and rollback

Windows reliable-queue locking remains unsupported: explicit on or auto negotiation of reliable mode can prevent startup. Legacy reporting still lacks batch-level idempotency. Other previously recorded certificate-permission, installer rollback and configuration issues are outside this release.

There is no new persistent format or schema migration. Source fixes can be reverted independently; a revert restores the corresponding omission, retained metadata or misleading logs. Existing reliable-queue downgrade constraints still apply when rolling back deployed binaries.

Packages: Linux amd64, Linux arm64, Linux armv7, Windows amd64. See HY2-SNIFF-ACCOUNTING.md, HY2-RETIRED-CLEANUP.md and RELIABLE-DELIVERY-STATUS.md for details. Publishing this release does not deploy nodes or modify origin servers.
