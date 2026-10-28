# Audit fixes 1–9 (unreleased)

Historical scope: the nine-fix baseline through `ff04720`. Subsequent local
boundary fixes supersede the single-pending-batch and lifetime-retention limits
described below; see [the later acceptance report](BOUNDARY-FIXES-ACCEPTANCE.md).
Those later changes have separate panel integration gates and are not released.

Base: fastincc-v0.4.1-6, da97bc8. This branch does not merge the archived hardening candidate. No automatic publication or production rollout is implied.

## Changes

1. Independent Hysteria2 now owns an authenticator per node. Missing and revoked identities are rejected by its traffic hook. Node-map access is synchronized.
2. Panel request errors omit credential-bearing URLs, raw transport errors, response bodies and parser fragments. Raw request debug logging is disabled; HTTP status and safe error category remain available.
3. The Linux built-in uninstall command immediately returns for every response other than y/Y, including EOF; its prompt now indicates the safe default.
4. sing/Xray report thresholds are synchronized during node updates and sampling.
5. AnyTLS tracks physical transports independently from logical-stream close callbacks. Revocation closes matching transports, and a per-identity generation check rejects stale handshakes. Unidentified pending transports are conservatively closed on revocation; unaffected authenticated users remain connected. New immutable authentication services replace concurrent mutation of the pinned dependency's user map. A minimal vendored v0.0.11 patch synchronizes stream close-error reads/writes without changing the wire format.
6. An alivelist outage no longer blocks user revocation. The last valid admission state remains in force; additions and node rebuilds wait for recovery. Partially applied user responses are fetched again. A successful empty user list differs from 304 and revokes the final users.
7. Sampling uses atomic swaps and serialized samplers. Related prerequisites were also corrected: concurrent first streams share one node counter, and ordinary Read/Write accumulate rather than overwrite bytes.
8. sing billing identity is scoped by (node, credential). Revoked identity/counter entries remain as accounting-only records for the lifetime of the core so asynchronous close can report final bytes. Retired entries bypass the reporting threshold; credentials rotated for the same numeric user ID are aggregated. These entries do not authorize traffic. **Memory usage therefore depends on identities seen during the core lifetime; bounded retirement reclamation is still a follow-up.**
9. New opt-in persistent traffic batches use the contract below. Existing configurations keep the legacy push format and behavior. Legacy push still cannot guarantee loss-free replay without panel deduplication.

## Batch-mode configuration and interface

Per node, explicitly set `TrafficBatchMode: true` and `TrafficSpoolDirectory` to a private persistent directory. Defaults remain off. This is a separate implementation of the selected accounting fix, not a switch to the archived `ReliableTraffic` candidate. Linux and macOS file locking are supported; other platforms reject batch mode. sing, Xray and independent Hysteria2 implement snapshots; the multi-core selector forwards to the chosen core.

- Wire body remains numeric UID -> `[upload, download]`, with positive UID and nonnegative int64 byte counts. Duplicate accounting identities are summed first.
- Request: existing `POST /api/v1/server/UniProxy/push`, plus `X-Traffic-Batch-ID`, a random 32-character lowercase hexadecimal ID. Authentication and node parameters are unchanged.
- Panel must persist a receipt keyed by storage node type, node ID and batch ID. Same ID and payload must be deduplicated; conflicting payload must be rejected. Billing and receipt application must be transactional. Existing multiplier and accounting rules remain panel-owned.
- Confirmation must be HTTP 200 JSON containing `data: true` and the exact `batch_id`. A legacy acknowledgement, mismatched ID, HTTP error or lost response leaves the spool pending. There is no fallback to legacy push.
- A private file is written, synced, atomically renamed and its directory synced **before** the corresponding values are subtracted from live counters. Increments arriving during persistence remain in memory. Failed persistence leaves counters untouched and fails closed until restart when durability is ambiguous.
- An exclusive lock prevents two processes from using one node spool. A node-binding digest covers API endpoint, node type and node ID; changing these requires an explicit drain/migration plan.
- Acknowledged files are removed and the directory synced. Retain panel receipts so a replay after a crash cannot charge again. Do not delete an unacknowledged spool or roll back into legacy mode with a pending batch.

## Acceptance and limits

