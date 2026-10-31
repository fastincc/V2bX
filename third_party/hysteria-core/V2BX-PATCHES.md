# Local patch provenance

Base: github.com/apernet/hysteria/core/v2 v2.6.4, copied from the Go module cache with upstream LICENSE.md and tests. No dependency version upgrade.

Connection shutdown seals worker admission, closes QUIC, and waits for TCP handlers and the UDP manager before emitting LogOnlineState(false). The early Disconnect event remains available. TCP's metered copy preserves first-direction-exit shutdown, unblocks both directions, and joins the second accounting callback before UntraceStream. A write deadline unblocks pending QUIC writes without resetting already queued data. UDP shutdown joins its idle cleanup loop and receive goroutines. No per-packet lock or buffer is added.

The V2bX hook retains the accounting handle between Disconnect and the offline drain barrier. Authentication, revocation, per-connection denial and accounting generation checks still apply to all callbacks.

Changed upstream production files: server/server.go, server/copy.go, server/udp.go. Local real QUIC tests cover delayed TCP and both UDP callback directions, exact accounting, and reclaimed handles. Upstream fixtures are synthetic, not deployment credentials.

Device lease integration adds optional StreamLeaseLogger in server/config.go. TCP acquires after request admission and releases on handler completion; UDP acquires on session creation and releases through its existing single-close path, including failed dial and idle timeout. Implementations without this optional interface retain upstream behavior. Packet and byte-counting paths are unchanged by device pinning.

Sniffed TCP putback is metered once before target writing using the existing ingress-byte convention. Denied callbacks and short/failed writes follow normal cleanup. No additional work is added to subsequent copy iterations; sniff timing and routing are unchanged.

Optional TrafficPacerProvider / TrafficPacer adds cancellable per-user pacing at
TCP writes (including sniffed putback) and UDP logical-message boundaries.
Unlimited streams keep the original copy functions; limited UDP reserves once
before download fragmentation and once after upload reassembly. There is no
new unbounded queue, per-packet authentication lock for unlimited traffic, or
QUIC ACK delay. Stream/connection cancellation unblocks pending writes before
the existing drain join. Additional production file: server/pacer.go. See
../../docs/HY2-USER-RATE-LIMIT.md for shared-bucket semantics, acceptance scope
and the pre-existing UDP fragment-retry accounting limitation.
