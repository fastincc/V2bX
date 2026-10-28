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

## Continuous node checkpointing (local implementation)

Batch mode checkpoints all positive byte counts every `TrafficCheckpointSeconds`
(default 1, range 1..60). A mutable `active.json` is never transmitted. It is
sealed into an immutable batch at the reporting cadence; an outstanding older
batch does not block checkpointing or sealing. A slow network acknowledgement
holds no checkpoint lifecycle lock. Current report request rate stays bounded by
the configured reporting task; the backlog therefore drains gradually.

`TrafficSpoolMaxBytes` defaults to 64 MiB and `TrafficSpoolMaxBatches` to 4096.
Capacity includes durable payloads, not filesystem metadata or temporary atomic
replacement space. Provision additional free space for replacements. At capacity,
existing batches can still drain; unsaved increments remain in memory and an
error is logged. Storage failure does not silently evict batches or stop service.
This preserves availability but does NOT guarantee durable counting during a full
or failed disk. An ambiguous write poisons the store until restart.

Checkpointing does not send every checkpoint as a separate charge. It aggregates
within the reporting window. Panel fractional-rate rounding and rate changes
still require explicit regression; billing uses the panel's existing receipt-time
rate, not a newly invented collection-time rate.

The v2 local queue migrates legacy pending files without changing their batch ID
or payload. A version marker makes the older single-batch binary refuse the spool.
Do not remove that marker to bypass rollback checks; export/drain and preserve a
backup first. Abrupt termination can lose bytes since the last successful
checkpoint. Normal teardown attempts a final checkpoint and seal; asynchronous
protocol-specific close tails must also be accounted for by lifecycle tracking.
