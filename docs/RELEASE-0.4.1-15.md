# fastincc-v0.4.1-15 — Hy2 tail accounting, active devices and SS2022 validation

## Fixes

Three independently committed fixes follow the -14 baseline:

- `4586aca`: fix a -14 independent Hysteria2 regression that could retire a connection before delayed TCP/UDP traffic callbacks finished. Retain the original accounting handle until transport callbacks drain. The pinned Hysteria core v2.6.4 is bundled with a minimal lifecycle patch; the version and congestion controls are unchanged. Revoked identities and denied connections remain denied.
- `8d1a1dd`: pin an admitted device source while a Hy2 TCP stream or UDP session is active. Keep long-lived sources visible across online reports, share the device slot across same-IP flows, and release references on close, failed dial and UDP timeout. An authenticated but otherwise idle QUIC connection is not permanently pinned. This fixes an existing integration gap, not a new -14 regression.
- `95b0dc0`: reject short SS2022 panel credentials before modifying inbound authentication or accounting maps. Validate the entire incoming addition; return a redacted error instead of slice-bounds panic. This fixes an upstream-existing issue.

## Compatibility and behavior

Configuration, subscription key derivation, source-IP extraction and proxy trust, audit identity, panel APIs, traffic directions and durable queue formats remain compatible. TrafficBatchMode still defaults to auto. No panel change, database migration or installer update is required.

Hy2 preserves the existing ingress-read accounting convention, rather than introducing target-delivered-byte billing. A device that stays active now keeps its slot; an additional source previously admitted because the active device disappeared from online samples may now be rejected. Cross-node device admission remains eventually consistent. Pinning is performed on stream/session transitions, not per packet.

SS2022 keeps the existing first-16/32-byte then Base64 key derivation for AES-128/256. It does not pad, hash or substitute malformed credentials. A mixed invalid addition is rejected as a whole and may defer valid new users until the panel data is corrected. Initial startup can still return a configuration error with invalid data. Failed additions do not restore completed user revocations. Other algorithms remain unchanged.

## Validation and performance

Local isolated acceptance includes repeated race tests, unchanged -14 negative controls, real QUIC TCP/UDP delayed callbacks, active-device and same-IP isolation, failed-dial/timeout cleanup, real SS/SS2022 authentication during user updates, and exact bidirectional accounting. The combined 15-package race suite, go vet and the four cross-builds passed. A real 185-second Hy2 long stream retained its device slot beyond the idle TTL and reconciled 1,245,184 bytes in each direction. Two thousand QUIC connection open/close cycles reclaimed handles and active devices, with stable goroutine count and post-warmup heap near 0.9 MiB in the test fixture.

Compared with -14, the initial Hy2 single-stream short-profile median was 4.50% lower; longer alternating 30-second profiles measured 0.89% lower. Eight-stream short profiles measured 0.14% higher. The device-lease-only incremental comparison against the tail fix measured -0.32%/+0.47% at one/eight streams. These bounded local differences do not establish a general slowdown or guarantee zero overhead on every network and CPU.

SS2022 AES-128/256 throughput medians changed -0.16%/+0.80%; validation of 10,000 users took about 5.4 microseconds with no allocations and adds no transfer-path operation. AnyTLS one/eight-stream medians changed -0.16%/-0.14%. At 10 Mbps with four downloads, local HTTPS 204 P95 was 103.7→104.2 ms; real IP, device rejection, audit and traffic reconciliation passed. CPU/RSS measurements include local clients and servers and are not VPS-only capacity numbers.

The dependency non-stress race suite passed. An initial combined upstream stress run exceeded its 180-second total timeout; the final 1,000×1 MiB parallel TCP case passed separately in about 39 seconds, and all UDP stress subcases passed separately. The initial timeout is not counted as a pass.

Linux CI gates the four builds on protocol, reliable-reporting, device, authentication, tail-lifecycle and user-sync regressions. Build success is not target-platform runtime acceptance. This release does not deploy nodes or modify any panel, test VPS or origin server.

## Remaining boundaries

Windows reliable-queue locking remains unsupported: explicit on or auto negotiation of reliable mode can still prevent Windows startup. Legacy reporting still lacks batch-level idempotency. Existing certificate permissions, installer rollback and other previously recorded issues are outside this release. No new remote-host, actual panel database/Redis, full subscription download, WAN/packet-loss, Windows runtime or hours-long stability validation is claimed.

The device-lease change depends on the bundled core introduced by the tail fix; if reverting source commits, revert the device change before the tail change. SS2022 validation can be reverted independently. No configuration or database migration is involved.

Packages: Linux amd64, Linux arm64, Linux armv7, Windows amd64. See HY2-TAIL-LIFECYCLE.md, HY2-ACTIVE-DEVICE-LEASES.md and SS2022-CREDENTIAL-VALIDATION.md for details.
