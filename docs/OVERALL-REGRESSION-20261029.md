# Overall regression: nine fixes, four boundaries and panel capability API

2026-10-29. Tested node source HEAD: `0af03ac` (functional code through
`0ac1b3d`); panel capability implementation: separate local commit `3cff0a3`.
No product code was changed during this regression. Nothing was pushed or
released, and no production/source server was contacted.

The selected regression paths passed. This does **not** close the existing panel
fractional-rate rounding issue or authorize reliable-billing rollout.

## Coverage and results

| Item | Verification this round | Result and scope |
| --- | --- | --- |
| 1. Independent Hy2 cross-node authorization | Node-specific authentication plus real loopback QUIC on Linux | Own-node traffic accepted; cross-node and revoked access rejected. Not a panel-backed Hy2 WAN billing test. |
| 2. Credential-bearing API errors | Synthetic transport/parser error tests; live test-log credential matching | Regression passed; no tested credential matched inspected candidate logs. |
| 3. Cancelled built-in uninstall | Actual candidate in disposable Linux chroots | n, N, no, newline and EOF cancel; y/Y confirm: seven cases passed. Installed directories were never uninstall targets. |
| 4. Concurrent threshold reload/sampling | sing/Xray race-enabled and native Linux regression | No test failure or race report. Full live topology replacement was not tested. |
| 5. AnyTLS revocation leaves transports open | Real three-node long streams and fresh reconnect | Revoked node's old/new access blocked; other nodes continued. |
| 6. Online API failure blocks revocation | Online endpoint 503 while user endpoint revokes | Revocation still applied; unaffected nodes continued. |
| 7. Concurrent counter clear loses increments | Counter initialization, Read/Write accumulation and byte-conservation tests | Passed locally with race detector and on Linux. |
| 8. Removing A's user breaks B's billing identity | Scoped mapping test plus same-user three-node WAN accounting | Other nodes retained traffic ownership; final per-node totals matched. |
| 9. Ambiguous traffic report failures | Failure-before-apply, lost acknowledgement, crash/restart, replay and final real DB reconciliation | Opt-in durable path passed. Legacy reporting is not made exactly-once by these tests. |
| 10. Reliable mode deployment boundary | Capability contract tests and actual controller refusal | Disabled capability rejected startup with exit 1; no silent legacy fallback. Global batch mode stayed off. |
| 11. New traffic remains volatile behind an unacked batch | Continue transferring during failure; inspect immutable queue before/after SIGKILL | Both affected nodes retained two batches and all checkpointed bytes; old identity/payload hashes were unchanged. |
| 12. Retired accounting memory lifetime | Leased I/O, half-close, failed persistence, generation rotation/reclamation and bounded cleanup | Passed, including 1,000 identity rotations. Five closed fixtures returned goroutines from baseline 5 to 5. |
| 13. AnyTLS oversized Write | Frame/short-write/concurrent-write tests plus three real WAN nodes | Single writes of 65,535, 65,536, 81,920 and 4,194,304 bytes round-tripped on all three nodes. |
| 14. Formal panel capability endpoint | 12 real Laravel Kernel cases and five formal Nginx/FPM HTTP cases; real controller in full node fixture | Passed. Formal endpoint remained enabled=false; enabled-state flags were isolated to the fixture process. |

Lifecycle follow-ups were also exercised: startup failures return nonzero,
checkpoint/report share sealing cadence, and normal sing shutdown drains the
active stream before sealing/replaying its final bytes. Thirteen limiter tests
covered device admission, snapshots, hot updates and connection lifecycle; this
was not a complete live multi-region device-limit deployment test.

## Test inventory

- Local selected regression: 63 top-level tests passed under `-race`; frame module:
  three more passed under `-race`. Targeted `go vet` passed.
- Fresh Linux test binaries: the same 63 top-level tests plus three frame tests
  passed. Four opt-in tests initially skipped by the ordinary suite were then
  explicitly enabled and passed: TCP/UDP audit evidence, closed-fixture goroutine
  cleanup, 60-second eight-user soak and 30-second long stream. Total: 70 selected
  Linux top-level passes; this is not an unrestricted `go test ./...` claim.
- Seven disposable-chroot uninstall cases passed.
- Twelve real Kernel capability checks and five formal HTTP checks passed.
- Real WAN: three IP modes, six negative authentication/PROXY cases, eighteen
  clients at concurrency six, twelve large-write/node combinations, failure and
  restart recovery, revocation isolation, graceful tail recovery, and six
  uninterrupted 60-second streams.
