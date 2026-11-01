# Independent Hy2 UDP downstream fragment accounting

Previously a downstream logical datagram was accounted before its first send.
When QUIC rejected the whole datagram as too large, its fragment retries each
accounted the payload again. A successfully fragmented datagram could therefore
charge twice its application payload. Partial failures could also overcount.

The internal send call now explicitly carries whether this is the original
logical datagram or a retry. Only the original attempt contributes payload bytes
and reserves rate quota. Every retry still calls the existing traffic logger
with zero upload/download bytes, preserving user revocation, accounting generation,
node pause and admission-denial checks. A rejected callback closes the transport
and stops subsequent fragment attempts. The flag is supplied only by the server's
fragmentation path; it is not a client-controlled field or a persistent dedup key.

Metering remains ingress-based: the bytes already read from the target count once
even if sending later fails or only some fragments are sent. This is not a switch
to client-acknowledged delivery accounting. A nil QUIC SendDatagram result is not
proof of delivery. Original packet IDs may repeat without suppressing later accounting.
This does not fix the existing reassembly ambiguity when consecutive fragmented
datagrams reuse the same PacketID and fragment count. That behavior is present
in both the previous commit and this repair; it needs separate investigation.

No changes to actual peer IP, authentication, audit fields, device admission or
lease release, upload counting, TCP, AnyTLS, Xray or sing-box Hy2 forwarding.
The existing per-user shared rate bucket, burst/refill policy, cancellable fair
waiting and node/user minimum selection remain. Unlimited traffic gets no new
rate wait, queue, timer, allocation or lock. An internal boolean controls existing
logging and pacing; ordinary UDP still logs once. MTU, fragment sizes, QUIC
congestion control, windows and protocol buffers are unchanged.

No configuration, subscription, panel API, database migration or reliable batch
format changes. Newly collected fragmented download counts become smaller and
correct. Already persisted/reported historical batches are not rewritten; retries
retain their original immutable payload. Reverting this repair restores the old
fragment overcount but requires no data migration or spool deletion.

Acceptance uses a fixed previous-commit archive as the negative control. It
covers real local QUIC small/fragmented/mixed messages, IPv6 destinations, multiple
connections, actual source IP/device rejection, 1/2/8 Mbps shared quotas, failed
and successful checkpoints, cancellation, revocation before/after a fragment,
serialization/boundary cases and real closed-queue send errors. Performance A/B
must use identical fixtures, separate processes, alternating versions and no
concurrent compilation/load; record CPU/RSS and retain all anomalous samples.
Local validation does not establish WAN/production or platform-runtime behavior.
