# fastincc-v0.4.1-21 — Vision regression test correction

## Changes since -20

This release corrects the Vision half-close regression test and adds coverage for the pinned core's TLS record-boundary prerequisite. No runtime forwarding code or dependencies changed.

A FIN sent before the node enters splice does not guarantee a later splice transition: TCP reads may split TLS records, leaving the connection on the existing buffered path. The before-splice test now accepts either path while preserving complete payload delivery, activity across the configured timeout, exact in-memory accounting, trusted synthetic PROXY IP, duplicate-snapshot protection and eventual idle closure. The after-splice case still requires a node splice transition before FIN and after transfer.

The added record-boundary test verifies whole records, multiple buffers within a single read, and the absence of retained framing across separate reads. Transition counts are recorded for diagnosis.

## Compatibility and performance

Configuration, subscriptions, panel interfaces, real-IP trust, device limits, static/dynamic rate limits and billing behavior remain those of -20. No database or spool migration and no installer change are required.

Both prototypes remain disabled by default: `EnableVisionSplice: false` and `AnyTLSAdaptiveQuickACK: false`. Default basic Vision splice remains separate from the adjustable prototype.

Before release preparation, a same-option build of the current runtime was byte-identical to an unmodified -20 rebuild. Test changes add no product forwarding overhead. Published binaries necessarily carry the new release version and commit metadata; this is not a new throughput optimization or a resolution of earlier AnyTLS bidirectional performance uncertainty.

## Verification

Dedicated isolated Linux testing on two hosts passed: 13 Vision tests repeated twice per host; 39 successful 4 MiB half-close transfers per host; before/after-splice with Go parallelism 1 and 2; TLS 1.2 buffered fallback; 1,000 record-boundary repetitions and 100 extended split-matrix rounds; 90 repeated connections with descriptor recovery; static-limit, device-revocation and accounting checks.

Test-only fault injection on both hosts confirmed that payload corruption, a one-byte accounting discrepancy, an unexpected synthetic source IP, delayed idle closure and missing splice observations still fail their respective assertions. Original test failures are retained as evidence of the overly strong prerequisite. The opt-in profiling entry is skipped unless explicitly requested; it is not counted as a pass.

The existing release workflow gates builds on regression tests, including Linux Vision live tests with the race detector. Two-host results are loopback evidence, not public-client, complete-subscription or real panel-database acceptance. Cross-compilation does not constitute native Windows or ARM runtime acceptance.

## Remaining limits and rollback

All -20 runtime limitations remain. In particular, pre-splice buffered fallback is not eliminated, and AnyTLS bidirectional scheduling/performance uncertainty is not declared solved. Returning to -20 restores the previous test suite, without a runtime behavior change from these test edits. Preserve pending reliable batches and follow the normal checkpoint procedure for any operational rollback.

Packages: Linux amd64, Linux arm64, Linux armv7 and Windows amd64. Publishing does not deploy or restart nodes or modify origin servers.
