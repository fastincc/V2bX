# AnyTLS batch user updates

`AddUsers` now collects changed authentication identities, then scans the active
session table once. Previously every newly added or password-changed user caused
a full scan while holding `usersMu`. Complexity of this part changes from
O(changed users × sessions) to O(changed users + sessions).

The existing immutable authentication service and epoch snapshots remain in use.
Password changes and re-added identities invalidate old sessions; unchanged
passwords and audit-only updates preserve them. Failed publication leaves the
active service, users, epochs, audit mapping and connections unchanged. Target
connections close after releasing the users lock and at most once per update.
No configuration, subscription, panel API, device, real-IP or accounting format
changes are required. No additional work is added to normal stream reads/writes.

Validation entry points (use the normal full build tags):

- `go test -race github.com/sagernet/sing-box/protocol/anytls -run TestUserBatch`
- `go test -race ./core/sing -run TestAnyTLSBatchUpdateWithActiveTraffic`
- `ANYTLS_TABLE_PROFILE=1 go test -v github.com/sagernet/sing-box/protocol/anytls -run TestAnyTLSAddSessionScanProfile`

Local macOS arm64 comparison with -13, three alternating runs, 5,000 added users
and 10,000 synthetic existing sessions: update elapsed time fell from
273.6–276.1 ms to 1.40–1.65 ms. CPU fell from 275–278 ms to 3.3–4.6 ms.
Temporary allocations rose by approximately 224 KiB due to the changed-name set;
there is no persistent new index. These are session-table measurements, not WAN
throughput claims. Three race-enabled real AnyTLS trials each added 5,000 users
during 160 streams and reconciled 655,360 bytes in each direction, including
unchanged real IP and audit attribution.
