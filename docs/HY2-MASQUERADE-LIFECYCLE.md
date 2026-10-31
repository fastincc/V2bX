# Independent Hy2 masquerade lifecycle

Optional TCP HTTP/HTTPS masquerade listeners are now owned by their node. All configured ports bind synchronously; partial binding or subsequent QUIC construction failure releases every newly acquired resource. Serving starts only after construction succeeds. Delete, core close and rebuild close listeners, HTTP connections and hijacked connections; an individual listener error no longer calls process-wide Fatal. First-start errors still follow the existing controller startup policy.

Configuration, certificate reload, masquerade content/proxying, redirect and Alt-Svc behavior are preserved. Shutdown intentionally disconnects this node's masquerade connections, including upgraded connections. Proxy QUIC streams still use the existing drain/accounting and device lifecycle. No packet-copy hook, authentication policy, panel API or queue format changes.

Validation covers repeated actual node replacement, other-node QUIC traffic with exact accounting, bind conflicts, partial bind rollback, QUIC-construction failure, HTTP/TLS/proxy responses, redirects, idle/hijacked connections and concurrent idempotent close. Local tests are not production deployment validation.
