# Pending panel integration: reliable traffic deployment

This is an interface request, not an implemented or deployed panel change.
Existing legacy configurations remain readable and log a warning. They cannot
acquire exactly-once retry semantics without panel deduplication. Batch mode now
requires an authenticated capability check before starting an inbound.

GET `/api/v1/server/UniProxy/traffic_capabilities` uses the existing token,
node_type and node_id authentication and authorization. Successful response:

```json
{"data":{"version":1,"enabled":true,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}
```

Zero retention means permanent deduplication tombstones, not zero retention.
Only advertise this for a node permitted to use durable batch push. A receipt
must be durable before HTTP 200, including during queue failure. Replay with the
same ID and canonical payload returns the same acknowledgement without charging
again; a changed payload returns 409. Preserve receipt identity/digest after
payload cleanup. Pending-job recovery and monitoring must be operational.
A finite receipt lifetime needs a future explicit retirement/expiry protocol;
this client rejects it rather than replaying potentially expired receipts.

The existing POST body and X-Traffic-Batch-ID acknowledgement contract remain
unchanged. Capability advertisement must not silently enable global billing
flags, alter rates, authentication, device limits or subscriptions.

Installer/rollout requirements: explicitly select batch mode only after this
node-specific preflight succeeds; use a private persistent spool outside the
replaceable program directory, preserve it across upgrades and uninstall backup,
and drain before reverting to legacy. Keeping a legacy configuration preserves
its old reliability limits. Never send an ambiguous failed legacy request through
batch mode afterwards: it may already have been billed.

Tests may advertise capabilities through an isolated adapter while calling real
panel billing code. Such tests validate the proposed contract, not deployment of
this new endpoint in the panel application. Coordinate implementation with the
panel maintainer before enabling batch mode outside the fixture.
