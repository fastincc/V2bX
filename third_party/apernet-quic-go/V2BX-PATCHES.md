# Local patch provenance

Base: github.com/apernet/quic-go v0.54.1-0.20250907230547-eb32f8aec5e2, copied from the verified Go module cache. Preserve upstream LICENSE and source. No dependency version upgrade.

The sent packet handler publishes custom congestion controllers under congestionMutex. Two extended-event type assertions read the interface without that mutex and raced with SetCongestionControl during Hysteria2 authentication. ACK handling now reuses its existing locked snapshot; timed loss handling reuses the snapshot returned by detectLostPackets. No new packet-path lock or goroutine is added.

Only internal/ackhandler/sent_packet_handler.go is changed from the pinned upstream production source. Added tests cover controller replacement during ACK and timed-loss callbacks; the V2bX real QUIC race regression covers authentication. No protocol, configuration, real-IP or billing schema changes.

The upstream testdata private keys are public synthetic test fixtures retained byte-for-byte, never deployment credentials. Production builds do not embed these fixture files.
