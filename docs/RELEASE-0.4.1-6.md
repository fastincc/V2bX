# fastincc-v0.4.1-6 — stream compatibility and known-IP admission

This release fixes the slow-transfer regression found in -5. Roll out gradually; publishing does not update running nodes.

## Fixes

- Preserve underlying connection capabilities through the device lease wrapper, including half-close discovery. Closing one direction does not prematurely release an active lease, and final release remains once-only.
- With a compatible panel, distinguish an already-online IP reconnecting or moving between nodes from a genuinely new source. Admission uses the union of valid panel IPs and local leases.
- Preserve unchanged users' local leases on same-node configuration reload. Continue reporting active streams even when no new logical streams are created.
- Retain valid online state on failed or malformed API responses. Expired snapshots do not open new slots during outages; existing local leases remain usable.
- Fetch admission state before consuming node/user ETags so an online-state failure does not discard a pending configuration update.

## Panel compatibility

The existing `alive` counts response is still supported. Complete known-IP cross-node/restart recovery requires the companion panel snapshot API; upgrading only the node against a count-only panel cannot remove the old information limitation.

New nodes request `X-Device-IP-Snapshot: 1` from the authenticated `/api/v1/server/UniProxy/alivelist` endpoint. Compatible panels additionally return `device_snapshot` with `version: 1`, a positive `ttl` (seconds), and `ips` as an object mapping numeric user IDs to objects mapping normalized IP addresses to their positive remaining lifetime in seconds. Empty maps must be JSON objects. Return only eligible users for that node, exclude denied-history records, and use `Cache-Control: no-store, private`.

API failures are no longer interpreted as zero online devices. An unavailable or invalid initial online-state response can prevent node startup. After snapshot support has been negotiated, silently removing the snapshot field is not a safe live rollback: coordinate node rollback/restart with panel rollback.

## Validation

- Repeated limiter/API race tests (20 rounds), half-close and once-only release, IPv4-mapped IPv6, snapshot expiry/recovery, and remaining-slot contention passed.
- Full subscription tests covered known-IP movement, node restart, rejected new sources, and admission after the old source left; existing traffic remained usable.
- Actual relay and direct-mode complete slow downloads/uploads passed. A 120-second relay transfer completed with its expected contents while 48 concurrent requests passed.
- Eight-user AnyTLS soak completed 18,660 logical streams with matching per-user counters and audit events. Closed test fixtures returned to their goroutine baseline after the client's cleanup interval.
- A scoped API outage rejected new sources after expiry while retaining existing-source access; recovery reopened the free slot.
- Experimental panel queue consumption matched expected traffic accounting, including rate-multiplied test traffic. No accounting or daily-statistics formulas changed.

## Unchanged behavior and limits

- This remains periodic synchronization plus per-node atomic admission, not a globally atomic distributed slot allocator. Cross-node concurrent races are not fully eliminated.
- Disconnected sources retain an idle lease, with additional reporting/polling delay before reuse; release is not instantaneous. Lowering a limit does not forcibly evict existing sources.
- AnyTLS PROXY reception and audit defaults are unchanged. Without relay allowlist validation, claimed client IPs remain untrusted. Direct listeners must explicitly disable PROXY reception.
- No subscription formats, user credentials, node IDs, billing multipliers or daily-statistics behavior were changed. Panel access-history retention is independent of this release.
- Bounded tests do not establish day-long or production-scale stability. Other UDP speed-limit issues are outside this release.
