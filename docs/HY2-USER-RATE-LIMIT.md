# Independent Hy2 user and node rate limits

Independent Hysteria2 now applies the existing limiter bucket that authentication
previously created but discarded. This is an intentional behavior correction:
nonzero configured limits can reduce throughput that was previously unbounded.
No new configuration, subscription field, panel API, database migration, queue
format, or authentication credential is required. The sing-box Hy2 path is unchanged.

The existing selection rule is preserved: the smaller positive node/user static
limit applies, in decimal Mbps (`1 Mbps = 125000 bytes/s`), with the existing
one-second burst capacity and 10 ms refill quantum. The node setting supplies a
per-user ceiling, not a new aggregate ceiling across all users. A user's bucket
is shared across physical QUIC connections, TCP streams, UDP sessions and upload
plus download. Hy2 `UpMbps` / `DownMbps` congestion-control negotiation is separate
and remains unchanged. Dynamic-limit scheduling is an existing separate issue;
this change does not start that scheduler or promise live dynamic-policy updates.

Unlimited users get no pacing wrapper, context, timer, token operation or new
per-packet lock. The optional provider is checked once per TCP stream / UDP
session. It adds a short identity lookup at stream creation; identity/session
state includes pointers for limited users. Unlimited UDP retains a nil check.
Limited reservations use a cancellable per-user turn, preventing continuous
small packets from starving larger writes and preventing a refill wakeup herd.
Limited writes wait outside authentication, device, node and session-manager
locks. QUIC ACK handling is not paced. There is no unbounded pacing queue or
background worker per user. Limited UDP can experience datagram loss when offered
load exceeds its quota and the existing bounded receive queues fill.

TCP limits include sniffed putback bytes. UDP upload is paced after reassembly;
download reserves quota once before fragmentation, never again on fragment
retries. Metering stays at the existing ingress points and keeps upload/download
orientation, user generation, device lease and actual peer IP unchanged. The initial rate-only repair preserved the pre-existing UDP downstream
fragment-retry double accounting. The subsequent independent accounting repair
uses an internal original/retry marker: it counts the original payload once and
retains zero-byte authorization checks for retries. See
[UDP fragment accounting](HY2-UDP-FRAGMENT-ACCOUNTING.md).

Waits observe stream/connection cancellation and a generation cancellation signal.
User revocation and traffic drain cancel pending waits; drain scans user
identities only on the control path, not while copying packets; resume creates a fresh
cancellation group while keeping the bucket and its remaining quota. User policy
updates use the existing delete/add generation sequence. No future tokens are
reserved, so cancelled waits cannot leave long-lived token debt. Bytes already
read may still be counted even if forwarding later fails, as before.

Acceptance covers real local QUIC TCP/UDP, 1/2/8 Mbps, multiple connections,
node/user minimum selection, unlimited-user isolation, sniffing, cancellation,
revocation, drain/resume, real IP/device admission and snapshot retry/commit.
Baseline comparison must include unthrottled single/multiple streams and the
unchanged AnyTLS path. CPU/RSS measurements include the local client and server;
loopback is not a production/WAN performance guarantee.

Rollback: revert this commit to restore the old behavior (independent Hy2 static
user/node buckets are again not enforced). No data migration or spool deletion is
needed. Existing configured limits must be reviewed before deployment because
this repair makes them effective. The masquerade and receive-window fixes remain
independently revertible.
