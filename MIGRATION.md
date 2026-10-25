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
- Build/install/runtime verification is pending until repository write access and CI execution are available. Do not substitute these artifacts into existing production nodes before acceptance.

## Preparation verification

- Bash syntax and git diff whitespace checks passed.
- GitHub Actions actionlint passed; all 23 release-matrix targets resolve to expected asset names.
- Protocol Go sources, Go dependency pins, Dockerfile and licenses match upstream.
- Gitleaks full-history scan: script repository has no findings. V2bX test key removed; the two remaining findings are verified false positives for `certcrypto.RSA2048`, not API credentials.
- Build, publication and installation tests remain pending; no production machines changed.
