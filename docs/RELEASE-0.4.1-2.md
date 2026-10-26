# fastincc-v0.4.1-2 publication and installation acceptance

Verified 2026-10-26 (Asia/Shanghai). No production node was changed.

- Release source: `a71a9335fced81963a49ff0337aebf673cf24e59`.
- Installer change: `57cc45d`.
- Release: https://github.com/fastincc/V2bX/releases/tag/fastincc-v0.4.1-2
- Build: https://github.com/fastincc/V2bX/actions/runs/40311000864
- All 25 jobs passed: regression gate, 23 original platform builds, publication.
- 46 public release assets are present. Runtime acceptance is Ubuntu 24.04
  Linux x86_64 only; other architectures are compile-verified, not runtime-tested.
- Linux x86_64 ZIP SHA256:
  `34ee214e294aa0db7f158e0b5fd78fd935acbb7bfc69f9652534e7e2acefa167`.
- Extracted executable SHA256:
  `5ea796ddbc0557e154b9cec7c6d16966d6eba02c7d9ea7870d13305357ed3ee8`.
- Independent download matched published SHA256; ZIP integrity passed.
- Public installation script matched reviewed source byte-for-byte.
- Fresh old-release install then `V2bX update fastincc-v0.4.1-2` passed;
  config.json SHA256 remained unchanged.
- New-release latest-discovery fresh installation passed and reported the exact
  expected version; executable matched the independently checked archive.
- sing and Xray started with empty node configuration; restart, stop, old-binary
  rollback, restoring the new binary, and uninstall passed.
- Malformed digest and checksum mismatch were rejected before service/install
  changes, both with no existing installation and with one already running.
- The separate live experimental service retained its PID, restart count,
  executable hash and configuration hash throughout acceptance.
- Temporary empty-node installation was removed; test backups retained privately.
- Real IP reception and audit remain opt-in. No user/node/API credentials were
  placed into the public package; configuration examples are placeholders.

## Source and sensitive-data review

Runtime source files matched the reviewed local8 implementation: 86 V2bX and
620 core Go files compared, no runtime differences. Tests use synthetic IDs.
The modified core is bundled under third_party/sing-box so checkout builds do
not need a private sibling directory. Original licenses are retained, including
LICENSE.sing-box in release archives. Unrelated upstream docs/standalone fixtures
are excluded. The root dependency pins other than this bundled replacement are
unchanged.

Full-history Gitleaks scan: installer zero findings; five V2bX findings each
reviewed as false positives: two RSA2048 certificate-type enum assignments, two
QUIC HKDF-label enum assignments, one static public OAuth client identifier
byte-identical to original upstream source. No private keys, deployment config,
actual panel credentials, raw experimental logs or private experimental history
were added. Existing upstream whitespace in bundled sources was preserved.

## Remaining boundaries

This is installation acceptance plus the documented local8 real-relay validation,
not long-term production-load certification. Downstream audit ingestion and
non-x86_64 runtime acceptance remain deployment-specific. Docker workflow is
retained but Docker runtime was not validated on this test host. A GitHub-token
created Release does not automatically dispatch the Docker publication workflow.

Back up binaries and config before real-node deployment. Restart disconnects
active sessions. Begin with one low-traffic node; preserve matching old configs
when rolling back. See [AnyTLS options and log contract](ANYTLS.md).
