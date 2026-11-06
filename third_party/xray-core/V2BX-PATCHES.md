# V2bX local Xray patch provenance

Baseline copied byte-for-byte from `github.com/wyx2685/xray-core@v0.0.0-20251202200223-63db1dc9e9e2`, the pre-existing replacement for `github.com/xtls/xray-core v1.251202.0`. No upstream version upgrade. Original MPL-2.0 LICENSE and source retained.

Local runtime patches:
- `common/session/session.go`: shared atomic inbound splice state for the Linux Vision prototype and the ordinary unlimited accounted Vision path, initialized before dispatch.
- `proxy/proxy.go`: optional RawSpliceProvider (prototype) and RawSpliceIngressProvider (ordinary ingress accounting) hooks; both wait for downlink writer readiness. Writers without either provider retain their ReadFrom/readV path.

The actual cancellable pumps, quotas, accounting and lifecycle live in V2bX `common/visionsplice` and `core/xray/app/dispatcher/vision.go`. The ordinary path has no quota callbacks and preserves write-before-forward user accounting. See `docs/VISION-SPLICE-PROTOTYPE.md` and `docs/VISION-SPLICE-BASELINE.md` in the parent project. Do not silently merge unrelated upstream changes into this pinned copy.
