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

Installer compatibility: the companion V2bX-script change maps armv7l/armv7 explicitly and rejects unknown architectures instead of downloading amd64. s390x users must explicitly select an older release containing their asset; automatic latest installation is rejected. No running service is changed by publishing build or installer updates.
