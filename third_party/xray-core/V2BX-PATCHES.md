# V2bX local Xray patch provenance

Baseline copied byte-for-byte from `github.com/wyx2685/xray-core@v0.0.0-20251202200223-63db1dc9e9e2`, the pre-existing replacement for `github.com/xtls/xray-core v1.251202.0`. No upstream version upgrade. Original MPL-2.0 LICENSE and source retained.

Local runtime patches:
- `common/session/session.go`: optional shared atomic inbound splice state, initialized only by the Linux Vision prototype before dispatch.
- `proxy/proxy.go`: optional RawSpliceProvider hook; prototype waits for downlink writer readiness. Ordinary writers retain their ReadFrom/readV path and original state behavior.

The actual cancellable pump, quotas, accounting and lifecycle live in V2bX `common/visionsplice` and `core/xray/app/dispatcher/vision.go`. See `docs/VISION-SPLICE-PROTOTYPE.md` in the parent project. Do not silently merge unrelated upstream changes into this pinned copy.
