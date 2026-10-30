package sing

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	ss "github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestShadowsocksLiveUserUpdates(t *testing.T) {
	for _, cipher := range []string{"aes-128-gcm", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"} {
		t.Run(cipher, func(t *testing.T) {
			limiter.Init()
			tag := "ss-update"
			users := []panel.UserInfo{{Id: 1, Uuid: "00000000-0000-4000-8000-000000000001"}, {Id: 2, Uuid: "00000000-0000-4000-8000-000000000002"}, {Id: 3, Uuid: "33333333-3333-4333-8333-333333333333"}}
			// 2022 derives the first 16/32 bytes, so test users need distinct prefixes.
			users[1].Uuid = "22222222-2222-4222-8222-222222222222"
			limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
			defer limiter.DeleteLimiter(tag)
			c, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
			if e != nil {
				t.Fatal(e)
			}
			s := c.(*Sing)
			defer s.Close()
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			keylen := 16
			if cipher == "2022-blake3-aes-256-gcm" {
				keylen = 32
			}
			serverKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, keylen))
			info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{ServerPort: port}, Shadowsocks: &panel.ShadowsocksNode{Cipher: cipher, ServerKey: serverKey}}
			if e = s.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: &conf.SingOptions{}}); e != nil {
				t.Fatal(e)
			}
			if _, e = s.AddUsers(&vcore.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); e != nil {
				t.Fatal(e)
			}
			if e = s.Start(); e != nil {
				t.Fatal(e)
			}
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
			exchange := func(user panel.UserInfo) error {
				var method ss.Method
				var e error
				if cipher == "aes-128-gcm" {
					method, e = shadowaead.New(cipher, nil, user.Uuid)
				} else {
					method, e = shadowaead_2022.NewWithPassword(cipher, serverKey+":"+base64.StdEncoding.EncodeToString([]byte(user.Uuid[:keylen])), time.Now)
				}
				if e != nil {
					return e
				}
				raw, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
				if e != nil {
					return e
				}
				defer raw.Close()
				raw.SetDeadline(time.Now().Add(2 * time.Second))
				conn, e := method.DialConn(raw, M.ParseSocksaddr(echo.Addr().String()))
				if e != nil {
					return e
				}
				defer conn.Close()
				payload := bytes.Repeat([]byte{byte(user.Id)}, 4096)
				if _, e = conn.Write(payload); e != nil {
					return e
				}
				got := make([]byte, len(payload))
				if _, e = io.ReadFull(conn, got); e != nil {
					return e
				}
				if !bytes.Equal(got, payload) {
					return fmt.Errorf("payload mismatch")
				}
				return nil
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Add(2)
			for _, u := range []panel.UserInfo{users[0], users[2]} {
				go func(u panel.UserInfo) {
					defer wg.Done()
					for i := 0; i < 20; i++ {
						if e := exchange(u); e != nil {
							errs <- e
							return
						}
					}
				}(u)
			}
			for i := 0; i < 20; i++ {
				if e = s.DelUsers(users[1:2], tag, info); e != nil {
					t.Fatal(e)
				}
				if _, e = s.AddUsers(&vcore.AddUsersParams{Tag: tag, NodeInfo: info, Users: users[1:2]}); e != nil {
					t.Fatal(e)
				}
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				t.Fatal(e)
			}
			if e = s.DelUsers(users[1:2], tag, info); e != nil {
				t.Fatal(e)
			}
			if e = exchange(users[1]); e == nil {
				t.Fatal("revoked user accepted")
			}
			time.Sleep(50 * time.Millisecond)
			rows, e := s.GetUserTrafficSlice(tag, false)
			if e != nil {
				t.Fatal(e)
			}
			counts := map[int]panel.UserTraffic{}
			for _, r := range rows {
				counts[r.UID] = r
			}
			for _, uid := range []int{1, 3} {
				r := counts[uid]
				if r.Upload != 20*4096 || r.Download != 20*4096 {
					t.Fatalf("uid %d wrong counters: %+v", uid, r)
				}
			}
			if r := counts[2]; r.Upload != 0 || r.Download != 0 {
				t.Fatalf("other user charged: %+v", r)
			}
			t.Log("40 real TCP exchanges with concurrent user churn; per-user upload/download exact; revoked authentication denied")
		})
	}
}
