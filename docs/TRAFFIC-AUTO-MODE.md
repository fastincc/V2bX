# Automatic reliable traffic negotiation

This change is not part of the published fastincc-v0.4.1-7 binaries.

`TrafficBatchMode` accepts `"auto"` (the new default when omitted), `"on"`, or `"off"`. Existing `true` maps to `on`; existing `false` maps to `off`. An explicit false is never overridden. Both supported node configuration layouts retain this behavior.

At startup, after obtaining authenticated node information:

- `off`: no capability request; use legacy reporting unless persistent reliable state exists, in which case refuse unsafe downgrade.
- `on`: require enabled durable batch v1 with permanent deduplication receipts.
- `auto`: enabled compatible capabilities select reliable reporting. A complete disabled v1 response or HTTP 404/405 selects legacy reporting only when this node has no persistent reliable state. Authentication failures, timeouts, 5xx, malformed responses, unknown versions or incompatible enabled contracts fail startup; they are not treated as permission to downgrade.

Negotiation happens at startup only. Enabling a node on the panel whitelist takes effect on its next restart. No runtime switching or automatic fallback is added. Reliable delivery does not require fractional remainder accounting on the panel.

When TrafficSpoolDirectory is omitted, the default is `V2bX/traffic` under Go's os.UserConfigDir (Linux: $XDG_CONFIG_HOME, otherwise $HOME/.config; Windows: %AppData%; macOS: $HOME/Library/Application Support). This is independent of the working directory. For services, explicitly configure a persistent TrafficSpoolDirectory and keep the service account/environment unchanged. The existing per-panel/protocol/node hash isolates each node under this root. Do not change that path, API identity, service account or delete state to bypass a refusal: that can strand unacknowledged traffic.

Before any proxy listener is opened, the selected core must support traffic snapshots and the journal must open successfully. Private directory, locking, corruption, limits and durable-write checks still apply. Failure stops startup; no silent fallback. The existing durable pending.json v2 marker remains even when all batches have been acknowledged, pinning reliable mode across restarts. Explicitly disabling requires a separately reviewed drain/migration, not just an empty queue.

Successful startup logs `Reliable traffic reporting enabled` or `Reliable traffic reporting disabled; using legacy reporting`, including `mode` and `reliable_reporting`. Disabled logs distinguish explicit configuration from automatic legacy selection. Negotiation failures log a sanitized reason. No token, credential-bearing URL or user data is logged.

Compatibility change: an omitted setting now probes the panel. A legacy panel returning 500 rather than 404/405 for a missing route will fail automatic negotiation; use explicit `off` until its capability endpoint is available. A panel enabling this node can now cause automatic reliable startup, including required persistent storage. Existing startup failures unrelated to traffic mode remain unchanged.

Validation uses local HTTP fixtures and persistence/restart tests. It does not constitute blue-green or VPS deployment acceptance. Existing -7 Release assets and running services are not modified.

## Local validation (2026-10-29)

Using Go 1.25.0 and GOEXPERIMENT=jsonv2:

- `go test -race ./conf ./api/panel ./common/trafficbatch ./node -run 'Test(Traffic|Automatic|Batch|Queue|Conf_Load)' -count=1 -timeout=120s` passed, including config round trips, startup log fields, explicit absence versus errors, persistent pinning, corrupt state, and auto-negotiated lost-ack/restart replay.
- `go test -race ./api/panel ./common/trafficbatch ./node -count=1 -timeout=180s` passed.
- `go test -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./node -run 'Test(Automatic|Batch|Device)' -count=1 -timeout=180s` passed.
- The first broad conf invocation included the pre-existing permanently blocking TestConf_Watch; that invocation was stopped, not counted as a pass. The bounded configuration selection above excludes it.

No blue-green/VPS tests, deployment, GitHub push or release were performed for this change.
