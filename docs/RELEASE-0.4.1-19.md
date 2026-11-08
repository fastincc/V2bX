# fastincc-v0.4.1-19 — Default Vision splice and dynamic-limit efficiency

## Changes since -18

Three independently committed repairs are included:

- `7652b19`: restore eligible default Linux Vision downlink splice by sharing atomic readiness between the original and routed connections. The default path preserves source-read billing, per-generation ownership, revocation and reliable checkpoints. Fixed-rate and ineligible connections retain buffered forwarding. This does not enable the adjustable Vision prototype or add upload zero-copy.
- `93e7f36`: remove an unused timestamp from each dynamic observation slot. Window payload storage falls from 16 to 8 bytes per second per active user: 480 / 2,400 / 28,800 bytes for 60 / 300 / 3,600 seconds, before allocator and control-object overhead. Policy precision, thresholds and expiry are unchanged.
- `e7b8e9a`: allow Go 1.25 to inline the dynamic wait path when no temporary quota is active. The original limited slow path, cancellation, refunds and static ceilings remain in force. No sampling reduction or deferred billing is introduced.

## Defaults and compatibility

**Both prototypes remain off by default:** `EnableVisionSplice: false` and `AnyTLSAdaptiveQuickACK: false`. Missing configuration fields remain false, and packaged examples do not enable them. The restored basic Vision splice is separate from the adjustable prototype: it does not advertise prototype capability or remove the default Vision dynamic-policy exclusion.

No configuration, subscription, panel endpoint, database migration, authentication, real-IP trust, device admission, billing direction, reliable batch identity or persistent queue format changes are required. There is no installer change. Panel-managed dynamic limiting retains -18 semantics, including the disabled legacy local execution loop; see [the -18 release notes](RELEASE-0.4.1-18.md) and the packaged dynamic-limit interface contract.

Basic Vision uses two pipe file descriptors per active copy. It requests at most a 1 MiB pipe, subject to kernel limits, without changing system settings. Bytes are billed when consumed from the source, as on the existing write-before-output accounting boundary. A failed or cancelled output can therefore leave a larger already-read, billed tail than the older small-buffer path. This is not a guarantee that equal external failure times produce identical byte totals. Normal completed transfers must reconcile exactly; fallback drains already-billed pipe contents in order without billing them again. See VISION-SPLICE-BASELINE.md.

## Verification and performance boundaries

The exact runtime candidate `e7b8e9a` passed a local Linux arm64 VM release-equivalent gate, race/vet checks and additional isolated regressions: 737 distinct JSON test cases including subtests, zero failures; dynamic model checks, partial-quota refunds, existing-stream changes, device/IP/accounting and cleanup checks passed. All four release targets cross-compiled. The tagged release workflow reruns its gates against the release commit.

The serial performance round used real local proxy transports: 99 timed AnyTLS samples (11 smoke and 88 paired samples), 16 timed Vision samples and three existing-connection lifecycle samples. Initial adverse AnyTLS screening cases were retained; targeted eight-sample repeats averaged +0.17%, +0.81% and +0.46%. Vision downlink measured approximately +12% to +27%, bidirectional aggregate +11.5%, while upload measured -0.8%; that upload difference is retained, not asserted to be harmless noise. Sample counts and local conditions do not establish universal zero regression.

Successful sampled transfers reconciled exactly. A separate 90-second AnyTLS audit soak completed 13,384 streams and matching audit events; after full fixture cleanup, descriptors, sockets and goroutines returned to baseline and post-GC live heap was approximately 112 KiB higher. This is bounded local cleanup evidence, not an hours/days leak-free guarantee. CPU and RSS performance measurements include the synthetic client and target, not just the node process.

This round did not deploy or retest remote nodes, a panel database, public links or real subscription clients. The inherited public-link dynamic-observation difference of approximately -2.23%, unresolved early QUICKACK upload observations and historical host connectivity interruptions are not closed by these local tests. The two prototypes remain opt-in. Windows/other target compilation is not native runtime acceptance.

## Existing limitations and rollback

Memory still grows with active dynamic-policy users and their window lengths. Splice eligibility retains upstream TLS/transport restrictions. The inherited Xray certificate autoreload race is outside these three repairs; the live Vision fixture uses immutable certificates and the existing scoped VLESS checkptr exception while keeping race detection enabled. Independent-Hy2 UDP reassembly ambiguity, unsupported Windows reliable-queue locking, and legacy reporting without batch idempotency also remain unchanged.

No new database or spool format requires migration. Roll back through the existing drain/checkpoint procedure, preserving pending reliable batches and their identities. Rolling back to -18 restores its previous forwarding/memory/wait implementation; rolling back further also requires reviewing -18 dynamic-limit compatibility changes. Do not delete the spool to force a downgrade.

Packages: **Linux amd64, Linux arm64, Linux armv7 and Windows amd64**. Publishing does not deploy nodes or modify origin servers.
