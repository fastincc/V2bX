package sing

import (
	"bytes"
	"context"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"testing"
	"time"
)

func dynamicEcho(c net.Conn, n int) error {
	c.SetDeadline(time.Now().Add(8 * time.Second))
	p := bytes.Repeat([]byte{31}, 4096)
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
func TestDynamicAnyTLSExistingStreamIPDeviceBilling(t *testing.T) {
	f := newReviewFixture(t, true)
	cl := f.client(t)
	defer cl.Close()
	c, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = dynamicEcho(c, 1024); e != nil {
		t.Fatal(e)
	}
	l, _ := limiter.GetLimiter("review")
	u := []panel.UserInfo{{Id: 123, Uuid: "test-user", DeviceLimit: 1, DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 30}}}
	l.UpdateDeviceLimits("review", u)
	l.UpdateDynamicPolicies("review", u)
	if e = dynamicEcho(c, 1024); e != nil {
		t.Fatal(e)
	}
	// Exercise the real one-second scheduler, not a forced trigger.
	h := l.DynamicControl(format.UserTag("review", "test-user"))
	deadline := time.Now().Add(3 * time.Second)
	for !h.Status().Limited && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !h.Status().Limited {
		t.Fatal("scheduler did not trigger")
	}
	start := time.Now()
	if e = dynamicEcho(c, 125000); e != nil {
		t.Fatal(e)
	}
	slow := time.Since(start)
	if slow < 1500*time.Millisecond || slow > 4*time.Second {
		t.Fatal("existing stream not limited", slow)
	}
	v := *u[0].DynamicSpeedLimit
	v.Revision = 2
	v.SpeedMbps = 4
	u[0].DynamicSpeedLimit = &v
	l.UpdateDynamicPolicies("review", u)
	start = time.Now()
	if e = dynamicEcho(c, 125000); e != nil {
		t.Fatal(e)
	}
	faster := time.Since(start)
	if faster < 350*time.Millisecond || faster > 1500*time.Millisecond {
		t.Fatal("existing stream did not adjust", faster)
	}
	v.Revision = 3
	v.Enabled = false
	l.UpdateDynamicPolicies("review", u)
	start = time.Now()
	if e = dynamicEcho(c, 125000); e != nil {
		t.Fatal(e)
	}
	off := time.Since(start)
	if off > time.Second {
		t.Fatal("existing stream did not recover", off)
	}
	select {
	case ip := <-f.router.sources:
		if ip != "198.51.100.9" {
			t.Fatal("real IP changed", ip)
		}
	default:
		t.Fatal("missing source")
	}
	if _, reject := l.CheckLimit(format.UserTag("review", "test-user"), "198.51.100.10", true, true); !reject {
		t.Fatal("device limit bypass")
	}
	value, _ := f.router.hook.counter.Load("review")
	tc := value.(*counter.TrafficCounter).GetCounter("test-user")
	want := int64(2048 + 3*125000)
	if tc.UpCounter.Load() != want || tc.DownCounter.Load() != want {
		t.Fatal("billing changed", tc.UpCounter.Load(), tc.DownCounter.Load(), want)
	}
	t.Logf("DYNAMIC_ANYTLS same_stream=true one_mbps=%s four_mbps=%s disabled=%s up=%d down=%d real_ip=true device_limit=true", slow, faster, off, want, want)
}
