# Local transport patch

Source: github.com/anytls/sing-anytls v0.0.11, copied from the pinned module cache.
Upstream LICENSE retained. No wire protocol or authentication encoding changes.

- session/stream.go: synchronize the close error with concurrent Read/Write.
  Revocation closes an active transport while its streams may be writing.
- The V2bX AnyTLS inbound publishes new immutable Service instances when users
  change; it does not call the upstream Service.UpdateUsers concurrently.
