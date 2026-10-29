# fastincc-v0.4.1-8 — automatic reliable traffic negotiation

## Behavior change: auto is the default

When TrafficBatchMode is omitted, the node now negotiates reliable reporting at startup using the authenticated traffic_capabilities endpoint. Set it to "auto", "on" or "off". Existing true/false values remain compatible: explicit false still disables reliable reporting.

An enabled durable batch v1 contract with permanent deduplication receipts selects reliable reporting. A complete disabled response or HTTP 404/405 selects legacy reporting only if this node has no persistent reliable state. Timeouts, authentication errors, 5xx, malformed or incompatible responses fail startup; there is no silent fallback. Legacy panels that return 500 for an absent endpoint require explicit "off" until compatible.

The existing durable journal marker pins reliable mode across restarts, even after the queue is empty. Panel disablement cannot silently downgrade a previously reliable node. Existing journals must be drained and deliberately migrated before disabling or changing the state directory. Negotiation occurs only at startup, not during active connections.

Successful startup logs whether reliable reporting is enabled, the configured mode and, when disabled, the reason. Failure reasons omit credentials and request URLs.

## Storage and panel compatibility

An omitted TrafficSpoolDirectory defaults to V2bX/traffic under the service account's os.UserConfigDir. For services, explicitly set a persistent directory and keep the account/environment unchanged. Snapshot support, directory permissions, locking and durable storage are checked before proxy listeners open. Storage failure prevents startup.

The panel must provide authenticated capabilities, durable receipts, batch deduplication and compatible workers, with the intended node enabled. Fractional remainder accounting is NOT required. Mixed legacy/reliable nodes remain supported. This node release does not deploy panel patches or modify any running service.

## Platforms

Default release assets: Linux amd64, Linux arm64, Linux armv7 and Windows amd64, each with a .dgst file. Existing -7 assets for other platforms remain available. The historical 23-platform matrix remains available as manual workflow artifacts.

## Validation and limits

Local tests passed for configuration compatibility, capability errors, startup logs, persistent pinning, node isolation, corrupt storage and automatic negotiation followed by outage/lost-ack/restart replay. Interface, journal and node race regressions and production-core-tag checks passed. Publication is gated on these regressions plus the existing core, device and AnyTLS gates and all four platform builds.

No blue-green/VPS deployment or new live proxy acceptance was performed for this change. Cross-compilation does not establish runtime acceptance on every platform. All -7 repairs remain included; legacy HTTP retry ambiguity, uncheckpointed traffic at abrupt termination, persistence failures and non-global device admission retain their documented boundaries.

See TRAFFIC-AUTO-MODE.md for configuration and migration details. Historical BUILD-CANDIDATE.md describes the earlier -7 candidate; this release does not require fractional-carry panel changes.
