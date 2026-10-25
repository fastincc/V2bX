# Independent repository migration

Prepared 2026-10-26 (Asia/Shanghai). No production deployment.

- Source: https://github.com/wyx2685/V2bX
- Destination: https://github.com/fastincc/V2bX
- Original authors, license and protocol behavior retained.
- Default distribution branch: main. Historical branches/tags are provenance snapshots, not the supported installation entry point.
- Installation and update endpoints use fastincc repositories; installation telemetry to the upstream maintainer is disabled.
- No panel configuration, node API keys, SSH keys or business credentials are included.
- V2bX history removes upstream test_data/1.key and test_data/1.pem; affected historical commit/tag hashes therefore differ from upstream. The remaining source history is retained.
- V2bX main derives from upstream dev_new commit 71277de69efbbc86c23ad8ae02b68efd174e5756, not the older v0.4.0 release commit. No old binary is relabeled as a new build.
- Existing Go module paths and pinned dependencies are retained. This migration does not claim to independently host all dependency repositories.
- Historical tags do not imply historical Release assets are available here.
- Release builds: push a new fastincc-v* tag on the prepared main branch. All original build-matrix jobs must pass before the archive set is published.
- Docker images can be built with the Publish Docker image workflow. Releases created by GITHUB_TOKEN do not automatically trigger other workflows; dispatch the Docker workflow separately if needed.
- Installation acceptance was completed on an isolated Ubuntu 24.04 x86_64 VPS; see results below. No production nodes were changed.

## Preparation verification

- Bash syntax and git diff whitespace checks passed.
- GitHub Actions actionlint passed; all 23 release-matrix targets resolve to expected asset names.
- Protocol Go sources, Go dependency pins, Dockerfile and licenses match upstream.
- Gitleaks full-history scan: script repository has no findings. V2bX test key removed; the two remaining findings are verified false positives for `certcrypto.RSA2048`, not API credentials.
- No production machines changed. Docker runtime, non-x86_64 runtime and live panel/protocol traffic acceptance remain outside this migration test.

## Publication and installation acceptance — 2026-10-26 (Asia/Shanghai)

- Public, independent repositories: fastincc/V2bX and fastincc/V2bX-script; default branch main.
- Cleaned historical branches/tags were explicitly authorized for public publication. All 81 initial V2bX refs and 2 script refs matched the reviewed local copies after upload.
- Release: [fastincc-v0.4.1-1](https://github.com/fastincc/V2bX/releases/tag/fastincc-v0.4.1-1).
- [Build run](https://github.com/fastincc/V2bX/actions/runs/40214600175): all 23 platform builds and the publication job passed; 46 release assets.
- Linux x86_64 archive SHA256: `8496346718a565aa0067ff3b96334011a988c583b8892651e1dc3d1530b4d67c`; matched published digest, ZIP integrity passed.
- Downloaded public installation script matched reviewed script byte-for-byte.
- Fresh installation through latest-release discovery passed; executable version was fastincc-v0.4.1-1.
- sing and Xray started together with an isolated empty-node configuration; no production API or credentials used.
- `V2bX update fastincc-v0.4.1-1` passed and preserved config.json SHA256; service remained active.
- Restart, stop and confirmed uninstall passed; temporary service, binary directory and test configuration were removed.
- This preserves upstream implementation; it does not claim current security patch coverage or end-to-end acceptance of every protocol. Proxy Protocol/AnyTLS real-IP adaptation is not included.
- Upstream dependency hosts and external BBR/documentation links remain external; they have not been mirrored or executed in this test.
