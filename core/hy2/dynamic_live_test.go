package hy2

import (
	"bytes"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/limiter"
	"io"
	"net"
	"testing"
	"time"
)

func dynamicExchange(c net.Conn, n int) error {
	c.SetDeadline(time.Now().Add(8 * time.Second))
	p := bytes.Repeat([]byte{73}, 4096)
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
			return io.ErrUnexpectedEOF
		}
		n -= m
	}
	return nil
}
func TestDynamicHy2ExistingStreamAndUDPAccounting(t *testing.T) {
	f := newTailLifecycleFixture(t, 1, nil)
	cl, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	c, e := cl.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = dynamicExchange(c, 1024); e != nil {
		t.Fatal(e)
	}
	l, _ := limiter.GetLimiter(f.tag)
	u := []panel.UserInfo{{Id: 7, Uuid: "synthetic-live", DeviceLimit: 1, DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 30}}}
	l.UpdateDynamicPolicies(f.tag, u)
	if e = dynamicExchange(c, 1024); e != nil {
		t.Fatal(e)
	}
	l.TickDynamicPolicies(time.Now())
	start := time.Now()
	if e = dynamicExchange(c, 125000); e != nil {
		t.Fatal(e)
	}
	slow := time.Since(start)
	if slow < 1500*time.Millisecond || slow > 4*time.Second {
		t.Fatal("existing QUIC stream not limited", slow)
	}
	v := *u[0].DynamicSpeedLimit
	v.Revision = 2
	v.SpeedMbps = 4
	u[0].DynamicSpeedLimit = &v
	l.UpdateDynamicPolicies(f.tag, u)
	start = time.Now()
	if e = dynamicExchange(c, 125000); e != nil {
		t.Fatal(e)
	}
	faster := time.Since(start)
	if faster < 350*time.Millisecond || faster > 1500*time.Millisecond {
		t.Fatal("existing QUIC stream did not adjust", faster)
	}
	udp, e := cl.UDP()
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	udpTotal := 0
	for i := 0; i < 12; i++ {
		n := 1800
		if i%2 != 0 {
			n = 3000
		}
		payload := bytes.Repeat([]byte{47}, n)
		if e = udp.Send(payload, f.udp); e != nil {
			t.Fatal(e)
		}
		got, _, err := udp.Receive()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatal("fragmented UDP", err)
		}
		udpTotal += n
	}
	v.Revision = 3
	v.Enabled = false
	l.UpdateDynamicPolicies(f.tag, u)
	start = time.Now()
	if e = dynamicExchange(c, 125000); e != nil {
		t.Fatal(e)
	}
	off := time.Since(start)
	if off > time.Second {
		t.Fatal("disable not applied", off)
	}
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	want := int64(2048 + 3*125000 + udpTotal)
	if e != nil || len(rows) != 1 || rows[0].Upload != want || rows[0].Download != want {
		t.Fatal("billing mismatch", rows, e, want)
	}
	online, e := l.GetOnlineDevice()
	if e != nil || len(*online) != 1 || (*online)[0].IP != "127.0.0.1" {
		t.Fatal("real IP changed", online, e)
	}
	denied, e := f.dial(tailLifecycleSecondSource(t))
	if e == nil {
		denied.Close()
		t.Fatal("device limit bypass")
	}
	t.Logf("DYNAMIC_HY2 same_stream=true one_mbps=%s four_mbps=%s disabled=%s up=%d down=%d fragment_once=true", slow, faster, off, want, want)
}

func TestDynamicHy2FixedAndTemporaryMinimumDoesNotHalveSpeed(t *testing.T) {
	for _, tc := range []struct {
		name             string
		fixed, temporary int
	}{{"fixed-lower", 1, 4}, {"temporary-lower", 4, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTailLifecycleFixture(t, 1, nil)
			rateConfigure(t, f, 0, tc.fixed)
			cl, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			defer cl.Close()
			c, e := cl.TCP(f.tcp)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			l, _ := limiter.GetLimiter(f.tag)
			users := []panel.UserInfo{{Id: 7, Uuid: "synthetic-live", DeviceLimit: 1, SpeedLimit: tc.fixed, DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: tc.temporary, DurationSeconds: 30}}}
			l.UpdateDynamicPolicies(f.tag, users)
			h := l.DynamicControl(f.tag + "|synthetic-live")
			h.Observe(1)
			l.TickDynamicPolicies(time.Now())
			start := time.Now()
			if e = dynamicExchange(c, 125000); e != nil {
				t.Fatal(e)
			}
			elapsed := time.Since(start)
			if elapsed < 1700*time.Millisecond || elapsed > 3200*time.Millisecond {
				t.Fatal("minimum ceiling bypassed or duplicate pacing halved speed", elapsed)
			}
			rows, e := f.h.GetUserTrafficSlice(f.tag, false)
			if e != nil || len(rows) != 1 || rows[0].Upload != 125000 || rows[0].Download != 125000 {
				t.Fatal("accounting", e, rows)
			}
			t.Logf("DYNAMIC_HY2_MIN fixed=%d temporary=%d elapsed=%s up=125000 down=125000", tc.fixed, tc.temporary, elapsed)
		})
	}
}
