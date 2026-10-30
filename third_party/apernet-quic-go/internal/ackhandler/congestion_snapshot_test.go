package ackhandler

import (
	congestionExt "github.com/apernet/quic-go/congestion"
	"github.com/apernet/quic-go/internal/congestion"
	"github.com/apernet/quic-go/internal/protocol"
	"github.com/apernet/quic-go/internal/utils"
	"github.com/apernet/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type snapshotRecordingCC struct {
	congestion.SendAlgorithmWithDebugInfos
	afterAck, afterLoss func()
	extended            int
}

func (c *snapshotRecordingCC) OnPacketAcked(p protocol.PacketNumber, n, flight protocol.ByteCount, now time.Time) {
	c.SendAlgorithmWithDebugInfos.OnPacketAcked(p, n, flight, now)
	if c.afterAck != nil {
		c.afterAck()
	}
}
func (c *snapshotRecordingCC) OnCongestionEvent(p protocol.PacketNumber, n, flight protocol.ByteCount) {
	c.SendAlgorithmWithDebugInfos.OnCongestionEvent(p, n, flight)
	if c.afterLoss != nil {
		c.afterLoss()
	}
}
func (c *snapshotRecordingCC) OnCongestionEventEx(_ protocol.ByteCount, _ time.Time, _ []congestionExt.AckedPacketInfo, _ []congestionExt.LostPacketInfo) {
	c.extended++
}

// Replace a controller at a precise callback boundary. This reproduces the
// publication performed by SetCongestionControl without timing-dependent sleeps.
func TestCongestionSnapshotDuringReplacement(t *testing.T) {
	for _, loss := range []bool{false, true} {
		name := "ACK"
		if loss {
			name = "timed-loss"
		}
		t.Run(name, func(t *testing.T) {
			h := newSentPacketHandler(0, 1200, &utils.RTTStats{}, true, false, protocol.PerspectiveServer, nil, utils.DefaultLogger)
			var tracker packetTracker
			send := func(now time.Time) protocol.PacketNumber {
				pn := h.PopPacketNumber(protocol.EncryptionInitial)
				h.SentPacket(now, pn, protocol.InvalidPacketNumber, nil, []Frame{tracker.NewPingFrame(pn)}, protocol.EncryptionInitial, protocol.ECNNon, 1000, false, false)
				return pn
			}
			now := time.Now()
			var ack protocol.PacketNumber
			if loss {
				send(now.Add(-time.Second))
				send(now.Add(-10 * time.Millisecond))
				ack = send(now)
				_, err := h.ReceivedAck(&wire.AckFrame{AckRanges: ackRanges(ack)}, protocol.EncryptionInitial, now.Add(time.Second))
				require.NoError(t, err)
				require.False(t, h.GetLossDetectionTimeout().IsZero())
			} else {
				ack = send(now)
			}
			original := h.getCongestionControl()
			old, next := &snapshotRecordingCC{SendAlgorithmWithDebugInfos: original}, &snapshotRecordingCC{SendAlgorithmWithDebugInfos: original}
			replace := func() { h.congestionMutex.Lock(); h.congestion = next; h.congestionMutex.Unlock() }
			h.congestion = old
			if loss {
				old.afterLoss = replace
				require.NoError(t, h.OnLossDetectionTimeout(h.GetLossDetectionTimeout()))
			} else {
				old.afterAck = replace
				_, err := h.ReceivedAck(&wire.AckFrame{AckRanges: ackRanges(ack)}, protocol.EncryptionInitial, now.Add(time.Millisecond))
				require.NoError(t, err)
			}
			require.Same(t, next, h.getCongestionControl(), "replacement must actually occur")
			require.Equal(t, 1, old.extended, "extended callback must use the operation's original snapshot")
			require.Zero(t, next.extended, "replacement must not receive another controller's event")
		})
	}
}
