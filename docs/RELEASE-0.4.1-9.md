# fastincc-v0.4.1-9 — per-node reporting status after upgrade and restart

## Changes

- Emit dedicated per-node reporting status independently of Log.Level and Log.Output. Even warn/error logging or file logging no longer hides the service-manager status record.
- Register every configured node as unknown before startup; confirm enabled/disabled only after successful startup. Clear the status generation on reload and mark closing nodes unknown.
- Normalize protocol aliases and expose only node ID, protocol, mode, state and a hashed binding. No panel URL, token, custom node name or user traffic is emitted in these records.
- The companion V2bX-script update prints reliable reporting enabled/disabled/unknown after upgrade and restart, using only the new systemd invocation. It rejects old invocations, inactive services and restarts that occur during reading.
- Fix the installer's service check result being overwritten by an echo command.

## Companion script and compatibility

Use the updated V2bX-script main (commit 329fffe or its descendants) for the terminal summary. Installing or upgrading through the updated installer refreshes the management script. Replacing only the binary does not update an already-installed management script.

Missing dedicated records (including -8 and older binaries), unreadable/truncated journal history, incomplete startup and OpenRC/Alpine produce 状态未确认, not a claim that reporting is disabled. Polling includes up to four one-second waits; command latency is additional. Partial results remain per-node. This summary is a startup-state snapshot, not continuous panel health monitoring or proof that billing has completed.

Automatic reliable negotiation remains unchanged from -8: omitted TrafficBatchMode defaults to auto, explicit false/off stays disabled, and persistent reliable state prevents automatic downgrade. Panel support and per-node enablement are still required; fractional remainder accounting is not required. No running nodes or panel environments are modified by publication.

## Validation and platforms

Local complete node race tests, dedicated status/protocol alias tests, shell syntax checks, five parser scenarios, eight mocked service scenarios and the actual restart wrapper test passed. Publication also requires the existing core/device/AnyTLS gates plus reporting/automatic-mode regressions and builds for Linux amd64, Linux arm64, Linux armv7 and Windows amd64.

No live VPS/systemd acceptance was performed for this change. Cross-platform build verification is not runtime acceptance on every platform. Existing -7/-8 releases remain unchanged.

See REPORTING-STATUS.md and TRAFFIC-AUTO-MODE.md. Historical candidate notes in the archive describe an earlier release stage.
