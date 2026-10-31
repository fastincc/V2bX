package hy2

import (
	"bytes"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/limiter"
	"net"
	"os"
	"testing"
	"time"
)

func TestHy2ActiveDeviceLive(t *testing.T) {
	for _, udp := range []bool{false, true} {
		t.Run(fmt.Sprintf("udp=%v", udp), func(t *testing.T) {
			f := newHy2SessionFixture(t, 1)
			c, err := f.dial("127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			payload := bytes.Repeat([]byte{42}, 1024)
			var exchange func() error
			if udp {
				u, e := c.UDP()
				if e != nil {
					t.Fatal(e)
				}
				defer u.Close()
				exchange = func() error {
					if e := u.Send(payload, f.udp); e != nil {
						return e
					}
					type response struct {
						p []byte
						e error
					}
					ch := make(chan response, 1)
					go func() { p, _, e := u.Receive(); ch <- response{p, e} }()
					select {
					case r := <-ch:
						if r.e != nil {
							return r.e
						}
						if !bytes.Equal(payload, r.p) {
							return fmt.Errorf("UDP payload mismatch")
						}
						return nil
					case <-time.After(3 * time.Second):
						return fmt.Errorf("UDP timeout")
					}
				}
			} else {
				s, e := c.TCP(f.tcp)
				if e != nil {
					t.Fatal(e)
				}
				defer s.Close()
				exchange = func() error { return hyExchange(s, payload) }
			}
			l, _ := limiter.GetLimiter(f.tag)
			for i := 0; i < 4; i++ {
				if e := exchange(); e != nil {
					t.Fatal(e)
				}
				rows, _ := l.GetOnlineDevice()
				if len(*rows) != 1 || (*rows)[0].UID != 7 || (*rows)[0].IP != "127.0.0.1" {
					t.Fatal("active real IP absent", rows)
				}
			}
			denied, e := f.dial(hySecondSource(t))
			if e == nil {
				denied.Close()
				t.Fatal("different source admitted over limit")
			}
			same, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal("same source rejected", e)
			}
			same.Close()
			c.Close()
			a := f.h.Hy2nodes[f.tag].Auth
			deadline := time.Now().Add(3 * time.Second)
			for {
				a.mutex.Lock()
				n := len(a.sessions)
				a.mutex.Unlock()
				if n == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("session cleanup timed out")
				}
				time.Sleep(time.Millisecond)
			}
			// The first sample consumes the pre-close observations. Active pinning must
			// not repopulate the next sample after all streams are closed.
			l.GetOnlineDevice()
			rows, _ := l.GetOnlineDevice()
			if len(*rows) != 0 {
				t.Fatal("closed source remains active", rows)
			}
			traffic, e := f.h.GetUserTrafficSlice(f.tag, true)
			if e != nil || len(traffic) != 1 || traffic[0].Upload != 4096 || traffic[0].Download != 4096 {
				t.Fatal("accounting changed", traffic, e)
			}
			t.Log("real IP, 4 online cycles, same-IP sharing, second-source denial, lease release and bidirectional accounting passed")
		})
	}
}

func TestHy2DeviceLeaseRejectedAndRevoked(t *testing.T) {
	a, _, logger := admissionFixture(t)
	addr := admissionAddr("192.0.2.1")
	_, id := a.Authenticate(addr, "synthetic", 0)
	logger.checkAdmission(addr, id)
	release, ok := logger.AcquireStream(addr, id)
	if !ok {
		t.Fatal("accepted stream missing lease")
	}
	l, _ := limiter.GetLimiter(a.tag)
	logger.checkAdmission(admissionAddr("192.0.2.2"), id)
	if _, ok := logger.AcquireStream(addr, id); ok {
		t.Fatal("denied connection reacquired lease")
	}
	release()
	release()
	a.disconnect(id)
	if _, ok := logger.AcquireStream(addr, id); ok {
		t.Fatal("disconnected handle acquired lease")
	}
	_, id = a.Authenticate(addr, "synthetic", 0)
	core := &Hysteria2{Hy2nodes: map[string]Hysteria2node{a.tag: {Auth: a}}}
	core.DelUsers([]panel.UserInfo{{Id: 7, Uuid: "synthetic"}}, a.tag, nil)
	if _, ok := logger.AcquireStream(addr, id); ok {
		t.Fatal("revoked identity acquired lease")
	}
	l.GetOnlineDevice()
	rows, _ := l.GetOnlineDevice()
	if len(*rows) != 0 {
		t.Fatal("rejected lease pinned online")
	}
}

