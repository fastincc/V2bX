package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	xn "github.com/xtls/xray-core/common/net"
	xc "github.com/xtls/xray-core/core"
	coreconf "github.com/xtls/xray-core/infra/conf"
	"io"
	"net"
	"testing"
	"time"
)

func TestXrayRealRevokedTail(t *testing.T) {
	for _, protocol := range []string{"vmess", "vless"} {
		t.Run(protocol, func(t *testing.T) {
			limiter.Init()
			tag := "tail-" + protocol
			raw := "d803678a-23d7-497c-8efb-c4b0392a4111" // synthetic fixture, never a deployed credential
			user := panel.UserInfo{Id: 41, Uuid: raw}
			limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{user}, nil)
			defer limiter.DeleteLimiter(tag)
			c, e := New(&conf.CoreConfig{XrayConfig: conf.NewXrayConfig()})
			if e != nil {
				t.Fatal(e)
			}
			x := c.(*Xray)
			if e = x.Start(); e != nil {
				t.Fatal(e)
			}
			defer x.Close()
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			info := &panel.NodeInfo{Type: protocol, Common: &panel.CommonNode{ServerPort: port}, VAllss: &panel.VAllssNode{Network: "tcp", NetworkSettings: []byte(`{}`)}}
			options := &conf.Options{ListenIP: "127.0.0.1", ReportMinTraffic: 1024, XrayOptions: conf.NewXrayOptions()}
			options.XrayOptions.DisableSniffing = true
			if e = x.AddNode(tag, info, options); e != nil {
				t.Fatal(e)
			}
			add := func(uid int) {
				if _, e := x.AddUsers(&vcore.AddUsersParams{Tag: tag, NodeInfo: info, Users: []panel.UserInfo{{Id: uid, Uuid: raw}}}); e != nil {
					t.Fatal(e)
				}
			}
			add(41)
			echo, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer echo.Close()
			go func() {
				for {
					c, e := echo.Accept()
					if e != nil {
						return
					}
					go func() { defer c.Close(); io.Copy(c, c) }()
				}
			}()
			// Real protocol handshake and encrypted VMess payload, through the production dispatcher.
			config := fmt.Sprintf(`{"log":{"loglevel":"none"},"outbounds":[{"protocol":%q,"settings":{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":%q,"security":"auto","encryption":"none"}]}]}}]}`, protocol, port, raw)
			var clientConfig coreconf.Config
			if e = json.Unmarshal([]byte(config), &clientConfig); e != nil {
				t.Fatal(e)
			}
			parsed, e := clientConfig.Build()
			if e != nil {
				t.Fatal(e)
			}
			client, e := xc.New(parsed)
			if e != nil {
				t.Fatal(e)
			}
			if e = client.Start(); e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			dest, e := xn.ParseDestination("tcp:" + echo.Addr().String())
			if e != nil {
				t.Fatal(e)
			}
			dial := func() net.Conn {
				c, e := xc.Dial(context.Background(), client, dest)
				if e != nil {
					t.Fatal(e)
				}
				c.SetDeadline(time.Now().Add(3 * time.Second))
				return c
			}
			exchange := func(c net.Conn, n int) error {
				p := bytes.Repeat([]byte("x"), n)
				if _, e := c.Write(p); e != nil {
					return e
				}
				r := make([]byte, n)
				_, e := io.ReadFull(c, r)
				if e == nil && !bytes.Equal(p, r) {
					return errors.New("payload mismatch")
				}
				return e
			}
			old := dial()
			defer old.Close()
			if e = exchange(old, 777); e != nil {
				t.Fatal(e)
			}
			if e = x.DelUsers([]panel.UserInfo{user}, tag, info); e != nil {
				t.Fatal(e)
			}
			if e = exchange(old, 19); e == nil {
				t.Fatal("revoked connection still transfers")
			}
			add(42)
			fresh := dial()
			defer fresh.Close()
			if e = exchange(fresh, 333); e != nil {
				t.Fatal(e)
			}
			failure := errors.New("synthetic disk full")
			if e = x.PersistTrafficSnapshot(tag, func([]panel.UserTraffic) error { return failure }); e != failure {
				t.Fatal(e)
			}
			got := map[int]panel.UserTraffic{}
			if e = x.PersistTrafficSnapshot(tag, func(rows []panel.UserTraffic) error {
				for _, r := range rows {
					got[r.UID] = r
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			if (got[41].Upload != 777 && got[41].Upload != 796) || got[41].Download != 777 || got[42].Upload != 333 || got[42].Download != 333 {
				t.Fatalf("payload reconciliation failed: %+v", got)
			}
			calls := 0
			x.PersistTrafficSnapshot(tag, func([]panel.UserTraffic) error { calls++; return nil })
			if calls != 0 {
				t.Fatal("duplicate tail")
			}
			if e = x.DrainTraffic(tag); e != nil {
				t.Fatal(e)
			}
			if e = x.ResumeTraffic(tag); e != nil {
				t.Fatal(e)
			}
			fresh.Close()
			fresh = dial()
			defer fresh.Close()
			if e = exchange(fresh, 17); e != nil {
				t.Fatal(e)
			}
			x.DelUsers([]panel.UserInfo{user}, tag, info)
			rows, e := x.GetUserTrafficSlice(tag, true)
			if e != nil || len(rows) != 1 || rows[0].Upload != 17 || rows[0].Download != 17 {
				t.Fatal("legacy small tail lost", e, rows)
			}
			t.Logf("old-generation uplink including any pre-existing read: %d", got[41].Upload)
			t.Log("real proxy: revoked stream denied, re-add isolated, failed persistence retained, both directions reconciled")
		})
	}
}
