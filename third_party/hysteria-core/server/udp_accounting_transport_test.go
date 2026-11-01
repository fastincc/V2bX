package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/internal/frag"
	"github.com/apernet/hysteria/core/v2/internal/protocol"
	"github.com/apernet/quic-go"
)

func accountingQUICPair(t *testing.T) (*quic.Conn, *quic.Conn) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	pc, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pc.Close() })
	tlsConf := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"test-udp-accounting"}}
	listener, e := quic.Listen(pc, tlsConf, &quic.Config{EnableDatagrams: true, MaxDatagramFrameSize: 1200})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, e := quic.DialAddr(ctx, pc.LocalAddr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: tlsConf.NextProtos}, &quic.Config{EnableDatagrams: true, MaxDatagramFrameSize: 1200})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { peer.CloseWithError(0, "") }) // generated loopback certificate only
	server, e := listener.Accept(ctx)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { server.CloseWithError(0, "") })
	return server, peer
}

type accountingTransportLogger struct {
	TrafficLogger
	calls    int
	download uint64
	before   func(int) bool
}

func (l *accountingTransportLogger) LogTraffic(_ string, tx, rx uint64) bool {
	l.calls++
	if tx != 0 {
		panic("download changed direction")
	}
	if !l.before(l.calls) {
		return false
	}
	l.download += rx
	return true
}
func TestUDPAccountingRealSendFailures(t *testing.T) {
	for _, tc := range []struct {
		name            string
		closeAt, denyAt int
	}{{"success", 0, 0}, {"first-send-failure", 1, 0}, {"partial-send-failure", 3, 0}, {"deny-original", 0, 1}, {"deny-before-first-fragment", 0, 2}, {"deny-after-first-fragment", 0, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			conn, peer := accountingQUICPair(t)
			logger := &accountingTransportLogger{before: func(call int) bool {
				if call == tc.closeAt {
					conn.CloseWithError(0, "injected local close")
					// This QUIC dependency can still enqueue into a closed, non-full
					// datagram queue. Fill its bounded queue to obtain an actual send
					// error instead of equating Close or a nil SendDatagram with delivery.
					failed := false
					for i := 0; i < 64; i++ {
						if e := conn.SendDatagram([]byte{0}); e != nil {
							failed = true
							break
						}
					}
					if !failed {
						t.Fatal("failed to inject real closed-queue send error")
					}
				}
				return call != tc.denyAt
			}}
			pacer := new(recordedPacer)
			sender := &udpIOImpl{Conn: conn, AuthID: "opaque-session", TrafficLogger: logger}
			io := &pacedUDPIO{udpIO: sender, send: sender.sendMessage, ctx: context.Background(), pacer: pacer}
			size := 3000
			if tc.name == "first-send-failure" {
				size = 1000
			}
			data := bytes.Repeat([]byte{9}, size)
			msg := &protocol.UDPMessage{SessionID: 1, FragCount: 1, Addr: "127.0.0.1:53", Data: data}
			err := sendMessageAutoFrag(io, make([]byte, 4096), msg)
			expected := uint64(len(data))
			if tc.denyAt == 1 {
				expected = 0
			}
			if logger.download != expected || pacer.bytes != int(expected) {
				t.Fatalf("bytes=%d quota=%d want=%d", logger.download, pacer.bytes, expected)
			}
			if tc.closeAt != 0 || tc.denyAt != 0 {
				stop := tc.closeAt
				if stop == 0 {
					stop = tc.denyAt
				}
				if err == nil || logger.calls != stop {
					t.Fatalf("retry continued after failure: err=%v calls=%d want=%d", err, logger.calls, stop)
				}
				return
			}
			if err != nil || logger.calls < 3 {
				t.Fatal("fragment path not exercised", err, logger.calls)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			defrag := new(frag.Defragger)
			for {
				packet, e := peer.ReceiveDatagram(ctx)
				if e != nil {
					t.Fatal(e)
				}
				m, e := protocol.ParseUDPMessage(packet)
				if e != nil {
					t.Fatal(e)
				}
				if complete := defrag.Feed(m); complete != nil {
					if !bytes.Equal(complete.Data, data) {
						t.Fatal("wire payload changed")
					}
					break
				}
			}
		})
	}
}