func TestHy2DeviceLeaseBeyondIdleTTL(t *testing.T) {
	if os.Getenv("HY2_DEVICE_LONG") != "1" {
		t.Skip("set HY2_DEVICE_LONG=1 for real 185-second lease test")
	}
	f := newHy2SessionFixture(t, 1)
	c, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	s, e := c.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	l, _ := limiter.GetLimiter(f.tag)
	payload := bytes.Repeat([]byte{7}, 32768)
	var total int64
	end := time.Now().Add(185 * time.Second)
	for {
		if e = hyExchange(s, payload); e != nil {
			t.Fatal(e)
		}
		total += int64(len(payload))
		rows, _ := l.GetOnlineDevice()
		if len(*rows) != 1 || (*rows)[0].IP != "127.0.0.1" {
			t.Fatal("long stream disappeared", rows)
		}
		if time.Now().After(end) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	denied, e := f.dial(hySecondSource(t))
	if e == nil {
		denied.Close()
		t.Fatal("active source expired after 180 seconds")
	}
	rows, e := f.h.GetUserTrafficSlice(f.tag, true)
	if e != nil || len(rows) != 1 || rows[0].Upload != total || rows[0].Download != total {
		t.Fatal("long accounting", rows, e)
	}
	t.Logf("185 real seconds; active IP survives repeated reporting; another source denied; upload=download=%d", total)
}

func hySecondSource(t testing.TB) string {
	t.Helper()
	as, e := net.InterfaceAddrs()
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range as {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && !ip.IsLoopback() && ip.IsGlobalUnicast() {
			return ip.String()
		}
	}
	t.Fatal("second assigned local IPv4 required")
	return ""
}

func TestHy2DeviceDialFailureReleasesLease(t *testing.T) {
	f := newHy2SessionFixture(t, 1)
	c, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	target, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := target.Addr().String()
	target.Close()
	s, e := c.TCP(addr)
	if e == nil {
		_, e = s.Write([]byte("x"))
		if e == nil {
			buf := make([]byte, 1)
			s.SetReadDeadline(time.Now().Add(time.Second))
			_, e = s.Read(buf)
		}
		s.Close()
	}
	if e == nil {
		t.Fatal("closed target accepted")
	}
	l, _ := limiter.GetLimiter(f.tag)
	deadline := time.Now().Add(3 * time.Second)
	for {
		l.GetOnlineDevice()
		rows, _ := l.GetOnlineDevice()
		if len(*rows) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed dial leaked active device")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHy2DeviceSameIPStreamsReleaseIndependently(t *testing.T) {
	f := newHy2SessionFixture(t, 1)
	a, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	x, e := a.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer x.Close()
	y, e := b.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer y.Close()
	if e = hyExchange(x, []byte("first")); e != nil {
		t.Fatal(e)
	}
	if e = hyExchange(y, []byte("second")); e != nil {
		t.Fatal(e)
	}
	x.Close()
	a.Close()
	l, _ := limiter.GetLimiter(f.tag)
	for i := 0; i < 5; i++ {
		if e = hyExchange(y, []byte("still-active")); e != nil {
			t.Fatal(e)
		}
		rows, _ := l.GetOnlineDevice()
		if len(*rows) != 1 || (*rows)[0].IP != "127.0.0.1" {
			t.Fatal("one close released another flow", rows)
		}
	}
}
