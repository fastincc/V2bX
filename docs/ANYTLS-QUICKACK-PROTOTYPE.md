# AnyTLS adaptive TCP_QUICKACK prototype

This prototype is **off by default**. It is a best-effort latency hint, not a
guarantee that every delayed ACK will disappear. No panel or database change is
required. This option must be explicitly set in a local sing AnyTLS node:

```json
{
  "Core": "sing",
  "NodeType": "anytls",
  "AnyTLSAdaptiveQuickACK": true
}
```

The fragment illustrates the new field; retain the node's existing API, TLS,
PROXY, audit, rate and reporting configuration. Nested `Options` configuration
also accepts the field. Omission or `false` leaves the destination connection
unwrapped, with no per-read hint overhead. Setting it to false affects newly
created connections after the normal configuration reload/restart lifecycle;
it does not toggle existing connections in place.

## Scope and exclusions

- Linux only, sing AnyTLS logical TCP streams, a direct outbound and a bare
  destination `*net.TCPConn`.
- Does not change the client's TLS/AnyTLS socket, send PROXY headers to targets,
  alter the target's Nagle setting, or change any global TCP sysctl.
- Does not unwrap opaque sockets/tunnels. Other platforms, Xray/Vision, Hy2,
  other inbound protocols, UDP/UoT and TLS fragmentation retain existing paths.
- Startup logs say the prototype was requested and whether the platform is
  supported. They do not claim every route/socket supports it.

## Bounded adaptive hint

A successful destination read of 1–2048 bytes, or a larger read that does not
fill its offered buffer, is eligible. A per-connection
monotonic cooldown allows at most one attempt per 2 ms (roughly 500/s). A busy
cooldown skips the hint immediately; it never waits, sleeps, drops or throttles
payload. Full large reads skip the clock, atomics and syscall. Batch reads inspect
only the last returned buffer's fill level, without scanning payload. No per-connection timer,
goroutine, per-packet log or TCP_INFO polling is added.

Read sizes are a heuristic, not TCP segment sizes. Buffering/GRO and read
coalescing may hide the small segment; using short reads also catches some
coalesced tails larger than 2 KiB. This prototype deliberately does not
force QUICKACK for every bulk read. QUICKACK itself is transient, not a permanent
socket mode. See [Linux tcp(7)](https://man7.org/linux/man-pages/man7/tcp.7.html).

QUICKACK runs through `RawConn.Control` so descriptor ownership is protected
against concurrent close/reuse. Any socket-option or Control failure permanently
disables hints for that connection and leaves ordinary forwarding in place.
Failure does not become a payload error. There is no global aggregate hint
budget; at high connection counts the per-connection costs can add up, which is
one reason this remains opt-in.

## Compatibility and performance acceptance

The wrapper preserves sing's ReadWaiter/vectorised reader, write fast path,
deadlines, cancellation and TCP half-close. Read unwrapping deliberately stops
at the hint so optimized readers cannot silently bypass it. Existing client-side
real-IP/authentication/device admission, revocation, static/dynamic rate control,
counting ownership and reliable reporting are unchanged.

TCP ACK packets are not application payload or traffic-batch acknowledgments.
Application byte accounting does not increase because of this hint, although
NIC packet/byte totals can increase from earlier/more ACKs.

Before enabling beyond isolated tests, compare unchanged baseline, new code with
the option off, and the same code with it on. Include Nagle/NODELAY echo tails,
continuous upload/download/duplex, long connections, CPU seconds/GiB, RSS, actual
syscall counts and packet capture. Separate profiling from throughput tests.
Require actual real-IP/device/revocation/byte-count and dynamic-limit regression
tests. Do not infer WAN behavior or zero overhead from loopback results. Keep
the prototype disabled if representative unlimited throughput regresses.
