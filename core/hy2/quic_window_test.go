package hy2

import (
	"bytes"
	"fmt"
	"github.com/apernet/hysteria/core/v2/server"
	"testing"
)

func TestQUICInitialStreamWindow(t *testing.T) {
	for _, initial := range []uint64{0, 16384, 65536, 16 << 20} {
		for _, maximum := range []uint64{0, 16384, 32 << 20} {
			t.Run(fmt.Sprintf("%d/%d", initial, maximum), func(t *testing.T) {
				c := &serverConfig{}
				c.QUIC.InitStreamReceiveWindow = initial
				c.QUIC.MaxStreamReceiveWindow = maximum
				c.QUIC.InitConnectionReceiveWindow = 20 << 20
				c.QUIC.MaxConnectionReceiveWindow = 40 << 20
				q, e := new(Hysteria2node).getQUICConfig(c)
				if e != nil {
					t.Fatal(e)
				}
				want := initial
				if want == 0 {
					want = defaultStreamReceiveWindow
				}
				max := maximum
				if max == 0 {
					max = defaultStreamReceiveWindow
				}
				if q.InitialStreamReceiveWindow != want || q.MaxStreamReceiveWindow != max || q.InitialConnectionReceiveWindow != 20<<20 || q.MaxConnectionReceiveWindow != 40<<20 {
					t.Fatalf("window mapping: %+v", q)
				}
			})
		}
	}
	c := &serverConfig{}
	c.QUIC.InitStreamReceiveWindow = 16383
	if _, e := new(Hysteria2node).getQUICConfig(c); e == nil {
		t.Fatal("existing lower bound lost")
	}
}
func TestQUICInitialStreamWindowLive(t *testing.T) {
	for _, initial := range []uint64{0, 16384, 16 << 20} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			cfg := &serverConfig{}
			cfg.QUIC.InitStreamReceiveWindow = initial
			cfg.QUIC.MaxStreamReceiveWindow = 32 << 20
			q, e := new(Hysteria2node).getQUICConfig(cfg)
			if e != nil {
				t.Fatal(e)
			}
			f := newTailLifecycleFixture(t, 1, nil, func(c *server.Config) { c.QUICConfig = *q })
			client, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			s, e := client.TCP(f.tcp)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			p := bytes.Repeat([]byte{7}, 32768)
			for i := 0; i < 32; i++ {
				if e = tailLifecycleExchange(s, p); e != nil {
					t.Fatal(e)
				}
			}
			rows, e := f.h.GetUserTrafficSlice(f.tag, false)
			if e != nil || len(rows) != 1 || rows[0].Upload != 1<<20 || rows[0].Download != 1<<20 {
				t.Fatalf("window altered accounting: %+v %v", rows, e)
			}
		})
	}
}
