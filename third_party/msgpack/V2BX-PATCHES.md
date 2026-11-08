# Local patches to msgpack v5.4.1

This is the existing pinned `github.com/vmihailenco/msgpack/v5 v5.4.1`, not a
dependency upgrade. Go module and go.mod checksums plus the original SHA256 of
every copied file are recorded in `V2BX-UPSTREAM-MANIFEST.json`. Go sources,
original Go tests/examples, module files, LICENSE and upstream documentation are
preserved. Upstream CI and JavaScript release metadata are omitted.

The local change checks uint32 length-decoding sites (map, array,
string/binary, optional interned string/binary and extension) before converting to the platform int. On 32-bit
targets an unchecked conversion could silently skip an incomplete value or
panic. Extension skipping reads its type byte separately instead of computing
`payload length + 1` in an int. This also covers nested unknown fields and the
raw-value recording used by custom MessagePack unmarshallers.

Valid wire data, encoding, public APIs and ordinary narrow-length decode paths
are unchanged. No normal payload is parsed twice. V2bX still maps parser errors
to its fixed, credential-free user-snapshot error and preserves the last valid
authorization on failure. The package's error does not make malformed traffic
an authoritative empty snapshot.

Additional tests cover length boundaries and complete user-response envelopes;
32-bit runtime execution is required to verify the original failure, since a
64-bit-only pass cannot establish this fix.
