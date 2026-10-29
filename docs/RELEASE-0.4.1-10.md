# fastincc-v0.4.1-10 — preserve reliable reporting across core selection

## Fixed

Fix an auto-mode regression introduced in -8 and also present in -9. When multiple cores were configured and a node omitted Core (including configurations specifying only CoreName), core-specific option parsing could reset a successful reliable-reporting negotiation. Subsequent requests used legacy reporting and existing pending batches remained queued.

Negotiated state now belongs to the Controller independently of configuration parsing. Queue initialization, checkpoints, sending/replay, reload/shutdown handling and startup status use this state. Existing pending batches retain their IDs and replay after acknowledgement recovery. Previously sent legacy traffic is not synthesized or resent.

## Compatibility

TrafficBatchMode still defaults to auto and accepts auto/on/off and legacy true/false. Configuration format, panel API, queue identity and format are unchanged. No panel patch or database migration is required for this fix. Real-IP, protocol authentication and device-limit implementations are unchanged. The -9 terminal status feature remains available with the companion installer script.

Known limitation: reliable queue locking is still unsupported on Windows. If auto negotiates reliable reporting, Windows node startup can fail. The Windows amd64 asset is retained for compatible use cases; this release does not fix that limitation. Do not silently disable reliability or delete an existing pending queue to bypass it.

## Validation

24 combinations cover single/multiple cores, implicit/named/explicit core selection, and omitted/auto/on/off/true/false modes. Reliable cases verify lost acknowledgement, reopening the pending queue, unchanged batch IDs and acknowledgement cleanup. Local node race tests and release-core-tag tests passed.

Linux amd64 isolated tests ran on both the blue-green host and test VPS: -8 reproduced the defect and the fixed version passed. These tests use synthetic traffic sources and HTTP receivers, not real subscriber proxy traffic or production database reconciliation. CI reruns release regression gates and builds Linux amd64, Linux arm64, Linux armv7 and Windows amd64. Compilation is not runtime acceptance of every platform.

Publication does not deploy running nodes or change any panel or origin host. Earlier tags remain unchanged.