Tests cover actual TLS/PROXY and QUIC loopback transport, cross-node identity isolation, revocation while streaming, safe uninstall in a disposable Linux chroot, credential-free errors, concurrent byte conservation, persistence failure, process restart, lost acknowledgements, stale/legacy acknowledgements, and corrupted/wrong-node spool rejection. Explicit acceptance clients require test endpoints and a user ID; they never print or save credentials.

The batch spool deliberately holds one pending batch at a time. While it awaits confirmation, **new unsampled traffic remains in memory**. A crash, forced kill or node/core teardown can still lose unsampled bytes. This is not a claim of zero loss under power failure, nor a durable journal for every packet. Dynamic topology replacement, high-churn retained accounting metadata, long-running disk failures, full-platform coverage and high-load production behavior require further acceptance. The older scheduler/configuration risks outside issues 1–9 are not all addressed here.

Panel production enablement and any necessary API changes must be coordinated with the panel maintainer. Remote acceptance details, internal addresses, account identifiers, private configuration and raw logs belong outside the public repository.

## Recorded acceptance (2026-10-29)

Code commit: `7a63645`. Unreleased Linux amd64 binary SHA256:
`2c60f7a10207a9317cd6401d7442918da5fb007daad04ff760e8ad5d466c90f3`.

A dedicated Linux VPS and an isolated bluegreen PHP 8.3.36 fixture exercised real
AnyTLS traffic, control-plane revocation and database accounting. Seven unique
batches were durably accepted fifteen times across HTTP failures and restarts;
independent target traffic and billed deltas matched at 978,944 upload and
978,944 download bytes. No pending node batches or panel receipts remained.
The fixture used a fake queue and direct job execution; this is not acceptance
of production queue throughput. Test account fields were restored, temporary
processes stopped, and the installed baseline binary/configuration and global
panel flags remained unchanged. No push, release or production rollout occurred.

Targeted race tests, Linux builds, go vet, actual QUIC isolation, and bounded
AnyTLS load tests passed. Five fixture cleanup rounds settled from two baseline
goroutines back to two. This does not replace long-running production testing.

Additional finding outside these nine fixes: the pinned AnyTLS transport encodes
data-frame lengths as uint16 without splitting a single Write above 65,535 bytes.
An initial oversized acceptance-client write exposed this; bounded legal frames
passed. The oversized-write behavior is recorded for a separate fix, not claimed
as supported by this acceptance.

## Second isolated acceptance (2026-10-29)

The product binary and code commit above remained unchanged. One sing core on a
dedicated test VPS served three AnyTLS nodes. A client on a separate bluegreen
host exercised PROXY v2 IPv4, PROXY v1 IPv6 and explicit direct TLS. Audit and
online reports retained the expected client address and relay/source semantics.
Synthetic PROXY addresses were not treated as independently verified identities;
optional-trust mode continued to report `proxy_trusted: false`.

Revoking the shared user only on node A during an alivelist outage terminated A's
existing stream and rejected fresh connections. The same user's B/C streams
continued for eighteen seconds and remained correctly accounted. Reauthorization
restored A. Six wrong-password/header cases were rejected. Eighteen clients with
concurrency six completed verified payload transfers across all three nodes.

Independent echo byte counts matched each node's unique batches. With node rates
1, 2 and 0.5, upload and download accounting each totaled 27,617,280 bytes.
Twenty-seven unique batches were durably received thirty-eight times. Independent
HTTP failures, lost confirmations and a process restart retained identical spool
files and did not double-charge. Pending files and unapplied receipts were zero
at completion. Test account fields were restored and temporary nodes deleted;
applied receipts were retained. Global panel settings and installed baseline
binary/configuration were unchanged.

Linux real-QUIC two-node isolation, credential-safe errors, seven disposable
chroot uninstall cases, concurrent counter conservation, persistence failure,
revocation and device-state regressions passed. Targeted local race tests passed.
A thirty-second eight-user test produced 4,392 streams and matching audit events;
a thirty-second single stream transferred 6,735,872 bytes in each direction.
Five cleanup rounds settled below that test's initial goroutine count. These are
bounded isolated tests, not production throughput or long-term leak guarantees.
The default legacy-mode and unsampled-byte limitations above still apply.
