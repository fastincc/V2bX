# Release build matrix

Tag releases, pull requests and default manual builds target:

| Platform | Release archive |
| --- | --- |
| Linux amd64 | V2bX-linux-64.zip |
| Linux arm64 | V2bX-linux-arm64-v8a.zip |
| Linux armv7 | V2bX-linux-arm32-v7a.zip |
| Windows amd64 | V2bX-windows-64.zip |

All existing regression gates remain prerequisites. Each archive retains its .dgst companion. Existing fastincc-v0.4.1-7 assets are unchanged; this policy applies to subsequent builds.

For the historical 23-platform matrix, manually run Build and Release on a branch with full_matrix=true. This produces workflow artifacts, not a new Release. The default is false.

Installer compatibility: the current separate V2bX-script installer recognizes amd64/arm64/s390x and falls back to amd64 for unknown architectures. Before publishing the next four-platform release, update its architecture handling: map armv7 explicitly, reject unsupported latest-release targets with a clear message, and preserve explicit installation of older releases where assets exist. Do not imply that availability of an armv7 ZIP makes the existing installer armv7-compatible. No installer or running service is changed by this matrix-only commit.
