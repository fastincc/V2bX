# fastincc-v0.4.1-3 — 2026-10-26

Release: https://github.com/fastincc/V2bX/releases/tag/fastincc-v0.4.1-3

Build: https://github.com/fastincc/V2bX/actions/runs/40316414669

- Source: 86e2e9f61da8163a730134282ecd64ac034d376b; installed build metadata matched and vcs.modified=false.
- AnyTLS PROXY v2 reception defaults on, timeout 3 seconds, no relay allowlist required. Audit still defaults off. Explicit Enable=false preserves direct/no-header listeners. Omitted/null/empty configuration uses enabled defaults.
- Default parsing, explicit disable and existing AnyTLS regressions passed; all 23 platform builds and publication succeeded (46 assets).
- Public latest endpoint resolves to this release. A fresh installation without a version argument installed this exact version on Linux x86_64.
- Empty-node sing/Xray startup, configuration-preserving update, restart and uninstall passed. The existing experimental service's PID, restart count, executable and configuration hashes were unchanged.
- Temporary installation was cleaned up. No production node was modified. Other platform binaries are compile-verified, not runtime-tested.

Existing configs that explicitly disable the feature remain disabled. Existing configs omitting it now enable it: relays must send PROXY v2, or set AnyTLSProxyProtocol.Enable=false before upgrading. The no-allowlist client address remains an unverified declaration.
