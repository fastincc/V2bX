package sing

import (
	"bytes"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	N "github.com/sagernet/sing/common/network"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestShadowsocksUDPAdmissionRateAndAccounting(t *testing.T) {
	limiter.Init()
	tag := "ss-udp-six"
	secret := "synthetic-udp-password"
	user := panel.UserInfo{Id: 41, Uuid: secret, DeviceLimit: 1, SpeedLimit: 1}
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{user}, nil)
	defer limiter.DeleteLimiter(tag)
	c, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	s := c.(*Sing)
	defer s.Close()
	socket, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	port := socket.LocalAddr().(*net.UDPAddr).Port
	socket.Close()
	info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{ServerPort: port}, Shadowsocks: &panel.ShadowsocksNode{Cipher: "aes-128-gcm"}}
	if e = s.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: &conf.SingOptions{}}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AddUsers(&vcore.AddUsersParams{Tag: tag, NodeInfo: info, Users: []panel.UserInfo{user}}); e != nil {
		t.Fatal(e)
	}
	if e = s.Start(); e != nil {
		t.Fatal(e)
	}
	echo, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	defer echo.Close()
	go func() {
		p := make([]byte, 65535)
		for {
			n, a, e := echo.ReadFromUDP(p)
			if e != nil {
				return
			}
			echo.WriteToUDP(p[:n], a)
		}
	}()
	method, e := shadowaead.New("aes-128-gcm", nil, secret)
	if e != nil {
		t.Fatal(e)
	}
	dial := func(ip string) N.NetPacketConn {
		raw, e := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP(ip)}, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if e != nil {
			t.Fatal(e)
		}
		return method.DialPacketConn(raw)
	}
	exchange := func(c N.NetPacketConn, p []byte, timeout time.Duration) error {
		c.SetDeadline(time.Now().Add(timeout))
		if _, e := c.WriteTo(p, echo.LocalAddr()); e != nil {
			return e
		}
		b := make([]byte, 65535)
		n, _, e := c.ReadFrom(b)
		if e != nil {
			return e
		}
		if !bytes.Equal(p, b[:n]) {
			return fmt.Errorf("datagram mismatch")
		}
		return nil
	}
	first := dial("127.0.0.1")
	defer first.Close()
	if e = exchange(first, []byte("first"), 3*time.Second); e != nil {
		t.Fatal(e)
	}
	// A second source port at the same IP consumes no additional device slot.
	same := dial("127.0.0.1")
	defer same.Close()
	if e = exchange(same, []byte("same"), 3*time.Second); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS == "linux" {
		other := dial("127.0.0.2")
		defer other.Close()
		if e = exchange(other, []byte("denied"), 250*time.Millisecond); e == nil {
			t.Fatal("second source IP bypassed device cap")
		}
	} else {
		t.Log("second loopback IP admission is verified by Linux acceptance")
	}
	total := int64(9)
	start := time.Now()
	payload := bytes.Repeat([]byte("p"), 4096)
	for i := 0; i < 56; i++ {
		if e = exchange(first, payload, 3*time.Second); e != nil {
			t.Fatal(e)
		}
		total += int64(len(payload))
	}
	elapsed := time.Since(start)
	if elapsed < time.Second {
		t.Fatal("configured UDP limit bypassed")
	}
	// Both directions must retain the existing payload accounting convention.
	var up, down int64
	if e = s.PersistTrafficSnapshot(tag, func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			if r.UID != 41 {
				t.Fatal("wrong user")
			}
			up += r.Upload
			down += r.Download
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if up != total || down != total {
		t.Fatalf("payload ledger mismatch: %d %d expected %d", up, down, total)
	}
	devices, e := l.GetOnlineDevice()
	if e != nil || len(*devices) != 1 || (*devices)[0].IP != "127.0.0.1" {
		t.Fatal("real source IP/device dedup failed")
	}
	t.Logf("real Shadowsocks UDP: payload=%d each direction, elapsed=%s, configured cap=1 Mbps, one real source IP", total, elapsed.Round(time.Millisecond))
}
