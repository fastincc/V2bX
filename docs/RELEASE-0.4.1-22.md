# fastincc-v0.4.1-22

## Changes since -21

- New TLS private keys and ACME account files are created with mode 0600 on Unix. New private ACME account directories use 0700. Existing files retain their configured access, including intentional group reads. Review warnings for older files with overly broad access.
- When the node configuration request or configuration preflight fails, the node can still apply revocations from a valid, complete user snapshot. New identities and policy changes wait for a successful configuration sync. An invalid user response or a 304 preserves the last applied identities.
- Reliable traffic reporting resumes after transient storage failures. A checkpoint with an uncertain outcome retains the sampled bytes and original user attribution while the node reconciles it. A running node that has received panel acknowledgement can retry local cleanup. After a restart, replay uses the same persisted batch ID.

## Compatibility and operation

Existing configurations, subscriptions, panel interfaces, trusted real IP handling, device limits, rate limits and billing directions remain compatible. This release uses the existing spool format and needs no database migration. Vision adjustable splice and AnyTLS adaptive QUICKACK remain opt-in.

When upgrading or rolling back, use a graceful stop and keep the reliable queue at its existing path. Tests covered both directions between -21 and -22 with an active checkpoint and with a sealed batch awaiting acknowledgement. Uncheckpointed traffic held only in memory can be lost on abrupt termination.

If certificate renewal reports a partial write, verify that the certificate and key on disk form a matching pair before restarting. Both versions can leave the files mismatched if the certificate write succeeds and the key write fails. Repair the pair even if the new certificate is outside the renewal window.

## Verification and performance

The main Go module passed 275 release-tagged race tests including the opt-in cases. Isolated Linux tests covered storage exhaustion, configuration outages, lost acknowledgements, restarts and per-user accounting. Short loopback measurements of saturated bidirectional traffic varied. In a reversed-order 30-second comparison with reliable reporting enabled, candidate upload was 2.5% higher and download 4.4% lower. Fixed 300 Mbps loads per direction were sustained. Public-network throughput was outside this comparison.

The Linux mixed protocol test fixture can sometimes abort when logging a dial error through a mock outbound. The mock is shared with -21 and excluded from release binaries.

Packages are provided for Linux amd64, Linux arm64, Linux armv7 and Windows amd64.
