# fastincc-v0.4.1-20 — Snapshot validation, lifecycle and EOF repairs

## Changes since -19

Four independently committed fixes are included:

- `16b76c5`: reject incomplete or malformed user snapshots before replacing authorization. Invalid HTTP status, JSON/MessagePack structure and out-of-range lengths preserve the last valid snapshot; an explicit valid empty list remains authoritative. The existing MessagePack v5.4.1 dependency is vendored with checked 32-bit length conversion and extension skipping, not upgraded.
- `0c40400`: isolate dynamic-limit diagnostics from policy expiry and node lifecycle locks using a bounded, nonblocking diagnostic queue. A blocked logger no longer prevents temporary limits from expiring. Queue overflow is counted and later summarized; diagnostic delivery is not guaranteed. Access audit and billing records do not enter this queue.
- `22c063c`: preserve configured activity deadlines on the default Linux Vision splice path. Successful source reads and destination writes update activity; EOF/half-close keeps the remaining active direction subject to its existing timeout. Zero-copy remains eligible under the existing rules; this does not enable the adjustable Vision prototype.
- `f03fe84`: expose optional read/write half-close methods only when the underlying transport supports them. AnyTLS streams without half-close now reach the router's full-close branch at EOF instead of leaving a client waiting after receiving the tail. Real TCP half-close retains reverse traffic, and ending one AnyTLS logical stream does not close its siblings.

## Defaults and compatibility

**Both prototypes remain disabled by default:** `EnableVisionSplice: false` and `AnyTLSAdaptiveQuickACK: false`. Missing fields remain false; packaged examples do not opt in. Default basic Vision splice remains separate from the adjustable prototype and its panel capability negotiation.

No configuration, subscription, panel endpoint, database migration, authentication, trusted real-IP handling, device admission, billing direction, reliable batch identity or spool format changes are required. No installer changes are included. EOF releases active device ownership while retaining the existing grace period. Static and dynamic rate waits remain cancellable; already transferred bytes keep their accounting, and unsent bytes are not billed.

The Go constructor `NewConnRateLimiter` now returns `net.Conn` rather than `*Conn`, allowing capability-specific implementations. External Go code relying on the concrete return type must use optional interface assertions. This is not an installation/configuration or wire API change. AnyTLS itself still does not provide true TCP half-close semantics.

## Verification and performance boundaries

The combined runtime candidate is `f03fe84`. Local race regression, configuration-default checks and four-platform compilation were performed. Dedicated native Linux EOF tests compare failing pre-fix behavior against the repaired path, separately from throughput completion markers. They cover complete tail delivery, shared-session isolation, real TCP half-close, trusted synthetic PROXY IP, device ownership, rate cancellation and exact in-memory accounting. Repeated connection cleanup returned goroutines to baseline after the client's idle cleanup interval.

Pre-release performance review compares the released -19 runtime directly with the final four-fix candidate. Independent upload and download, long bidirectional transfers, fixed offered load CPU cost and repeated same-binary controls are evaluated separately. Earlier adverse AnyTLS upload samples remain relevant; the EOF-only comparison does not clear the preceding three repairs by itself. Measurements are bounded native Linux loopback evidence, not proof of universal zero regression, physical CPU isolation, WAN throughput or real-client compatibility.

The tagged workflow runs all inherited release gates plus explicit EOF regressions and real 32-bit runtime checks for MessagePack length handling. Linux build/test execution and cross-platform compilation do not constitute native runtime acceptance of every target. This release does not claim fresh public-client, full subscription or real panel-database billing acceptance.

## Remaining limits and rollback

Dynamic diagnostics are bounded but may be dropped or delayed. Other synchronous log paths are unchanged. AnyTLS bidirectional scheduling, the upstream target Nagle/delayed-ACK behavior and historical host interruptions are not declared solved. QUICKACK remains opt-in. Default Vision splice retains existing source-read accounting and potential already-read tails on failed output; it is not a guarantee of identical byte totals under externally timed cancellation.

Inherited Xray certificate autoreload races, independent-Hy2 UDP reassembly ambiguity and Windows reliable-queue locking limitations are outside these four repairs. See the retained earlier release notes and protocol documentation.

No database or spool migration is necessary. Use the existing drain/checkpoint procedure when rolling back, preserve pending reliable batches and their identities, and never delete the spool to force a downgrade. Returning to -19 also restores its snapshot, dynamic-diagnostic, Vision timeout and EOF behavior.

Packages: **Linux amd64, Linux arm64, Linux armv7 and Windows amd64**. Publishing this release does not deploy or restart nodes and does not modify origin servers.
