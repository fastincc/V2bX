# AnyTLS PROXY protocol and audit logging

The published fastincc-v0.4.1-2 defaults both features off. Starting with fastincc-v0.4.1-3, this
revision defaults PROXY v2 reception on (3-second timeout, no relay allowlist);
audit also defaults ON starting with fastincc-v0.4.1-4. These settings apply to
sing-core AnyTLS nodes only. Set `"AnyTLSAuditAccess": false` to restore legacy
logging. Explicit log levels are preserved: existing warn/error or disabled
logging suppresses access events; use info level when audit access is desired.
For direct clients or relays without PROXY headers, explicitly set
`"AnyTLSProxyProtocol": {"Enable": false}` before upgrading to that revision.
Omitted, null or empty configuration uses the new defaults. Existing authentication, traffic counters and panel
API formats are unchanged. Back up configuration and binaries before upgrading;
restarting a service disconnects its active sessions. Start with a low-traffic
node and observe real workload before wider rollout.

## Node configuration

Merge these properties into the relevant `Nodes[]` object, not `Cores[]`:

```json
{
  "AnyTLSProxyProtocol": {
    "Enable": true,
    "Version": 2,
    "HeaderTimeoutSeconds": 3,
    "RequireTrustedProxy": false,
    "TrustedProxies": []
  },
  "AnyTLSAuditAccess": true
}
```

Retain your existing panel, TLS, node ID and other settings. In the sing core's
`Log` configuration, set `Level` to `info` to collect access events. Use an
appropriately protected file or the existing service logger; configure rotation
in your logging system. Do not publish your full config or historical logs.

PROXY headers are consumed **before TLS**. A sender such as Nyanpass must send
PROXY v2 to this node. Enabling this listener requires a valid header: ordinary
direct clients without a header cannot connect. Leave it disabled on direct
listeners. Header versions 0 (v1/v2), 1 or 2 are supported. Timeout 0 uses the
3-second default; explicit values may be 1–30 seconds.

With the example above, no relay allowlist is required. The client address is
**declared, not verified**; `proxy_trusted` is false. Do not use that address as
sole evidence for sanctions or shared-account detection. To enforce trusted
relays, set `RequireTrustedProxy: true` and supply relay CIDRs in
`TrustedProxies`. A nonempty list is enforced even if RequireTrustedProxy is
false. Invalid/unsupported source addresses remain rejected in either mode.

Audit logging is independent of PROXY parsing and also supports direct clients.
Disabling audit restores legacy logging, which can include a UUID used as the
AnyTLS password; it does not erase historical logs.

## Audit contract, version 1

Each authenticated logical stream emits at most one `AUDIT_ACCESS` followed by
single-line JSON. An access means an **attempt**, not a successful connection.
IDs are strings; ports are integers; missing fields are null. `user_id` is the
panel database user ID derived from authentication, never a client declaration
or the AnyTLS password. `instance_id` identifies a process; `transport_id` is
unique in that process; `stream_id` increments within a transport.

```json
{"v":1,"event":"access","authenticated":true,"user_id":"1001","instance_id":"example-instance","transport_id":"12","stream_id":"1","network":"tcp","target_host":"example.com","target_port":443,"client_ip":"198.51.100.10","client_port":12345,"relay_ip":"192.0.2.10","relay_port":23456,"client_ip_source":"proxy_protocol","proxy_trusted":false}
```

Direct connections use `client_ip_source: direct`, null relay fields and false
proxy_trusted. IPv6 is stored without brackets. UDP is one association event,
not one event per packet or per destination of a multi-target association.

`AUDIT_ERROR` shares the same correlation/identity/address fields and adds:

| stage | code | meaning |
|---|---|---|
| policy | policy_domain_denied | Existing destination rule rejected stream |
| policy | policy_protocol_denied | Existing protocol rule rejected stream |
| policy | policy_limit_denied | Existing limiter rejected stream |
| protocol | uot_request_invalid | Malformed or truncated UoT request |
| stream | stream_closed_error | Genuine stream failure, without finer cause classification |

`access_emitted` tells whether an access event was emitted before this error.
Malformed UoT can emit an error without any access; network is udp and unknown
target fields are null. Empty ordinary close emits no error. Normal EOF/close/
cancellation is filtered. A stream-shared latch emits only its first error,
including overlapping policy/close callbacks. Arbitrary error text is not copied
into structured records. Existing operational error logs remain available.

Collectors should match node + instance_id + transport_id + stream_id, recognize
markers independently of the logger prefix, accept additive fields, and count
errors separately from access attempts. Deploy parsers supporting both old and
new formats before enabling new audit logs. No credential or payload is intended
in these records; this is not a guarantee for unrelated/debug log paths.

## Validation scope

The local8 source baseline passed isolated authentication/direct/PROXY tests,
policy/UoT error cases, per-stream concurrent deduplication, disabled-mode and
counter regressions. An 8-user 30-second run completed 4288 streams and 4288 access
events. A real relay test verified TCP multiplexing, IPv6 TCP/UDP, policy reject,
connection failure and malformed UoT: 6 access events and 4 associated errors;
normal empty close was not logged. Uploaded/downloaded test payload totals each
matched 131105 bytes in panel accounting. This does not replace long-term
production load testing or downstream audit ingestion acceptance.
