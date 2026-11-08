//go:build linux && vision_live

package xray

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

func visionTimeoutConfig(idle, down uint32) *conf.XrayConnectionConfig {
	v := *conf.NewXrayConfig().ConnectionConfig
	v.ConnIdle, v.DownlinkOnly = idle, down
	return &v
}

func visionTimeoutReconcile(t *testing.T, f *visionFixture) {
	t.Helper()
	var up, down int64
	persist := func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			if r.UID != f.u.Id {
				t.Fatal("wrong user attribution")
			}
			up += r.Upload
			down += r.Download
		}
		return nil
	}
	for end := time.Now().Add(time.Second); time.Now().Before(end); {
		if err := f.x.PersistTrafficSnapshot(f.tag, persist); err != nil {
			t.Fatal(err)
		}
		if up == f.up.Load() && down == f.down.Load() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if up != f.up.Load() || down != f.down.Load() {
		t.Fatalf("successful transfer billing: up=%d/%d down=%d/%d", up, f.up.Load(), down, f.down.Load())
	}
	beforeUp, beforeDown := up, down
	if err := f.x.PersistTrafficSnapshot(f.tag, persist); err != nil || up != beforeUp || down != beforeDown {
		t.Fatal("unchanged traffic was billed twice", err)
	}
	online, err := f.l.GetOnlineDevice()
	if err != nil || len(*online) != 1 || (*online)[0].IP != "198.51.100.9" {
		t.Fatal("real IP lost", err)
	}
	if calls, _ := f.x.dispatcher.VisionSpliceStats(f.tag); calls != 0 {
		t.Fatal("default path enabled adjustable prototype")
	}
	t.Logf("VISION_TIMEOUT_RECONCILE up=%d down=%d exact=true real_ip=true prototype=false", up, down)
}

func expectVisionTimeoutClosure(t *testing.T, c *tls.Conn, maximum time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	// xc.Dial's cnc connection ignores deadlines. Enforce the test bound by
	// interrupting its underlying pipe, not by assuming SetReadDeadline works.
	var timedOut atomic.Bool
	guard := time.AfterFunc(maximum, func() {
		timedOut.Store(true)
		c.NetConn().Close()
	})
	defer guard.Stop()
	var p [1]byte
	n, err := c.Read(p[:])
	if timedOut.Load() {
		t.Fatal("configured activity timeout was replaced or did not close the raw copy")
	}
	if err == nil || n != 0 {
		t.Fatal("unexpected data after target stopped", n, err)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("configured activity timeout was replaced or did not close the raw copy", err)
	}
	return time.Since(start)
}

func TestVisionRealDefaultIdleTimeout(t *testing.T) {
	f := newVisionFixtureWithOptions(t, false, 0, visionFixtureOptions{connectionConfig: visionTimeoutConfig(1, 1)})
	logs := observeVisionSplice(t, f)
	c := f.dial(t)
	visionRead(t, c, 256<<10)
	visionRead(t, c, 256<<10)
	if logs.calls.Load() == 0 {
		t.Fatal("test never entered node splice")
	}
	visionTimeoutReconcile(t, f)
	elapsed := expectVisionTimeoutClosure(t, c, 4*time.Second)
	t.Logf("VISION_DEFAULT_IDLE configured=1s closed_after=%s", elapsed)
}

func TestVisionRealDefaultHalfCloseKeepsActiveDownlink(t *testing.T) {
	for _, stage := range []string{"after-splice", "before-splice"} {
		t.Run(stage, func(t *testing.T) {
			const payloadBytes = 4 << 20
			nodeConn := make(chan *net.TCPConn, 1)
			requested := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			opts := visionFixtureOptions{
				connectionConfig: visionTimeoutConfig(20, 1),
				downlinkDelay:    20 * time.Millisecond,
				nodeConnection: func(c *net.TCPConn) {
					select {
					case nodeConn <- c:
					default:
					}
				},
				beforeDownlink: func(n int) {
					if stage == "before-splice" && n == payloadBytes {
						requested <- struct{}{}
						<-release
					}
				},
			}
			f := newVisionFixtureWithOptions(t, false, 0, opts)
			// Release a stalled synthetic target before fixture teardown on any
			// failed precondition; the early cleanup also covers setup failures.
			t.Cleanup(unblock)
			logs := observeVisionSplice(t, f)
			c, err := f.open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if stage == "after-splice" {
				visionRead(t, c, 64)
				visionRead(t, c, 64)
				visionRead(t, c, 256<<10)
				visionRead(t, c, 256<<10)
				if logs.calls.Load() == 0 {
					t.Fatal("test never entered node splice before FIN")
				}
			}
			var gateway *net.TCPConn
			select {
			case gateway = <-nodeConn:
			case <-time.After(time.Second):
				t.Fatal("gateway-to-node connection not observed")
			}
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], payloadBytes)
			c.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := c.Write(size[:]); err != nil {
				t.Fatal(err)
			}
			payload := make([]byte, payloadBytes)
			prefix := 0
			if stage == "after-splice" {
				prefix = 32 << 10
				if _, err := io.ReadFull(c, payload[:prefix]); err != nil {
					t.Fatal("target did not receive final request", err)
				}
			} else {
				select {
				case <-requested:
				case <-time.After(time.Second):
					t.Fatal("target did not receive final request")
				}
				if logs.calls.Load() != 0 {
					t.Fatal("test already entered node splice before intended pre-splice FIN")
				}
			}
			// This is a transport FIN on the TCP socket connected to the node,
			// not a TLS close_notify or the end of an HTTP request. The gateway's
			// opposite direction stays open until the node closes it.
			if err := gateway.CloseWrite(); err != nil {
				t.Fatal("failed to deliver transport half-close", err)
			}
			start := time.Now()
			unblock()
			if _, err := io.ReadFull(c, payload[prefix:]); err != nil {
				t.Fatal("active downlink was cut off after upstream FIN", err)
			}
			elapsed := time.Since(start)
			if elapsed < 2*time.Second || !bytes.Equal(payload, bytes.Repeat([]byte{69}, payloadBytes)) {
				t.Fatal("paced response did not cross the half-close timeout intact", elapsed)
			}
			if logs.calls.Load() == 0 {
				t.Fatal("paced response never entered node splice")
			}
			visionTimeoutReconcile(t, f)
			idle := expectVisionTimeoutClosure(t, c, 4*time.Second)
			t.Logf("VISION_DEFAULT_HALF_CLOSE stage=%s configured_downlink_only=1s active_duration=%s idle_close=%s payload=%d transport_fin=true", stage, elapsed, idle, payloadBytes)
		})
	}
}

func TestVisionRealDefaultSparseActivity(t *testing.T) {
	f := newVisionFixtureWithOptions(t, false, 0, visionFixtureOptions{connectionConfig: visionTimeoutConfig(1, 1)})
	logs := observeVisionSplice(t, f)
	c := f.dial(t)
	visionRead(t, c, 256<<10)
	for i := 0; i < 15; i++ {
		time.Sleep(200 * time.Millisecond)
		visionRead(t, c, 64)
	}
	if logs.calls.Load() == 0 {
		t.Fatal("sparse activity did not use node splice")
	}
	visionTimeoutReconcile(t, f)
	elapsed := expectVisionTimeoutClosure(t, c, 4*time.Second)
	t.Logf("VISION_DEFAULT_SPARSE active_duration=3s configured_idle=1s idle_close=%s", elapsed)
}
