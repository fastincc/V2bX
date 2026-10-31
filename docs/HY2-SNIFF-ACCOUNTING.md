# Independent Hysteria2 sniff accounting

Sniffed TCP putback is charged once to the admitted connection's original accounting generation before writing it to the target. This matches the existing ingress-byte convention; a short or failed target write does not cause a second charge. A denied accounting callback prevents forwarding and follows the existing disconnect/lease cleanup path. Failed sniffing and failed target dialing keep the existing failed-request behavior.

Configuration, authentication, source IP, device limits, directional counters, batch format and sniff timeouts are unchanged. The intentional billing difference is that previously omitted sniffed bytes are now included. Sniff-disabled streams and subsequent copy iterations have no new work. Sniff-enabled streams add one accounting callback, without another payload copy, HTTP request or persistence operation.

Local tests cover real QUIC/TCP, HTTP and HTTPS, reliable snapshot equivalence, short/failed writes and denied callbacks. Performance and integration evidence is maintained in the workspace audit report; no WAN or deployment claim is implied.
