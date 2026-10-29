# 2026-10-29 full-platform repair candidate

This candidate contains the nine audit fixes and four follow-up boundary repairs on top of fastincc-v0.4.1-6. It is built from the repair branch by workflow_dispatch; the binary version identifies its exact Git commit. It does not publish a release, move main, update running nodes, or include the paused archive-hardening branch.

## Included node repairs

1. Isolate standalone Hysteria2 authorization and revocation by node.
2. Prevent panel credentials from appearing in transport errors and debug logs.
3. Return immediately when built-in uninstall is cancelled.
4. Synchronize report thresholds during concurrent node reload/sampling.
5. Revoke authenticated AnyTLS physical sessions with their user.
6. Continue user revocation synchronization during online-state API failures.
7. Atomically drain counters without losing concurrent increments.
8. Keep user-to-billing identity mappings scoped to each node.
9. Add explicit opt-in durable delivery and deduplicated batch acknowledgements.
10. Gate reliable mode on authenticated panel capabilities; fail startup when unavailable.
11. Continuously checkpoint into an immutable multi-batch journal while older batches wait for acknowledgement.
12. Reclaim retired billing identities only after leased I/O and persisted tail traffic are safe.
13. Split oversized AnyTLS writes into bounded protocol frames.

Lifecycle follow-ups preserve startup error exit status and orderly sing tail draining. Real-IP/PROXY parsing, audit attribution and device admission compatibility were covered in the recorded regression. Existing identity credentials and subscription formats are not changed by packaging.

## Panel compatibility and defaults

`TrafficBatchMode` remains disabled by default. Legacy reporting remains supported but does not gain cross-request deduplication from this node update alone. Reliable mode requires the authenticated capability endpoint, durable receipt storage, per-node enablement and compatible billing workers.

The panel fractional-rate carry fix and its database migrations are separate panel changes, not part of the node ZIP. Subsequent blue-green tests verified the companion panel fix, exact fractional reconciliation, real proxy traffic and isolated FPM stability. The older regression documents' fractional-accounting blocker describes their historical test stage; it does not mean the companion fix is bundled here. Production must separately integrate the companion interface, receipt and fractional-carry changes and update all billing workers before enabling reliable delivery.

The accepted panel environment uses 64-bit PHP. External counter resets must handle both integer usage and fractional remainders. Daily statistics are outside the account-carry guarantee.

## Validation and remaining limits

Before this push, the selected node race regressions, AnyTLS frame regressions and targeted vet were rerun using Go 1.25.0 and GOEXPERIMENT=jsonv2. The workflow additionally runs its existing regression gates before building the full platform matrix. Completion of those jobs is required before treating an archive as built; cross-compilation is not runtime acceptance on every operating system.

Earlier acceptance includes real standalone Hysteria2 QUIC isolation; three AnyTLS nodes with PROXY v1/v2 and direct IP; revocation, outage, lost acknowledgement, restart replay and accounting checks; oversized transfers; and a 60-minute real-connection churn run. The FPM follow-up used approximately 30 minutes of effective Redis load split across three segments, with recorded harness interruptions and a final five-minute cooldown. These are bounded tests, not 24/72-hour or production-scale guarantees.

Uncheckpointed bytes at abrupt termination or persistence failure remain a risk. Non-sing core shutdown tails, legacy HTTP retry ambiguity, globally concurrent device admission, and full production/plugin/version compatibility retain the boundaries in the linked documents.

- [Nine audit fixes](AUDIT-FIXES-1-9.md)
- [Boundary repair acceptance](BOUNDARY-FIXES-ACCEPTANCE.md)
- [Overall regression and historical evidence](OVERALL-REGRESSION-20261029.md)
- [Traffic boundary contract](TRAFFIC-BOUNDARY-CONTRACT.md)

Build archives include the executable, examples, licenses and rule datasets. They contain no deployed node configuration or credentials. Archive digests identify the downloaded build; rule data fetched by the workflow may change independently of the source commit.
