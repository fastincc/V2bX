# Upgrade and restart reporting summary

Upgrade (install.sh with an existing configuration) and restart (V2bX.sh) print reliable reporting state per node. They read only dedicated V2BX_REPORTING_STATUS_V1 records from the current systemd InvocationID, and reject the previous invocation, an inactive service, or an invocation change during reading. They poll at most five times with four one-second waits. Journal command latency is additional.

Each startup/reload begins a V2BX_REPORTING_RESET_V1 generation and registers every configured node as unknown before individual startup. Only successful node startup confirms enabled/disabled. Closing a node marks it unknown. A reset discards the previous generation. Protocol aliases and case are normalized. Records expose a hashed panel/node binding, numeric node ID, protocol, state and mode; no host, token, custom node name, user or traffic data. Output uses a fixed safe format, independently of the application's log level or output file.

Mixed results remain per-node: an enabled node does not imply others succeeded. Unreadable logs, missing complete generation, older binaries without dedicated records (including published -8), startup still pending, failed restart, or OpenRC/Alpine yield 状态未确认. No inference is made from configuration or old info logs. Only the latest 2,000 journal entries are inspected; if the reset marker has fallen out of the window, the result is unknown.

This requires the companion node status-output change for confirmed results. Updating only scripts with an old binary safely prints unknown. It does not alter traffic mode, configuration, queues or services beyond the user's existing upgrade/restart action. No status helper is downloaded or executed separately.

Validation: bash syntax checks and tests/reporting-summary.py (five parser scenarios and eight mocked service scenarios). These tests do not execute installation, systemctl or network operations. No real VPS/systemd acceptance has been performed for this change.
