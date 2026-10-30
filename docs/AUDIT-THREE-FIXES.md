# Three audit fixes after -11

## 1. Shadowsocks authentication snapshots

Pinned sing-shadowsocks v0.2.8 is retained under third_party with its license. AEAD and AEAD2022 multi-user tables are built before atomic publication; each handshake/datagram uses a consistent snapshot. Failed validation retains the previous table. The sing-box adapter carries a stable, non-recycled identity instead of indexing a mutable user slice. User changes are serialized and post-authentication admission checks reject retired generations. Unchanged users keep their identity; repeated adds update instead of duplicating named users.

No wire, password, subscription, panel API, real-IP, device-limit, accounting-direction or spool-format changes. Peak memory during updates includes old and new authentication tables; ordinary authentication adds a snapshot load and short identity check, without a lock held during network reads or routing.

Validation: concurrent AEAD/AEAD2022 table/packet tests under race (3 rounds); retained/revoked/re-added identity and invalid-update tests; real SS and SS2022 AES-128/AES-256 TCP exchanges under user churn with exact per-user accounting; existing SS UDP device/rate/accounting test. Linux blue-green isolated tests passed 10 snapshot rounds and 3 proxy rounds. Test VPS verification is pending connection recovery. A combined macOS integration run observed a separate sing-tun default-interface monitor race after successful protocol assertions; preserved in the internal evidence and not represented as a passing full race gate. No production deployment or release.
