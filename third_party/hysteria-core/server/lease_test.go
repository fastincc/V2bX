package server

import (
	"errors"
	"github.com/apernet/hysteria/core/v2/internal/protocol"
	"github.com/stretchr/testify/mock"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type leaseTestEvents struct{ active atomic.Int64 }

func (*leaseTestEvents) New(uint32, string)  {}
func (*leaseTestEvents) Close(uint32, error) {}
func (l *leaseTestEvents) Acquire() (func(), bool) {
	l.active.Add(1)
	var once sync.Once
	return func() { once.Do(func() { l.active.Add(-1) }) }, true
}
func TestUDPLeaseTimeoutAndFailedDial(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "dial-failure", false: "idle-timeout"}[fail], func(t *testing.T) {
			io := newMockUDPIO(t)
			events := new(leaseTestEvents)
			m := newUDPSessionManager(io, events, time.Millisecond)
			msg := &protocol.UDPMessage{SessionID: 1, FragCount: 1, Addr: "test.invalid:53", Data: []byte("x")}
			io.EXPECT().Hook(mock.Anything, mock.Anything).Return(nil).Once()
			if fail {
				io.EXPECT().UDP(msg.Addr).Return(nil, errors.New("dial failed")).Once()
			} else {
				c := newMockUDPConn(t)
				closed := make(chan struct{})
				c.EXPECT().WriteTo(msg.Data, msg.Addr).Return(1, nil).Once()
				c.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func([]byte) (int, string, error) { <-closed; return 0, "", errors.New("closed") }).Once()
				c.EXPECT().Close().RunAndReturn(func() error { close(closed); return nil }).Once()
				io.EXPECT().UDP(msg.Addr).Return(c, nil).Once()
			}
			m.feed(msg)
			if !fail {
				if events.active.Load() != 1 {
					t.Fatal("no active lease")
				}
				time.Sleep(10 * time.Millisecond)
				m.cleanup(true)
			}
			m.receivers.Wait()
			if events.active.Load() != 0 || m.Count() != 0 {
				t.Fatal("UDP lease or session leaked", events.active.Load(), m.Count())
			}
		})
	}
}
