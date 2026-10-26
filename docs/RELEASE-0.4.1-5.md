# fastincc-v0.4.1-5 — device/IP admission fix

- Enforce per-node IP admission atomically instead of depending only on delayed panel counts. Reporting no longer clears admission leases.
- Active streams retain leases; disconnected IPs retain a 180-second idle lease. Lowering limits does not forcibly disconnect existing admitted sources.
- Device-limit-only updates apply without deleting/re-adding authenticated users.
- Add bounded aggregated allowed/denied device events to the existing online report. Events carry panel numeric user IDs, not authentication secrets. A compatible panel is required to display history; the panel's history retention is configured separately.
- Existing AnyTLS PROXY v2 and audit defaults remain unchanged. PROXY reception defaults ON without relay allowlist validation; reported client addresses are unverified claims. Direct/no-header listeners must explicitly disable AnyTLSProxyProtocol.Enable. Audit can be disabled with AnyTLSAuditAccess=false.

## Validation and limits

Device limiter race tests and scoped AnyTLS/device-update regression passed locally. Complete subscription tests passed through the experimental relay. Controlled three-IP tests with limit two allowed exactly two sources across reporting cycles and rejected the third; hot updates were exercised.

This is local atomic admission, not globally atomic quota allocation across nodes. The test panel received traffic reports, but its queue consumer was not running: end-to-end billing ingestion remains unverified. Long-duration production-scale stability and matched network-throughput comparisons remain pending. Other UDP speed-limit issues are not fixed by this release. Access-history decisions are admission results, not proof that destinations connected successfully.

The release workflow runs regression checks before building all platform archives. Production nodes are not automatically modified by publishing this release. Keep the previous binary and configuration for rollback and roll out gradually.
