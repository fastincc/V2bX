# fastincc-v0.4.1-7 — authorization, accounting and stream repairs

This release includes the thirteen node repairs tested on top of -6. Publishing does not upgrade running nodes. The paused archive-hardening candidate is excluded.

## Fixes

- Scope standalone Hysteria2 authorization and revocation to each node.
- Redact panel credentials from transport errors and debug logs.
- Stop built-in uninstall immediately when confirmation is cancelled.
- Synchronize traffic thresholds during concurrent reload and sampling.
- Associate authenticated AnyTLS physical sessions with users for revocation.
- Continue user synchronization when the online-state endpoint fails.
- Atomically drain counters and preserve concurrent increments.
- Scope billing identity mappings to each node.
- Add opt-in persistent traffic batches with acknowledged, deduplicated delivery.
- Require authenticated panel capabilities before reliable mode starts.
- Checkpoint new traffic while older immutable journal batches await acknowledgement.
- Retain retired billing identities until leased I/O and persisted tails are safe.
- Split oversized AnyTLS writes into bounded frames.

Startup failures preserve error exit status; orderly sing shutdown drains reportable tails. Real IP / trusted PROXY handling, audit attribution, authentication, subscription formats and device admission remain compatible with the documented -6 behavior.

## Panel compatibility and upgrade

TrafficBatchMode remains disabled by default. Existing panels and -6-style reporting remain supported; legacy HTTP reporting does not gain end-to-end deduplication. Existing installation asset names and .dgst files are retained.

Before enabling reliable mode, separately deploy the authenticated traffic_capabilities endpoint, durable receipt migrations, exact fractional-rate accounting and compatible billing workers, then enable the intended panel node whitelist. These panel changes are NOT included in the node archives. Do not enable reliable mode merely because the node was upgraded.

The companion panel uses 64-bit PHP, rates with at most nine effective fractional decimal digits, and per-user upload/download remainders. Resets and absolute usage edits must handle those remainders. Existing daily statistics are outside the exact account-carry guarantee. Do not remove receipts, remainders or node journals while unacknowledged traffic or late replays remain possible.

## Validation

Recorded tests cover node/API race regressions, standalone Hysteria2 QUIC authorization isolation, three AnyTLS nodes, direct and PROXY v1/v2 real IP, revocation, oversized writes, panel outages, lost acknowledgements and restart replay. Companion-panel tests cover mixed legacy/batch concurrency, fractional split equivalence and exact reconciliation with real Redis workers and proxy traffic.

A 60-minute connection churn run completed 4,296 connections with 13 matching reconciliations. An isolated FPM follow-up used approximately 30 minutes of effective Redis load across three segments, with harness interruptions documented. These are bounded acceptance tests, not production capacity or 24/72-hour guarantees.

The release workflow gates publication on regression tests and all 23 platform builds. Cross-compilation is not runtime acceptance on all platforms. Two MIPS archives additionally include softfloat binaries. Their second build can record vcs.modified=true because the first build artifact is untracked; the source revision is unchanged.

## Remaining limits

- Abrupt termination before checkpointing and persistence failures can still lose uncheckpointed traffic.
- Non-sing shutdown tails and legacy HTTP/job retry ambiguity retain documented limits.
- Device admission is not a globally atomic distributed slot allocator.
- Full production/plugin/PHP-version/subscription-client matrices have not been revalidated.
- Reliable mode rollback requires coordinated draining and reconciliation; never silently downgrade pending batches.

See TRAFFIC-BOUNDARY-CONTRACT.md and the repository acceptance documents. BUILD-CANDIDATE.md in the archive describes the preceding candidate stage; this release tag and these notes identify the formal release.
