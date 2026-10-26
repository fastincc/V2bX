# fastincc-v0.4.1-4 — 2026-10-26

Release: https://github.com/fastincc/V2bX/releases/tag/fastincc-v0.4.1-4

Build: https://github.com/fastincc/V2bX/actions/runs/40320438437

- Source: 39a451c17e1e22a5f5b7ed980e62175a8f1145f2; installed binary build metadata matched and vcs.modified=false.
- AnyTLSAuditAccess now defaults true; explicit false remains respected. PROXY v2 remains default-on. Existing configured logging level/disabled state is not overridden.
- Node JSON default/true/false/null cases were exercised through authenticated test connections, checking output presence, credential-free structured events, normal-close filtering and unchanged byte counters. The existing AnyTLS regression suite passed locally on the test VPS and in CI.
- All 23 platform builds and release publication succeeded; 46 assets are available and latest resolves to this version.
- The installation wizard now explicitly sets AnyTLSAuditAccess=true for new AnyTLS nodes and selects info for their shared sing core. Single-node AnyTLS, sing Shadowsocks and Xray VMess generator tests passed; non-AnyTLS-only configurations retain error level.
- Public installer and generator matched reviewed source. Linux x86_64 fresh latest installation, empty-node sing/Xray startup, config-preserving update, restart and uninstall passed.
- Existing experimental service PID, restart count, binary and configuration hashes remained unchanged. Temporary installation was cleaned up. No production node was modified.
- New source and installer commit scans found no secrets. Actual user/node credentials were not used for installation acceptance or published.

For an existing configuration with sing Log.Level=error/warn or Disable=true, access logs remain suppressed. Set the corresponding sing logger to info if access records are required. This release does not silently overwrite that choice. Logging adds per-stream serialization/output work; production performance and downstream collector acceptance remain deployment-specific.
