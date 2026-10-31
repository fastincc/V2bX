# Independent Hy2 retired identity cleanup

Legacy collection and reliable checkpoints also reclaim retired identities when no traffic counter exists. A retired-only FIFO is populated on user removal; each collection inspects at most 256 entries. Pending byte tails rotate to the back, preventing them from starving zero-byte cleanup. Active users are never scanned by this cleanup. There is no per-packet registration, new goroutine, persistence format or panel change.

The existing node collection gate and authentication mutex protect attribution. Reliable persistence remains outside the identity lock and failed saves retain their counters. Cleanup rechecks the current counter under the identity lock, preserving counters created during persistence. Revoked sessions remain denied, and re-added users receive independent generations.

Tests cover 10000/100000 removals without traffic in both modes, bounded progress, active users, pending tails, failed persistence and concurrent accounting through existing regression tests. Heap observations are bounded local evidence, not a production memory guarantee.