- The unchanged installer scripts passed `bash -n`; install/upgrade/rollback was
  not executed on the installed service.

The first local attempt was blocked from binding loopback ports by the sandbox;
it passed after the required test permission was granted. A frame-only command
initially selected the vendored module's uncached standalone dependency; it was
corrected to test with the root application's dependency graph. One live assertion
initially looked for the capability error on stdout instead of the configured
application log; corrected evidence confirmed refusal. These were test-environment
or harness failures, not product changes, and the initial records were retained.

## Traffic reconciliation

The isolated blue-green fixture used the deployed capability method and real
panel controller, receipt service, transaction job and database. Fault injection,
test-only configuration and a fake queue isolated the run; shared queue workers
and email were not exercised. Three hidden test nodes shared one authorized test
account, with rates 1, 2 and 0.5.

| Node | Independent upload and download, each direction | Unique batches | Rate |
| --- | ---: | ---: | ---: |
| A | 28,669,950 bytes | 42 | 1 |
| B | 30,052,350 bytes | 47 | 2 |
| C | 29,028,350 bytes | 46 | 0.5 |

All per-node target totals equalled unique batch totals. There were **135 unique
batches and 142 accepted requests**. Expected and actual weighted DB increments
both equalled **103,288,825 bytes per direction**. Local pending files and unapplied
receipts were zero before restoration. The short snapshot immediately after the
load was lower because delivery backlog was still draining; only the drained
ledger is the final reconciliation.

Audit events matched all 46 successful target connections. PROXY v2 IPv4, PROXY
v1 IPv6 and direct client IPs matched audit and online reports. Credential checks
exported counts only; inspected test logs had zero matches.

## Complete candidate process CPU and memory

The candidate ran all three sing nodes with audit and durable checkpointing.
Samples came from that candidate PID every 250 ms, excluding the echo server,
SSH, test clients and panel. CPU 100% denotes one logical core. Other native
stress tests had finished before this measurement.

| Window | Mean CPU | Peak RSS | End RSS |
| --- | ---: | ---: | ---: |
| Idle, 10 seconds | 0.20% | 62.29 MiB | 62.29 MiB |
| Six WAN streams, 60 seconds | 1.56% | 67.09 MiB | 67.09 MiB |
| After load, 10 seconds | 0.30% | 67.12 MiB | 66.94 MiB |

Each stream echoed 10 KiB and paused 100 ms between exchanges. This is a bounded
functional load, not saturation or a before/after performance comparison. RSS
had not returned fully to the initial baseline after ten seconds; that alone
neither proves a leak nor establishes a long-term memory plateau.

Separately, the eight-user loopback audit soak completed 8,872 streams in 60
seconds, with 6.07 MiB Go heap after GC in the test process. Its immediate end
goroutine count was 140 versus initial 7; client sweepers have delayed cleanup,
so this immediate sample is not a settled-leak measurement. The independent
closed-fixture test waited through its cleanup interval and returned 5 to 5.
Neither number is the full candidate's RSS. The separate 30-second long stream
transferred 6,735,872 bytes per direction.

## Known failure, remaining limits and cleanup

The existing panel fractional rounding failure was reproduced again in a fully
rolled-back transaction: rate 0.5, two `[1,1]` batches charged two bytes per
direction, while one `[2,2]` batch charged one. Successful even-byte reconciliation
does not resolve arbitrary partition-dependent rounding. This remains a rollout
blocker pending the panel remainder/transaction change or explicit acceptance of
that billing rule.

Other limits remain: legacy ambiguous failures; bytes since the last successful
checkpoint; in-memory increments during full/failed storage; backlog delivery at
the normal reporting cadence; non-sing graceful-drain coverage; shared queue and
Redis fault/load acceptance; full client/protocol/platform matrices and long-term
production load. The archived hardening candidate was not restored.

After exact reconciliation the account fields were restored, all three temporary
nodes deleted, and candidate/echo/fixture processes and SSH forwards stopped.
Applied receipts were retained. Installed node binary/config hashes and stopped
service state were unchanged. The formal capability endpoint remained deployed;
its source hash, panel accounting code and disabled global batch configuration
were unchanged by this run.

Fresh Linux candidate SHA256:
`7d229d054a06e9554e717c2224d1a4a065ed72bff67a92a3038e118a20975599`.
Internal endpoint/account identifiers, original logs, configuration and private
backups remain outside the public repository.
