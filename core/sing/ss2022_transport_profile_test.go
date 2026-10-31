//go:build linux || darwin

package sing

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestSS2022TransportProfile(t *testing.T) {
	raw := os.Getenv("SS2022_PROFILE_DURATION")
	if raw == "" {
		t.Skip("set SS2022_PROFILE_DURATION")
	}
	duration, e := time.ParseDuration(raw)
	if e != nil || duration < time.Second || duration > time.Minute {
		t.Fatal("duration")
	}
	for _, n := range []int{16, 32} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			limiter.Init()
			tag := "ss-profile"
			user := panel.UserInfo{Id: 1, Uuid: "synthetic-profile-credential-000000000"}
			limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{user}, nil)
			defer limiter.DeleteLimiter(tag)
			c, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
			if e != nil {
				t.Fatal(e)
			}
			s := c.(*Sing)
			defer s.Close()
			reserve, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := reserve.Addr().(*net.TCPAddr).Port
			reserve.Close()
			cipher := fmt.Sprintf("2022-blake3-aes-%d-gcm", n*8)
			key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, n))
			info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{ServerPort: port}, Shadowsocks: &panel.ShadowsocksNode{Cipher: cipher, ServerKey: key}}
			if e = s.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: &conf.SingOptions{}}); e != nil {
				t.Fatal(e)
			}
			if _, e = s.AddUsers(&vcore.AddUsersParams{Tag: tag, NodeInfo: info, Users: []panel.UserInfo{user}}); e != nil {
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
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, e := echo.Accept()
				if e == nil {
					defer c.Close()
					io.Copy(c, c)
				}
			}()
			method, e := shadowaead_2022.NewWithPassword(cipher, key+":"+base64.StdEncoding.EncodeToString([]byte(user.Uuid[:n])), time.Now)
			if e != nil {
				t.Fatal(e)
			}
			rawConn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if e != nil {
				t.Fatal(e)
			}
			defer rawConn.Close()
			stream, e := method.DialConn(rawConn, M.ParseSocksaddr(echo.Addr().String()))
			if e != nil {
				t.Fatal(e)
			}
			defer stream.Close()
			payload := bytes.Repeat([]byte{42}, 32768)
			response := make([]byte, len(payload))
			var total int64
			var before, after syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_SELF, &before)
			start := time.Now()
			end := start.Add(duration)
			for time.Now().Before(end) {
				stream.SetDeadline(time.Now().Add(3 * time.Second))
				if _, e = stream.Write(payload); e != nil {
					t.Fatal(e)
				}
				if _, e = io.ReadFull(stream, response); e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(payload, response) {
					t.Fatal("payload mismatch")
				}
				total += int64(len(payload))
			}
			elapsed := time.Since(start)
			syscall.Getrusage(syscall.RUSAGE_SELF, &after)
			stream.Close()
			rawConn.Close()
			<-done
			rows, e := s.GetUserTrafficSlice(tag, true)
			if e != nil || len(rows) != 1 || rows[0].Upload != total || rows[0].Download != total {
				t.Fatal("billing mismatch", rows, total, e)
			}
			cpu := func(r syscall.Rusage) float64 {
				return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
			}
			rss := after.Maxrss
			if runtime.GOOS == "linux" {
				rss *= 1024
			}
			out, _ := json.Marshal(map[string]any{"cipher": cipher, "aggregate_mib_s": float64(total*2) / (1 << 20) / elapsed.Seconds(), "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss, "bytes_each_direction": total, "reconciled": true})
			t.Log("SS2022_PROFILE " + string(out))
		})
	}
}
