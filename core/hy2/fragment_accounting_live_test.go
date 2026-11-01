package hy2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/limiter"
	client "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/core/v2/server"
)

type fragmentAccountingLog struct {
	*HookServer
	quota      atomic.Int64
	zeroChecks atomic.Int64
	downCalls  atomic.Int64
	afterDown  func(rx uint64, accepted bool)
}

func (l *fragmentAccountingLog) LogTraffic(id string, tx, rx uint64) bool {
	ok := l.HookServer.LogTraffic(id, tx, rx)
	if tx == 0 {
		l.downCalls.Add(1)
		if rx == 0 {
			l.zeroChecks.Add(1)
		}
		if l.afterDown != nil {
			l.afterDown(rx, ok)
		}
	}
	return ok
}
func (l *fragmentAccountingLog) TrafficPacer(id string) server.TrafficPacer {
	p := l.HookServer.TrafficPacer(id)
	if p == nil {
		return nil
	}
	return &fragmentAccountingPacer{p, l}
}

type fragmentAccountingPacer struct {
	server.TrafficPacer
	log *fragmentAccountingLog
}

func (p *fragmentAccountingPacer) Wait(ctx context.Context, n int) error {
	if e := p.TrafficPacer.Wait(ctx, n); e != nil {
		return e
	}
	// A disabled dynamic handle is present to allow hot enable, but spends no quota.
	h, ok := p.TrafficPacer.(*hyPacer)
	if !ok || h.rate != nil || h.dynamic.ActiveLimit() {
		p.log.quota.Add(int64(n))
	}
	return nil
}
func fragmentExchange(c client.HyUDPConn, addr string, size int) error {
	p := bytes.Repeat([]byte{57}, size)
	if e := c.Send(p, addr); e != nil {
		return e
	}
	b, _, e := c.Receive()
	if e != nil {
		return e
	}
	if !bytes.Equal(b, p) {
		return fmt.Errorf("fragment payload changed: sent=%d received=%d", len(p), len(b))
	}
	return nil
}
func TestHy2FragmentAccountingLive(t *testing.T) {
	for _, speed := range []int{0, 1, 2, 8} {
		t.Run(fmt.Sprintf("Mbps=%d", speed), func(t *testing.T) {
			var log *fragmentAccountingLog
			f := newTailLifecycleFixture(t, 1, nil, func(c *server.Config) {
				log = &fragmentAccountingLog{HookServer: c.TrafficLogger.(*HookServer)}
				c.TrafficLogger = log
			})
			rateConfigure(t, f, 0, speed)
			clients := make([]client.Client, 2)
			sessions := make([]client.HyUDPConn, 2)
			for i := range clients {
				c, e := f.dial("127.0.0.1")
				if e != nil {
					t.Fatal(e)
				}
				defer c.Close()
				clients[i] = c
				u, e := c.UDP()
				if e != nil {
					t.Fatal(e)
				}
				defer u.Close()
				sessions[i] = u
			}
			each := 120000
			if speed > 0 {
				each = speed * 125000 * 3 / 4
			}
			done := make(chan error, 2)
			var before, after syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_SELF, &before)
			start := time.Now()
			for _, u := range sessions {
				go func(u client.HyUDPConn) {
					sizes := []int{1, 1000, 1100, 1200, 1800, 3000, 4000}
					left := each
					for i := 0; left > 0; i++ {
						n := min(left, sizes[i%len(sizes)])
						if e := fragmentExchange(u, f.udp, n); e != nil {
							done <- e
							return
						}
						left -= n
					}
					done <- nil
				}(u)
			}
			for range sessions {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(12 * time.Second):
					t.Fatal("fragment exchange stalled")
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
			if speed > 0 && (elapsed < 1.85 || elapsed > 4.5) {
				t.Fatalf("fragment rate charged twice or bypassed %.4fs", elapsed)
			}
			want := int64(each * 2)
			rows, e := f.h.GetUserTrafficSlice(f.tag, false)
			if e != nil || len(rows) != 1 || rows[0].Upload != want || rows[0].Download != want {
				t.Fatalf("fragment accounting: application=%d each direction rows=%+v err=%v", want, rows, e)
			}
			if log.zeroChecks.Load() == 0 {
				t.Fatal("no per-fragment zero-byte authorization checks")
			}
			quota := int64(0)
			if speed > 0 {
				quota = 2 * want
			}
			if log.quota.Load() != quota {
				t.Fatalf("quota=%d want=%d", log.quota.Load(), quota)
			}
			l, _ := limiter.GetLimiter(f.tag)
			online, e := l.GetOnlineDevice()
			if e != nil || len(*online) != 1 || (*online)[0].IP != "127.0.0.1" || (*online)[0].UID != 7 {
				t.Fatal("actual IP/device changed", online, e)
			}
			bad, e := f.dial(tailLifecycleSecondSource(t))
			if e == nil {
				bad.Close()
				t.Fatal("device limit bypassed")
			}
			failed := errors.New("injected persistence failure")
			if e = f.h.PersistTrafficSnapshot(f.tag, func([]panel.UserTraffic) error { return failed }); e != failed {
				t.Fatal(e)
			}
			retry, _ := f.h.GetUserTrafficSlice(f.tag, false)
			if len(retry) != 1 || retry[0] != rows[0] {
				t.Fatal("failed snapshot changed bytes", retry)
			}
			if e = f.h.PersistTrafficSnapshot(f.tag, func(saved []panel.UserTraffic) error {
				if len(saved) != 1 || saved[0] != rows[0] {
					return errors.New("checkpoint mismatch")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			empty, _ := f.h.GetUserTrafficSlice(f.tag, false)
			if len(empty) != 0 {
				t.Fatal("duplicate after successful checkpoint")
			}
			result, _ := json.Marshal(map[string]any{"mbps": speed, "seconds": elapsed, "up": want, "down": want, "quota": log.quota.Load(), "fragment_checks": log.zeroChecks.Load(), "reconciled": true, "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss})
			t.Logf("FRAGMENT_ACCOUNTING %s", result)
		})
	}
}
func TestHy2FragmentIPv6Destination(t *testing.T) {
	target, e := net.ListenUDP("udp6", &net.UDPAddr{IP: net.ParseIP("::1")})
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, e := target.ReadFromUDP(buf)
			if e != nil {
				return
			}
			target.WriteToUDP(buf[:n], addr)
		}
	}()
	f := newTailLifecycleFixture(t, 1, nil)
	c, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	u, e := c.UDP()
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	done := make(chan error, 1)
	go func() { done <- fragmentExchange(u, target.LocalAddr().String(), 3000) }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("IPv6 target timeout")
	}
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil || len(rows) != 1 || rows[0].Upload != 3000 || rows[0].Download != 3000 {
		t.Fatal("IPv6 target accounting", rows, e)
	}
}
func TestHy2FragmentRevocationAndDrain(t *testing.T) {
	for _, action := range []string{"revoke-before-fragment", "revoke-after-fragment", "drain-before-fragment", "cancel-pacing"} {
		t.Run(action, func(t *testing.T) {
			var log *fragmentAccountingLog
			var f *tailLifecycleFixture
			var injected atomic.Bool
			errorsCh := make(chan error, 1)
			f = newTailLifecycleFixture(t, 1, nil, func(c *server.Config) {
				log = &fragmentAccountingLog{HookServer: c.TrafficLogger.(*HookServer)}
				log.afterDown = func(rx uint64, ok bool) {
					trigger := rx > 0
					if action == "revoke-after-fragment" {
						trigger = rx == 0
					}
					if !ok || !trigger || !injected.CompareAndSwap(false, true) {
						return
					}
					var e error
					if action == "revoke-before-fragment" || action == "revoke-after-fragment" {
						e = f.h.DelUsers([]panel.UserInfo{{Id: 7, Uuid: "synthetic-live"}}, f.tag, nil)
					} else {
						e = f.h.DrainTraffic(f.tag)
					}
					errorsCh <- e
				}
				c.TrafficLogger = log
			})
			if action == "cancel-pacing" {
				rateConfigure(t, f, 0, 1)
			}
			c, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			u, e := c.UDP()
			if e != nil {
				t.Fatal(e)
			}
			defer u.Close()
			done := make(chan error, 1)
			go func() { done <- fragmentExchange(u, f.udp, 3000) }()
			select {
			case e := <-errorsCh:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("injection not reached")
			}
			if action == "cancel-pacing" {
				c.Close()
			} // UDP session failure need not close the physical transport.
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("denied fragmented message delivered")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("revocation did not stop forwarding")
			}
			calls := int64(2)
			if action == "revoke-after-fragment" {
				calls = 3
			}
			if action == "cancel-pacing" {
				calls = 1
			}
			if log.downCalls.Load() != calls {
				t.Fatalf("unexpected fragment attempts %d want %d", log.downCalls.Load(), calls)
			}
			rows, e := f.h.GetUserTrafficSlice(f.tag, false)
			if e != nil || len(rows) != 1 || rows[0].Upload != 3000 || rows[0].Download != 3000 {
				t.Fatalf("failed/denied ingress attribution %+v %v", rows, e)
			}
		})
	}
}

func TestHy2FragmentCheckpointBetweenAttempts(t *testing.T) {
	var f *tailLifecycleFixture
	var saved []panel.UserTraffic
	var once atomic.Bool
	checkpoint := make(chan error, 1)
	f = newTailLifecycleFixture(t, 1, nil, func(c *server.Config) {
		log := &fragmentAccountingLog{HookServer: c.TrafficLogger.(*HookServer)}
		log.afterDown = func(rx uint64, ok bool) {
			if ok && rx > 0 && once.CompareAndSwap(false, true) {
				checkpoint <- f.h.PersistTrafficSnapshot(f.tag, func(rows []panel.UserTraffic) error { saved = append(saved, rows...); return nil })
			}
		}
		c.TrafficLogger = log
	})
	c, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	u, e := c.UDP()
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	done := make(chan error, 1)
	go func() { done <- fragmentExchange(u, f.udp, 3000) }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fragment exchange stalled")
	}
	select {
	case e := <-checkpoint:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("checkpoint not reached")
	}
	if len(saved) != 1 || saved[0].Upload != 3000 || saved[0].Download != 3000 {
		t.Fatal("wrong mid-fragment snapshot", saved)
	}
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil || len(rows) != 0 {
		t.Fatal("fragment retries created a duplicate batch", rows, e)
	}
}
