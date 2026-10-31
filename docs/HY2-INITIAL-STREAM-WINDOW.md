# Independent Hy2 initial stream receive window

A nonzero quic.initStreamReceiveWindow now populates InitialStreamReceiveWindow, instead of mistakenly assigning a connection-window field. Unspecified/default values, maximum and connection windows, existing validation, congestion control and other settings are unchanged. No new rejection rule for existing initial/maximum combinations is introduced.

Only explicitly customized configurations change behavior: small windows can constrain throughput; larger windows permit more buffered data and may increase memory demand. This is not an automatic tuning change. Configuration, subscriptions, real IP, admission, accounting and panel interfaces need no migration.

Tests cover defaults, the existing lower bound, independent initial/maximum/connection mapping and real QUIC transfer with 16 KiB and 16 MiB initial windows. Local loopback results do not establish performance on WAN/high RTT paths.
