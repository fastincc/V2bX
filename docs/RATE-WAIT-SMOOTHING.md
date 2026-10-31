# Shared rate-limit refill and cancellation

Configured Mbps conversion, per-user TCP/UDP and upload/download sharing, and the
one-second bucket capacity remain unchanged. Integer Mbps maps exactly to a
10 ms refill quantum (`bytes_per_second / 100`) instead of a one-second quantum.
Refill is calculated lazily by juju/ratelimit; there is no background refill
worker. Unlimited connections keep their existing path without a rate wrapper.
Independent Hysteria2's native bandwidth/congestion behavior is unchanged.

Limited TCP waits now respond to Close, directional half-close and deadline
changes. Positive byte counts and underlying I/O errors survive cancellation,
so final transferred bytes remain accounted and connection leases can drain.
Xray buffered writes respond to Close/Interrupt and release canceled, unsent
buffers once. The rate wrapper now sits outside the existing statistics writer:
unsent buffers canceled during the rate wait do not increment traffic counters.
Successful forwarding retains the existing direction, UID and accounting units.
The existing downstream writer's error/partial-delivery accounting convention is
not otherwise changed. UDP packet boundaries and existing cancel behavior remain.

No config, subscription, panel API, database or queue format changes are needed.
Source IP extraction, proxy trust, authorization and device admission are unchanged.

## Costs and limits

Only limited connections gain two directional cancellation states and short
mutex-protected checks. Waiting writes allocate timers; faster refill increases
wakeups under load. No claim is made that CPU cost is zero. Canceled pre-send
Xray/UDP reservations are not refunded because the existing bucket API has no
safe reservation cancellation; their bounded debt expires through normal refill.
TCP still charges bytes after actual I/O and retains its existing per-call
buffering/burst behavior. This does not provide per-stream fair scheduling or
promise idle latency under saturated bandwidth.

Local macOS arm64, three alternating -13/candidate trials, 5-second actual
AnyTLS/PROXY-v2/HTTPS204 plus download load: P95 median changed from 974.7 to
244.8 ms at 1 Mbps/one load stream and from 974.5 to 104.5 ms at 10 Mbps/four
load streams. CPU increased by about 0.6 and 2.3 single-core percentage points.
Peak RSS was approximately 32 and 35 MiB respectively, with less than 1 MiB
variation between median variants. These are bounded test-process measurements,
not production-node capacity. A short trial includes the preserved one-second
initial burst; its average wire Mbps must not be mistaken for the sustained cap.

## Validation entry points

Use the normal full build tags and `GOEXPERIMENT=jsonv2`:

- `go test -race ./common/rate ./limiter ./core/xray/app/dispatcher`
- `RATE_HTTPS_DURATION=5s RATE_HTTPS_MBPS=1 RATE_HTTPS_LOAD=1 go test -race -v ./core/sing -run '^TestRateHTTPSProfile$'`
- `CHECKPOINT_PROFILE_DURATION=5s CHECKPOINT_PROFILE_PARALLEL=8 go test -v ./core/sing -run '^TestCheckpointTransportProfile$'`

Coverage includes shared average rates, unchanged burst capacity, partial bytes
with errors, cancellation/deadline changes, half-close, final accounting leases,
Xray manager revocation and unsent-buffer accounting, actual HTTPS responses,
real-IP/device admission, and exact bidirectional wire/accounting reconciliation.
Unmodified -13 fails the fine-refill and canceled-writer negative controls.
