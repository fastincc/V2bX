//go:build sing

package node

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	singcore "github.com/InazumaV/V2bX/core/sing"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

type batchLiveCore struct {
	vcore.Core
	deleteCalls int
	failPartial bool
}

func (f *batchLiveCore) DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error {
	f.deleteCalls++
	if f.failPartial {
		f.failPartial = false
		if e := f.Core.DelUsers(users[:1], tag, info); e != nil {
			return e
		}
		return errors.New("injected error after real partial revocation")
	}
	return f.Core.DelUsers(users, tag, info)
}
func (f *batchLiveCore) DrainTraffic(tag string) error {
	return f.Core.(interface{ DrainTraffic(string) error }).DrainTraffic(tag)
}
func (f *batchLiveCore) ResumeTraffic(tag string) error {
	return f.Core.(interface{ ResumeTraffic(string) error }).ResumeTraffic(tag)
}
func (f *batchLiveCore) HasNode(tag string) bool {
	return f.Core.(interface{ HasNode(string) bool }).HasNode(tag)
}
func (f *batchLiveCore) PersistTrafficSnapshot(tag string, save func([]panel.UserTraffic) error) error {
	return f.Core.(vcore.TrafficSnapshotter).PersistTrafficSnapshot(tag, save)
}

func TestPanelRebuildRealPartialRevocation(t *testing.T) {
	c, _, next := batchRebuildFixture(t, 3)
	vc, e := singcore.New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	defer vc.Close()
	core := &batchLiveCore{Core: vc, failPartial: true}
	c.server = core
	socket, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := socket.Addr().(*net.TCPAddr).Port
	socket.Close()
	info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{ServerPort: port}, Shadowsocks: &panel.ShadowsocksNode{Cipher: "aes-128-gcm"}}
	c.info = info
	c.ListenIP = "127.0.0.1"
	c.SingOptions = conf.NewSingOptions()
	next.Type = info.Type
	next.Shadowsocks = info.Shadowsocks
	all := append([]panel.UserInfo(nil), c.userList...)
	if e = core.AddNode(c.tag, info, c.Options); e != nil {
		t.Fatal(e)
	}
	if _, e = core.AddUsers(&vcore.AddUsersParams{Tag: c.tag, NodeInfo: info, Users: all}); e != nil {
		t.Fatal(e)
	}
	if e = core.Start(); e != nil {
		t.Fatal(e)
	}
	echo, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer echo.Close()
	go func() {
		for {
			conn, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	exchange := func(u panel.UserInfo) error {
		method, e := shadowaead.New("aes-128-gcm", nil, u.Uuid)
		if e != nil {
			return e
		}
		raw, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if e != nil {
			return e
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(time.Second))
		conn, e := method.DialConn(raw, M.ParseSocksaddr(echo.Addr().String()))
		if e != nil {
			return e
		}
		if _, e = conn.Write([]byte("ping")); e != nil {
			return e
		}
		b := make([]byte, 4)
		_, e = io.ReadFull(conn, b)
		if e == nil && string(b) != "ping" {
			return errors.New("corrupt echo")
		}
		return e
	}
	for _, u := range all {
		if e = exchange(u); e != nil {
			t.Fatal(e)
		}
	}
	if e = c.rebuildFromPanel(next, all[2:], nil); e == nil {
		t.Fatal("missing partial error")
	}
	if e = c.recoverPanelRebuild(); e != nil {
		t.Fatal(e)
	}
	if core.deleteCalls != 2 || len(c.userList) != 1 {
		t.Fatal("wrong batch recovery")
	}
	for _, u := range all[:2] {
		if exchange(u) == nil {
			t.Fatal("revoked user restored", u.Id)
		}
	}
	if e = exchange(all[2]); e != nil {
		t.Fatal("surviving user not restored", e)
	}
	if e = core.DrainTraffic(c.tag); e != nil {
		t.Fatal(e)
	}
	if e = c.captureTraffic(); e != nil {
		t.Fatal(e)
	}
	if e = c.trafficSpool.Seal(); e != nil {
		t.Fatal(e)
	}
	totals := map[int][2]int64{}
	for batch := c.trafficSpool.Pending(); batch != nil; batch = c.trafficSpool.Pending() {
		for _, r := range batch.Traffic {
			v := totals[r.UID]
			v[0] += r.Upload
			v[1] += r.Download
			totals[r.UID] = v
		}
		if e = c.trafficSpool.Acknowledge(batch.ID); e != nil {
			t.Fatal(e)
		}
	}
	for _, u := range all {
		want := int64(4)
		if u.Id == 3 {
			want = 8
		}
		if totals[u.Id] != [2]int64{want, want} {
			t.Fatal("lost or misattributed tail", totals)
		}
	}
	t.Log("real SS partial revocation/recovery: two rejected users, one surviving user, tails exactly reconciled")
}

// Opt-in comparison using real AnyTLS authentication-table publication. Network
// listeners are not started; this isolates rebuild CPU/allocation cost.
func TestPanelRebuildTableProfile(t *testing.T) {
	text := os.Getenv("REBUILD_PROFILE_USERS")
	if text == "" {
		t.Skip("set REBUILD_PROFILE_USERS")
	}
	n, e := strconv.Atoi(text)
	if e != nil || n < 10 || n > 10000 {
		t.Fatal("invalid users")
	}
	c, _, next := batchRebuildFixture(t, n)
	vc, e := singcore.New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	defer vc.Close()
	core := &batchLiveCore{Core: vc}
	c.server = core
	info := &panel.NodeInfo{Type: "anytls", Common: &panel.CommonNode{}, AnyTls: &panel.AnyTlsNode{}}
	c.info = info
	c.ListenIP = "127.0.0.1"
	c.SingOptions = conf.NewSingOptions()
	c.CertConfig = conf.NewCertConfig()
	next.Type = info.Type
	next.Common = &panel.CommonNode{}
	next.AnyTls = info.AnyTls
	if e = core.AddNode(c.tag, info, c.Options); e != nil {
		t.Fatal(e)
	}
	if _, e = core.AddUsers(&vcore.AddUsersParams{Tag: c.tag, NodeInfo: info, Users: c.userList}); e != nil {
		t.Fatal(e)
	}
	desired := append([]panel.UserInfo(nil), c.userList[n*9/10:]...)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	if e = c.rebuildFromPanel(next, desired, nil); e != nil {
		t.Fatal(e)
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	t.Logf("TABLE_PROFILE users=%d removed=%d delete_calls=%d elapsed_ms=%.3f allocated_bytes=%d allocations=%d", n, n-len(desired), core.deleteCalls, float64(elapsed.Microseconds())/1000, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
}
