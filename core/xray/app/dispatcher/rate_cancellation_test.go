package dispatcher

import (
	"context"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common/buf"
	XN "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
)

func TestLimitedLinkCancellationAndAccounting(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		limiter.Init()
		l := limiter.AddLimiter("rate-link", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic", SpeedLimit: 1}}, map[int]int{})
		d := new(DefaultDispatcher)
		lm := NewLinkManager(format.UserTag("rate-link", "synthetic"))
		d.LinkManagers.Store("generation", lm)
		if cancel {
			l.SpeedLimiter.Store(lm.LimitKey, ratelimit.NewBucketWithRate(1, 1))
		}
		ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: "rate-link", User: &protocol.MemoryUser{Email: "generation"}, Source: XN.TCPDestination(XN.LocalHostIP, 1234)})
		a, b, _, e := d.getLink(ctx, XN.Network_TCP)
		if e != nil {
			t.Fatal(e)
		}
		bucket, reject := l.CheckLimit(lm.LimitKey, "127.0.0.1", true, true)
		if reject || bucket == nil {
			t.Fatal("bucket")
		}
		if cancel {
			bucket.Take(bucket.Capacity())
		}
		done := make(chan error, 2)
		for _, w := range []buf.Writer{a.Writer, b.Writer} {
			go func(w buf.Writer) {
				p := buf.New()
				p.Write([]byte("data"))
				done <- w.WriteMultiBuffer(buf.MultiBuffer{p})
			}(w)
		}
		if cancel {
			// Both writers have reserved their unsent bytes before revocation.
			end := time.Now().Add(time.Second)
			for bucket.Available() > -8 && time.Now().Before(end) {
				time.Sleep(100 * time.Microsecond)
			}
			lm.CloseAll()
		}
		for i := 0; i < 2; i++ {
			select {
			case e := <-done:
				if cancel && e == nil || !cancel && e != nil {
					t.Fatal("write result", e)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("revoked limited writer did not stop")
			}
		}
		v, _ := d.Counter.Load("rate-link")
		c := v.(*counter.TrafficCounter).GetCounter("generation")
		want := int64(4)
		if cancel {
			want = 0
		}
		if c.UpCounter.Load() != want || c.DownCounter.Load() != want {
			t.Fatalf("wrong charged bytes up=%d down=%d want=%d", c.UpCounter.Load(), c.DownCounter.Load(), want)
		}
		lm.CloseAll()
		if !lm.Quiescent() {
			t.Fatal("manager not drained")
		}
	}
}
