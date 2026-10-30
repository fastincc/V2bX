# Bounded reliable traffic catch-up

Reliable mode retains its existing wire protocol, batch IDs, payloads, ordering,
acknowledgement contract, disk format and configuration. No panel change or
migration is required. Legacy mode and auto/on/off negotiation are unchanged.

A controller owns one cancellable delivery worker. Normal reporting requests have
priority; sealed batches also wake the worker. Catch-up has at most one in-flight
request per node, 500 ms between extra attempts, and a shared 8/s (burst one)
extra-request budget per panel origin in this process. This is not a cluster-wide
limit. Catch-up yields after eight extra attempts or two seconds; an individual
HTTP request retains the configured timeout. Regular requests retain their normal
cadence and do not consume the extra-request budget. Checkpoints and sealing retain
the panel interval; already accepted batches are never merged or renumbered.

The reliable HTTP client shares the existing transport/authentication/source-IP
settings but performs one attempt only. The worker owns exponential retry delay
(2–60 seconds plus bounded jitter) and honors longer valid Retry-After delays on
429/503. All attempts retain the original head ID/payload. A mismatched or missing
acknowledgement does not advance the queue. Local acknowledgement storage failure
pauses delivery until restart. Pending files must not be deleted to clear errors.

Network delivery does not hold the controller operation lock. Shutdown cancels
and joins the sender before closing its spool; ambiguous in-flight requests remain
for deduplicated replay. Reporting callbacks are serialized across interval
changes. Existing online-interface HTTP and core snapshot locking are unchanged.

Device online reporting remains at its regular cadence and keeps its configured
traffic threshold. Its threshold input now comes from new, successfully persisted
increments collected in that reporting window, independently of queue delivery.
Previously it depended on whichever historical head batch was being sent; faster
replay must not let that historical data qualify a currently low-traffic device.
This intentional correction can change which low-traffic devices appear in a
snapshot during backlog recovery. It does not change device admission limits,
real-IP extraction, credentials or billable bytes/directions. Catch-up does not
emit extra online snapshots or consume device events.

Additional memory is bounded by worker state, one in-flight batch copy and a map
of active user totals for the current device-reporting window. Recovery increases
request/fsync frequency temporarily; normal checkpoints are not accelerated.
Recovery requires panel throughput greater than batch production. Existing queue
capacity limits, disk-failure windows, platform-lock support and snapshot-lock
contention remain separate constraints. Rollback to the preceding compatible
version can reuse the same pending files, but restores its slower delivery.
