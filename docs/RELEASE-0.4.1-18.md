# fastincc-v0.4.1-18 — Panel-managed dynamic limits and opt-in transport prototypes

## Changes since -17

- **Panel-managed dynamic speed limits:** negotiate optional `dynamic_speed_limit` v1 policies through the existing user API. A per-user sliding window observes raw upload plus download bytes on each node. Reaching the threshold applies a temporary shared rate, which expires without disconnecting existing streams. TCP/UDP and concurrent connections share the policy; static and dynamic ceilings use the smaller positive limit. Rule updates, cancellation, revocation and node replacement preserve existing attribution and lifecycle handling.
- **Linux Vision Splice prototype:** an explicitly enabled Xray/VLESS Vision node can apply live quotas while retaining eligible kernel-splice downlink transfers. Buffered fallback and the existing upload path remain available. The pinned Xray source is vendored with small opt-in hooks; its upstream version is not upgraded.
- **AnyTLS adaptive QUICKACK prototype:** eligible Linux sing AnyTLS direct TCP destination connections can request earlier ACKs after short reads, with a per-connection cooldown. This is a best-effort hint for Nagle/delayed-ACK interactions, not a guarantee that every 40 ms wait disappears.
- **Deterministic QUIC path-probe test:** use consecutive packet numbers only inside the affected test. All 13 existing assertions remain intact. Production packet-number skipping, forwarding and congestion control are unchanged.

The implementation and test repairs retain their independent commits. This release includes the QUIC test repair `42ac9fa`; its deterministic reproduction confirmed an inherited test-precondition problem rather than a new forwarding regression.

## Defaults and compatibility

**Both prototypes remain off by default:** `EnableVisionSplice: false` and `AnyTLSAdaptiveQuickACK: false`. Existing configuration files need no new fields to keep them disabled. Packaging or upgrading does not opt nodes into either prototype.

Ordinary dynamic limiting is driven by the panel, not by either prototype switch. A supporting panel can activate it by returning an enabled, valid policy to a capable node; absent policies leave it inactive. Policies use the existing authenticated user endpoint and capability headers, with JSON/MessagePack and ETag/304 semantics. The node release does not modify a panel or run database migrations. See DYNAMIC-SPEED-LIMIT-INTERFACE.md before enabling panel rules.

**Behavior change:** the old local `EnableDynamicSpeedLimit` setting is still parsed, but its legacy execution loop is inactive and logs a warning. Migrate intended dynamic limits to panel v1 policies. This is a traffic-burst policy, not a plan-quota-exhaustion feature and not an aggregate limit across different nodes. Window sampling is approximate; restarting the process clears in-memory dynamic windows and temporary penalties.

Vision remains excluded from dynamic policies unless the Linux node explicitly enables `EnableVisionSplice` and the panel authorizes the prototype capability. Explicitly enabling Vision Splice on a non-Linux platform rejects that node's startup. The prototype switch itself is applied through the normal node rebuild lifecycle; subsequent dynamic rule changes can affect existing connections.

Subscription formats, real-IP/proxy trust, authentication, device admission, billing direction, reliable batch formats and automatic reliable-reporting negotiation retain their existing contracts. Dynamic observation does not bill retransmitted batches. There is no installer change.

## Verification and measured boundaries

The release candidate passed local macOS race/vet checks and all 15 existing release-test steps in a local Linux arm64 VM, including live Vision Splice and native QUICKACK socket tests. The repaired QUIC test passed 10,000 race repetitions on each platform, and the complete ACK-handler package passed 20 rounds. Four-platform compilation passed. These local results are not a claim of Windows runtime or every client/architecture being verified; the release workflow reruns its own gates against the tagged commit.

Earlier isolated panel/node integration exercised policy updates on existing connections, expiry, static ceilings, shared quotas, real IP, device refusal, revocation and billing reconciliation. Public-link stepped tests completed 56 traffic samples plus two CPU-profile samples, totaling 212.47 GiB, with exact accounting for all 45 proxy samples. No sustained host outage was reproduced in that round.

**Performance caveat:** near-saturation dynamic-observation comparisons showed mean differences of -2.56% and -1.88% in opposite test orders, pooled approximately -2.23%. The association remains; the causal contribution of the implementation versus network timing has not been fully isolated. There is no universal zero-overhead claim. Larger windows and active user populations also increase memory requirements.

QUICKACK reduced delayed-ACK waits in targeted workloads, but earlier unfavorable public-upload samples remain unexplained. The most recent paired round did not reproduce the large decline. QUICKACK therefore remains an opt-in prototype. Vision zero-copy eligibility depends on the original TLS/transport conditions; not every connection uses splice. Real-client coverage, very long operation and arbitrary RTT/loss/concurrency combinations remain limited. Historical external-host connectivity interruption was not conclusively attributed.

## Existing limitations and rollback

The existing independent-Hy2 UDP reassembly ambiguity for consecutive reused PacketID/fragment-count combinations is not repaired here. Windows reliable-queue locking remains unsupported: explicit reliable mode, or auto negotiation into it, can prevent startup. Legacy traffic reporting has no batch-level idempotency. These are inherited boundaries, not new guarantees supplied by this release.

To disable panel dynamic limiting, send an explicit higher-revision disabled policy and allow normal user synchronization. Static limits remain in effect. Disabling a prototype follows its documented reload/rebuild lifecycle and should be performed with normal connection draining. Do not delete reliable spool files, change pending batch identities or bypass existing downgrade safeguards. A binary rollback to -17 removes panel-managed dynamic behavior and may reactivate its old local dynamic setting if configured; review that setting before rollback. No new persistent queue or database format is introduced by this node release.

Packages: **Linux amd64, Linux arm64, Linux armv7 and Windows amd64**. Prototype details and interface contracts are included in every package. Publishing this release does not deploy nodes or modify origin servers.
