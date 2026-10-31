package hy2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	client "github.com/apernet/hysteria/core/v2/client"
	"io"
	"net"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

func rateConfigure(t *testing.T, f *tailLifecycleFixture, node, user int) {
	t.Helper()
	l, e := limiter.GetLimiter(f.tag)
	if e != nil {
		t.Fatal(e)
	}
	l.SpeedLimit = node
	u := []panel.UserInfo{{Id: 7, Uuid: "synthetic-live", DeviceLimit: 1, SpeedLimit: user}}
	l.UpdateUser(f.tag, u, u)
}
func rateTCP(c net.Conn, n int) error {
	defer c.Close()
	c.SetDeadline(time.Now().Add(12 * time.Second))
	p := bytes.Repeat([]byte{73}, 16384)
	copy(p, []byte("GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"))
	r := make([]byte, len(p))
	for n > 0 {
		m := min(n, len(p))
		if _, e := c.Write(p[:m]); e != nil {
			return e
		}
		if _, e := io.ReadFull(c, r[:m]); e != nil {
			return e
		}
		if !bytes.Equal(p[:m], r[:m]) {
			return fmt.Errorf("TCP payload changed")
		}
		n -= m
	}
	return nil
}
func rateUDP(c client.HyUDPConn, addr string, n int) error {
	defer c.Close()
	p := bytes.Repeat([]byte{37}, 1000)
	for n > 0 {
		m := min(n, len(p))
		if e := c.Send(p[:m], addr); e != nil {
			return e
		}
		b, _, e := c.Receive()
		if e != nil {
			return e
		}
		if !bytes.Equal(b, p[:m]) {
			return fmt.Errorf("UDP payload changed")
		}
		n -= m
	}
	return nil
}
func TestHy2RateLiveMatrix(t *testing.T) {
	cases := []struct {
		name                  string
		node, user, effective int
		udp                   bool
	}{
		{"user-1", 0, 1, 1, false}, {"user-8", 0, 8, 8, false}, {"node-2", 2, 0, 2, false},
		{"minimum-node", 1, 8, 1, false}, {"minimum-user", 8, 1, 1, false}, {"tcp-udp-shared", 0, 1, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTailLifecycleFixture(t, 1, nil)
			rateConfigure(t, f, tc.node, tc.user)
			c1, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			defer c1.Close()
			c2, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			defer c2.Close()
			a, e := c1.TCP(f.tcp)
			if e != nil {
				t.Fatal(e)
			}
			defer a.Close()
			// Three seconds of quota, including the existing one-second initial burst.
			each := tc.effective * 125000 * 3 / 4
			done := make(chan error, 2)
			var before, after syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_SELF, &before)
			var start time.Time
			if tc.udp {
				u, e := c2.UDP()
				if e != nil {
					t.Fatal(e)
				}
				defer u.Close()
				start = time.Now()
				go func() { done <- rateUDP(u, f.udp, each) }()
			} else {
				b, e := c2.TCP(f.tcp)
				if e != nil {
					t.Fatal(e)
				}
				defer b.Close()
				start = time.Now()
				go func() { done <- rateTCP(b, each) }()
			}
			go func() { done <- rateTCP(a, each) }()
			for i := 0; i < 2; i++ {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(12 * time.Second):
					t.Fatal("rate transfer stalled")
				}
			}
			elapsed := time.Since(start).Seconds()
			syscall.Getrusage(syscall.RUSAGE_SELF, &after)
			cpu := func(r syscall.Rusage) float64 {
				return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
			}
			rss := after.Maxrss
			if runtime.GOOS == "linux" {
				rss *= 1024
			}
			if elapsed < 1.85 || elapsed > 4.5 {
				t.Fatalf("shared rate or forwarding speed incorrect: %.4fs expected about 2s", elapsed)
			}
			rows, e := f.h.GetUserTrafficSlice(f.tag, false)
			if e != nil || len(rows) != 1 || rows[0].Upload != int64(2*each) || rows[0].Download != int64(2*each) {
				t.Fatalf("rate altered billing %+v %v", rows, e)
			}
			l, _ := limiter.GetLimiter(f.tag)
			online, e := l.GetOnlineDevice()
			if e != nil || len(*online) != 1 || (*online)[0].IP != "127.0.0.1" || (*online)[0].UID != 7 {
				t.Fatalf("source IP changed: %+v %v", online, e)
			}
			denied, e := f.dial(tailLifecycleSecondSource(t))
			if e == nil {
				denied.Close()
				t.Fatal("device limit bypassed")
			}
			failed := fmt.Errorf("injected local persistence failure")
			if e = f.h.PersistTrafficSnapshot(f.tag, func([]panel.UserTraffic) error { return failed }); e != failed {
				t.Fatal("snapshot failure ignored")
			}
			if e = f.h.PersistTrafficSnapshot(f.tag, func(saved []panel.UserTraffic) error {
				if len(saved) != 1 || saved[0] != rows[0] {
					return fmt.Errorf("snapshot changed accounting")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			cleared, _ := f.h.GetUserTrafficSlice(f.tag, false)
			if len(cleared) != 0 {
				t.Fatal("acknowledged snapshot counted twice")
			}
			result, _ := json.Marshal(map[string]any{"case": tc.name, "seconds": elapsed, "mbps_effective": tc.effective, "up": 2 * each, "down": 2 * each, "reconciled": true, "cpu_seconds": cpu(after) - cpu(before), "rss_bytes": rss})
			t.Logf("HY2_RATE %s", result)
		})
	}
}
func TestHy2RateUnrelatedUserAndClose(t *testing.T) {
	f := newTailLifecycleFixture(t, 1, nil)
	rateConfigure(t, f, 0, 1)
	l, _ := limiter.GetLimiter(f.tag)
	other := []panel.UserInfo{{Id: 8, Uuid: "synthetic-unlimited", DeviceLimit: 1}}
	l.UpdateUser(f.tag, other, nil)
	if _, e := f.h.AddUsers(&vcore.AddUsersParams{Tag: f.tag, Users: other}); e != nil {
		t.Fatal(e)
	}
	limited, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer limited.Close()
	free, _, e := client.NewClient(&client.Config{ConnFactory: tailLifecycleConnFactory{"127.0.0.1"}, ServerAddr: f.addr, Auth: "synthetic-unlimited", TLSConfig: client.TLSConfig{ServerName: "test.invalid", RootCAs: f.roots}})
	if e != nil {
		t.Fatal(e)
	}
	defer free.Close()
	s, e := limited.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	waiting := make(chan error, 1)
	go func() { waiting <- rateTCP(s, 2<<20) }()
	time.Sleep(100 * time.Millisecond)
	u, e := free.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	start := time.Now()
	if e = rateTCP(u, 1<<20); e != nil {
		t.Fatal(e)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("unrelated user blocked by rate lock %s", elapsed)
	}
	start = time.Now()
	limited.Close()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("connection close blocked behind rate wait")
	}
	// Node close must not wait for a long quota reservation either.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); f.h.Close() }()
	closed := make(chan struct{})
	go func() { wg.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("node close blocked behind rate wait")
	}
	t.Logf("limited close and node shutdown %s", time.Since(start))
}

