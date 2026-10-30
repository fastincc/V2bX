# fastincc-v0.4.1-11 — configuration recovery, tail accounting and UDP limits

## Fixes

Six independently committed audit fixes follow the -10 baseline:

- Validate panel configuration, intervals and regular expressions before applying them. Publish immutable rule snapshots and commit response cache state only after successful application.
- Parse and validate watched local files as candidates. Invalid or partially written configuration preserves the last working service; atomic file replacement is supported.
- Restore nodes and durable reporting state after failed coordinated shutdown or local runtime replacement. Join previous task generations and release resources from partially created nodes.
- Replace credential identities in ordinary Hysteria2 and AnyTLS logs with safe user IDs, including when AnyTLS audit logging is disabled.
- Retain revoked-user tail traffic in Xray and standalone Hysteria2 until successfully persisted. Separate authorization generations so re-adding the same credential cannot revive an old authorization or reassign its tail to a new user.
- Apply existing device and speed limits to authenticated sing UDP sessions. Preserve datagram boundaries and transport framing; close and deadlines cancel rate waits.

An additional independently committed release-blocker fix addresses a pre-existing race in the pinned standalone Hysteria2 QUIC dependency: extended ACK and timed-loss callbacks now use synchronized congestion-controller snapshots. The same issue reproduced against unmodified -10. The original dependency version is bundled with a minimal local patch; no broad dependency upgrade is included.

## Compatibility and behavior

Existing configuration fields, subscription/authentication formats, panel APIs, real-IP sources and upload/download directions remain compatible. TrafficBatchMode still defaults to auto. No panel patch, new API, fractional remainder accounting or database migration is required.

UDP clients that previously bypassed configured limits can now be refused or rate-limited. Positive revoked-user tails in legacy mode are no longer suppressed by ReportMinTraffic. Legacy HTTP reporting still has no batch-level idempotency guarantee. Ordinary log identities and error details are intentionally sanitized.

Failed local reload recovery can interrupt existing connections; clients may need to reconnect. Last-good backups cover directly referenced inputs, not arbitrary nested include trees.

## Validation

On Linux amd64, isolated suites ran three rounds each on two test hosts, including actual AnyTLS, VMess/VLESS, Hysteria2 QUIC and Shadowsocks UDP paths. Negative controls against -10 detected the original UDP admission, revoked-session and tail-accounting defects. The release preflight exposed the additional QUIC race above; after its patch, the complete ACK-handler race suite and 30 rounds of real Hysteria2 race tests passed. CI includes the six-fix regression gate and patched ACK-handler race tests.

An isolated current-panel HTTP/controller/database path with an AnyTLS proxy verified outages, lost acknowledgements, replay, pending-batch restart, configuration recovery, repeated connections and trusted PROXY v2 address fields. 24 unique batches across 32 accepted requests reconciled exactly in both traffic directions. Test account fields were restored and temporary services stopped. This is isolated acceptance, not production verification or a full public subscription/middleware test.

Release targets: Linux amd64, Linux arm64, Linux armv7 and Windows amd64. Non-amd64 targets are build-validated, not runtime-certified. Short resource observations are not a long-term memory-leak or capacity benchmark.

## Known remaining limitations

- Shadowsocks dependency user hot-update can race with connection/packet handling. Stable-user UDP tests do not prove this separate hot-update race is fixed.
- Panel-driven per-node runtime rebuild still has panic paths on AddNode/DelNode failure. Local Watch transaction recovery does not cover every runtime application failure from panel updates.
- Reliable queue locking remains unsupported on Windows. Auto negotiation of reliable mode can prevent Windows startup. Do not delete pending queues or silently downgrade reliability to bypass this limitation.
- Sustained queue catch-up, storage failures and process termination after the last checkpoint retain previously documented boundaries.

Publication does not deploy nodes or change any panel or origin host. Earlier tags remain unchanged.
