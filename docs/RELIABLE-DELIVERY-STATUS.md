# Reliable delivery completion status

The sender distinguishes idle, deferred, paused, canceled, failed, remote_confirmed_local_pending and confirmed. Only confirmed returns rows eligible for the controller's successful-report log: the panel's matching acknowledgement and the existing local acknowledgement operation must both succeed.

After a matching panel acknowledgement, a failed local acknowledgement yields `remote_confirmed_local_pending`. A transient storage failure triggers a local retry with backoff. The running sender keeps the confirmed ID and sends no additional request for that head. A closed spool or a conflicting head pauses the sender. Deferred, paused and canceled messages are emitted on state transitions and omit credentials and payloads.

Count only `confirmed` as completed delivery. See [reliable traffic catch-up](RELIABLE-TRAFFIC-CATCHUP.md) for restart and replay behavior.
