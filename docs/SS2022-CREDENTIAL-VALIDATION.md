# SS2022 panel credential validation

Before changing a sing Shadowsocks inbound or its accounting identity map, validate every credential in the incoming addition. AES-128 requires at least 16 bytes and AES-256 at least 32 bytes. A short credential returns an error with numeric user ID, byte length and cipher, never the credential itself. The entire addition is rejected; valid existing identities are not replaced by a partially applied batch.

Valid credentials still use the exact existing first-16/32-byte then Base64 key derivation. There is no padding, hashing or password substitution. Legacy Shadowsocks and all other protocols are unchanged. No configuration, subscription, panel API, database, real-IP, device policy or accounting direction changes.

An invalid mixed addition can defer otherwise valid new users until panel data is corrected. On initial startup, the node may still return a configuration error; this is not a promise that every listener starts with invalid input. Existing controller retry/ETag handling and completed revocations remain effective: a failed addition must not restore deleted users.

Independent local acceptance: three race repetitions cover both SS2022 ciphers, empty/short/boundary/long and multibyte credentials, mixed-batch ordering, no partial identity map, error redaction and corrected-data retry. Live legacy/SS2022 authentication and concurrent user-update tests reconcile each user's upload/download and reject both revoked users and the valid member of a rejected mixed batch. The same short-credential regression probe fails on unmodified -14.

A 10,000-user validation scan took median 5.434 microseconds, 0 B/op and 0 allocs/op on the local Apple M4. This runs during user updates only and adds no per-connection or per-byte operation. Real transport A/B results are recorded separately; microbenchmarks are not network capacity measurements.

Twelve real 10-second SS2022 transport profiles (three alternating pairs for each cipher) all reconciled exact bytes. AES-128 median aggregate throughput 896.32→894.87 MiB/s (-0.16%), AES-256 856.41→863.28 MiB/s (+0.80%). Client+server CPU seconds were 10.151→10.164 and 10.144→10.205; peak RSS did not increase. This local run shows no material throughput decrease, not a WAN or zero-overhead guarantee.
