# Four boundary fixes: implementation and acceptance

2026-10-29. Code under test: `0ac1b3d`, based on the prior nine-fix branch
at `ff04720`. New changes are local only; this is not a release or production
validation. The installer and panel application source were not changed.

## Independent implementation commits

| Boundary | Commit | Result |
| --- | --- | --- |
| AnyTLS oversized Write | `0814259` | Split a logical write into valid frames; serialize concurrent writes; return partial progress on error and close a short-written session. |
| Reliable billing deployment | `03d5c86` | Require authenticated, node-specific durable-ack and permanent-dedup capabilities before starting batch-mode inbounds. Preserve explicit legacy mode. |
| Pending acknowledgement blocks persistence | `44f7494` | Independent periodic checkpoints, immutable multi-batch queue, durable legacy migration and bounded storage. |
| Retired accounting identities accumulate | `2e2e9a3` | Account by generation; retain ownership through in-flight I/O; reclaim only drained, zero-counter retired generations. |

Acceptance-driven follow-ups are separate commits:

- `2e397e4`: checkpoint and delivery share one sealing cadence, avoiding extra batches.
- `0a7d521`: startup errors exit nonzero; normal shutdown closes controllers before the core, enabling final sing drain/checkpoint.
- `0ac1b3d`: use 65,528-byte payload fragments, keeping payload plus seven-byte header within the reusable buffer limit.

## Compatibility and behavior changes

- Wire protocol, credentials, numeric audit user IDs, subscriptions and device-limit interfaces remain unchanged. Real-IP selection remains PROXY v2, PROXY v1 or direct according to configuration. A client implementation that itself emits invalid oversized frames still needs its own fix.
- Legacy panel configurations remain readable. Legacy reporting still cannot provide safe automatic retries after an ambiguous failure. No automatic enablement or downgrade is performed.
- Batch mode now requires the proposed capability endpoint. Missing/disabled/incomplete capabilities, unknown version or finite receipt retention reject startup. Startup failure now correctly returns a nonzero process exit status.
- Batch snapshots include every positive byte count, bypassing the old `ReportMinTraffic` suppression. Legacy threshold behavior is unchanged. This avoids stranding small retired counters but can increase billed small-flow traffic compared with threshold-based suppression.
- `TrafficCheckpointSeconds` defaults to 1 (range 1–60). `TrafficSpoolMaxBytes` defaults to 64 MiB and `TrafficSpoolMaxBatches` to 4096. Provision space for atomic replacement and filesystem metadata in addition to payload capacity.
- The version-2 queue preserves old batch identity/payload during migration. Older binaries cannot consume it. Drain/export and back up before rollback; never delete the version marker to force compatibility.
- Lease tracking adds synchronization to accounting and intentionally prevents unsafe optimized unwrapping. Half-close is forwarded. Graceful draining is implemented for sing; no equivalent lossless-shutdown claim is made for the other cores.

## Verification

Local race-enabled regression passed for panel API, counter, queue, limiter,
controller and sing/Xray/independent Hy2 accounting paths. Frame splitting and
real TLS large-write/revocation tests passed after the final buffer optimization.
Targeted `go vet` and the Linux amd64 all-core build passed. The local macOS linker
emitted an existing LC_DYSYMTAB warning; test exits were successful.

On the test VPS, the selected Linux suites recorded 61 top-level passes and four
explicit sing live-fixture skips, with no failures; the separate final frame
suite added three passes. The skips are not counted as passes. Most Linux test
binaries were built before the CLI and final buffer follow-ups; those changes
were separately tested locally and with the final candidate/frame binary.

Cross-host acceptance used three isolated AnyTLS nodes, a blue-green panel adapter,
real panel controller/receipt/transaction-job code and the real test database.
The adapter supplied the proposed capabilities and used a fake queue; shared
queue consumers and email delivery were not exercised.

- PROXY v2 IPv4, PROXY v1 IPv6 and direct addressing matched audit and online reports; no tested credential appeared in the inspected test logs.
- Single writes of 65,535, 65,536, 81,920 and 4,194,304 bytes round-tripped on all three nodes. Eighteen clients at concurrency six also passed.
- Wrong passwords, absent required PROXY headers, wrong PROXY version and unexpected PROXY on a direct listener were rejected.
- While one node's push failed and another's acknowledgement was lost after application, both continued saving new traffic behind the unacknowledged head. Two queued batches per affected node survived SIGKILL/restart with unchanged identities and payload hashes, then replayed without duplicate billing.
- With the online endpoint failing, revoking one node's user terminated its old stream and denied fresh connections while the other two nodes continued their streams and accounting. Reauthorization succeeded.
- Normal candidate termination drained a live stream; the final tail was sealed and replayed after restart.
- Final independent echo totals equalled unique batch totals per node. There were 51 unique batches, 58 accepted requests and exactly 92,312,555 weighted bytes per direction in both the expected ledger and database. These successful cases do not establish correctness for arbitrary fractional rounding.

## Measured costs

These are bounded microbenchmarks and short isolated load tests, not network
capacity estimates or production latency guarantees.

| Measurement | Before | After |
| --- | ---: | ---: |
| Blue-green 10 KiB frame write, median of three | 566.2 ns | 603.0 ns |
| Blue-green 65,535-byte write, median of three | 20,662 ns | 3,589 ns |
| Blue-green 65,535-byte allocated bytes/op | about 73,794 | about 129 |
| VPS counter wrapper against discard target | 5.563 ns/op | 23.39 ns/op |
| VPS 30-second, eight-user audit soak streams | 4,448 | 4,460 |

The small-frame median increased about 6.5%; the large-frame allocation reduction
comes from staying within the buffer pool. The counter microbenchmark adds about
17.8 ns/op with zero allocations in both variants. It does not establish the cost
of disabling optimized copy paths on real networks. One 1,000-user durable journal
checkpoint took about 8.72 ms/op and allocated about 387 KiB/op on the test VPS.
The 30-second soak was performed before the final buffer optimization and is too
short to establish a long-term memory plateau. A deterministic test separately
rotated 1,000 credentials and reclaimed all eligible retired generations.

## Remaining deployment gates

The panel capability endpoint is a proposed contract, not an implemented panel
change. More importantly, an isolated, rolled-back real-database experiment
found existing per-batch fractional rounding: at rate 0.5, two one-byte batches
charged two bytes while one two-byte batch charged one byte, in each direction.
The panel must carry fractional remainders transactionally, or the user must
explicitly accept the current partition-dependent billing rule before rollout.

See [the panel handoff](面板配套接口要求-计费边界修补.md) and
[the technical contract](TRAFFIC-BOUNDARY-CONTRACT.md).

Further limits remain explicit: abrupt failure can lose bytes since the last
successful checkpoint; full/failed storage preserves availability and retains new
counts in memory rather than stopping traffic; backlog delivery is limited to
the normal reporting cadence; other cores need their own graceful-drain acceptance;
shared Redis/queue load, every protocol/client combination and production load
were not validated in this run.

All temporary candidate/echo/panel-adapter processes were stopped, the test account
was restored after exact reconciliation, and all three temporary panel nodes were
deleted. Applied receipts were retained for deduplication. Existing installed
binary/config hashes and the stopped service state were unchanged; global batch
mode remained disabled. No source/production server was contacted or modified.
