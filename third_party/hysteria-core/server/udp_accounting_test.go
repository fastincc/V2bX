package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apernet/hysteria/core/v2/internal/protocol"
	"github.com/apernet/quic-go"
)

type accountingUDPIO struct {
	udpIO
	send func([]byte, *protocol.UDPMessage, bool) error
}

func (s accountingUDPIO) SendMessage(b []byte, m *protocol.UDPMessage, first bool) error {
	return s.send(b, m, first)
}

// Exercise the real logger/pacer entry point without sending: a zero-size
// serialization buffer produces the existing post-accounting silent drop.
func TestUDPLogicalMessageAccounting(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			logger := &putbackLogger{allow: true}
			sender := &udpIOImpl{TrafficLogger: logger}
			var p TrafficPacer
			record := new(recordedPacer)
			if limited {
				p = record
			}
			msg := &protocol.UDPMessage{SessionID: 1, Addr: "127.0.0.1:53", Data: make([]byte, 3000), FragCount: 1}
			if e := sender.sendMessage(context.Background(), p, nil, msg, true); e != nil {
				t.Fatal(e)
			}
			// Retry metadata is deliberately 1 as well. Local ownership, not a wire
			// field, decides whether to account or reserve quota again.
			for i := 0; i < 3; i++ {
				part := *msg
				part.Data = msg.Data[i*1000 : (i+1)*1000]
				if e := sender.sendMessage(context.Background(), p, nil, &part, false); e != nil {
					t.Fatal(e)
				}
			}
			wantQuota := 0
			if limited {
				wantQuota = 3000
			}
			if logger.rx != 3000 || logger.tx != 0 || logger.calls != 4 || record.bytes != wantQuota {
				t.Fatalf("billing=%d/%d calls=%d quota=%d", logger.tx, logger.rx, logger.calls, record.bytes)
			}
			// A new logical packet using the same session/packet IDs must count again.
			if e := sender.sendMessage(context.Background(), p, nil, msg, true); e != nil {
				t.Fatal(e)
			}
			if logger.rx != 6000 {
				t.Fatal("cross-message suppression")
			}
		})
	}
}
func TestUDPAccountingCancelledReservation(t *testing.T) {
	l := &putbackLogger{allow: true}
	sender := &udpIOImpl{TrafficLogger: l}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := new(recordedPacer)
	msg := &protocol.UDPMessage{Data: make([]byte, 3000), FragCount: 1}
	if e := sender.sendMessage(ctx, p, nil, msg, true); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if l.rx != 3000 || l.calls != 1 {
		t.Fatal("ingress accounting changed on cancellation")
	}
}
func TestUDPAutoFragmentOwnershipAndFailures(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:53", "[2001:db8::1234]:443", strings.Repeat("a", 180) + ".invalid:443"} {
		header := (&protocol.UDPMessage{Addr: addr}).HeaderSize()
		for _, size := range []int{0, 1100 - header, 1101 - header, 3000} {
			for _, failAt := range []int{0, 1, 2, 3} {
				t.Run(fmt.Sprintf("addr=%d/size=%d/fail=%d", len(addr), size, failAt), func(t *testing.T) {
					msg := &protocol.UDPMessage{SessionID: 3, Addr: addr, Data: bytes.Repeat([]byte{7}, size), FragCount: 1}
					buffer := make([]byte, 4096)
					calls, original := 0, 0
					var pieces []byte
					fault := errors.New("injected datagram failure")
					io := accountingUDPIO{send: func(b []byte, m *protocol.UDPMessage, first bool) error {
						calls++
						if first {
							original++
							if calls != 1 {
								t.Fatal("retry owns accounting")
							}
						} else if calls == 1 {
							t.Fatal("original missing ownership")
						}
						if calls == failAt {
							return fault
						}
						if m.Size() > 1100 {
							return &quic.DatagramTooLargeError{MaxDatagramPayloadSize: 1100}
						}
						pieces = append(pieces, m.Data...)
						return nil
					}}
					err := sendMessageAutoFrag(io, buffer, msg)
					if original != 1 {
						t.Fatal("wrong original attempts", original)
					}
					if failAt > 0 && calls >= failAt {
						if !errors.Is(err, fault) || calls != failAt {
							t.Fatal("failure did not stop retries", err, calls)
						}
					} else if err != nil || !bytes.Equal(pieces, msg.Data) {
						t.Fatal("payload reconstruction", err)
					}
				})
			}
		}
	}
}
func TestUDPSingleRetryAndPacedAdapterOwnership(t *testing.T) {
	l := &putbackLogger{allow: true}
	sender := &udpIOImpl{TrafficLogger: l}
	p := new(recordedPacer)
	attempts := 0
	wrapped := &pacedUDPIO{ctx: context.Background(), pacer: p, send: func(c context.Context, p TrafficPacer, b []byte, m *protocol.UDPMessage, first bool) error {
		attempts++
		if e := sender.sendMessage(c, p, nil, m, first); e != nil {
			return e
		}
		if attempts == 1 {
			return &quic.DatagramTooLargeError{MaxDatagramPayloadSize: 4096}
		}
		return nil
	}}
	msg := &protocol.UDPMessage{SessionID: 1, Addr: "test.invalid:53", Data: make([]byte, 1000), FragCount: 1}
	if e := sendMessageAutoFrag(wrapped, make([]byte, 4096), msg); e != nil {
		t.Fatal(e)
	}
	if attempts != 2 || l.calls != 2 || l.rx != 1000 || p.bytes != 1000 {
		t.Fatal("single-fragment retry or adapter double charged", attempts, l.rx, p.bytes)
	}
}