func TestHy2RateSniffPrefix(t *testing.T) {
	f := newTailLifecycleFixture(t, 1, nil, sniffLiveConfig)
	rateConfigure(t, f, 0, 1)
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
	start := time.Now()
	if e = rateTCP(s, 187500); e != nil {
		t.Fatal(e)
	}
	elapsed := time.Since(start).Seconds()
	if elapsed < 1.85 || elapsed > 4.5 {
		t.Fatalf("sniff bypass or double quota %.4fs", elapsed)
	}
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil || len(rows) != 1 || rows[0].Upload != 187500 || rows[0].Download != 187500 {
		t.Fatalf("sniff accounting %+v %v", rows, e)
	}
	t.Logf("sniff prefix counted and paced once; seconds=%.4f", elapsed)
}
func TestHy2RateUserUpdate(t *testing.T) {
	f := newTailLifecycleFixture(t, 1, nil)
	rateConfigure(t, f, 0, 1)
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
	done := make(chan error, 1)
	go func() { done <- rateTCP(s, 2<<20) }()
	time.Sleep(100 * time.Millisecond)
	old := []panel.UserInfo{{Id: 7, Uuid: "synthetic-live", DeviceLimit: 1, SpeedLimit: 1}}
	next := []panel.UserInfo{{Id: 7, Uuid: "synthetic-live", DeviceLimit: 1, SpeedLimit: 8}}
	if e = f.h.DelUsers(old, f.tag, nil); e != nil {
		t.Fatal(e)
	}
	l, _ := limiter.GetLimiter(f.tag)
	l.UpdateUser(f.tag, next, old)
	if _, e = f.h.AddUsers(&vcore.AddUsersParams{Tag: f.tag, Users: next}); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("old generation continued")
		}
	case <-time.After(time.Second):
		t.Fatal("old rate wait survived revocation")
	}
	fresh, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	stream, e := fresh.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	start := time.Now()
	if e = rateTCP(stream, 1500000); e != nil {
		t.Fatal(e)
	}
	if elapsed := time.Since(start).Seconds(); elapsed < 1.85 || elapsed > 4.5 {
		t.Fatalf("new 8Mbps policy ineffective %.4fs", elapsed)
	}
}
