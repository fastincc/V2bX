# Reliable delivery completion status

The sender distinguishes idle, deferred, paused, canceled, failed, remote_confirmed_local_pending and confirmed. Only confirmed returns rows eligible for the controller's successful-report log: the panel's matching acknowledgement and the existing local acknowledgement operation must both succeed.

A remote confirmation followed by a local storage error is explicitly logged as such; the existing queue retention and pause-until-restart behavior is unchanged. Deferred/paused/canceled messages are emitted on state transitions rather than every reporting tick. They never include payloads or credentials. Existing success text remains, but no longer describes a skipped attempt.

This changes observation only: no request format, batch ID, replay, pacing, fsync, device-window accounting or forwarding-path changes. Monitoring that relied on misleading periodic success entries should instead distinguish confirmed delivery from pending state.
