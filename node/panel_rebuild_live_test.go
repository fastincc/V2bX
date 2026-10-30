//go:build sing

package node

import (
	"bytes"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	singcore "github.com/InazumaV/V2bX/core/sing"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPanelRebuildRealBindFailureAndRecovery(t *testing.T) {
	limiter.Init()
	oldSocket, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	oldPort := oldSocket.Addr().(*net.TCPAddr).Port
	oldSocket.Close()
	occupied, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer occupied.Close()
	var desired atomic.Int64
	desired.Store(int64(oldPort))
	user := "synthetic-real-rebuild-user"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config":
			fmt.Fprintf(w, `{"server_port":%d,"cipher":"aes-128-gcm","base_config":{"pull_interval":3600,"push_interval":3600}}`, desired.Load())
		case "/api/v1/server/UniProxy/user":
			fmt.Fprintf(w, `{"users":[{"id":1,"uuid":%q}]}`, user)
		case "/api/v1/server/UniProxy/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			fmt.Fprint(w, `{"data":true}`)
		}
	}))
	defer srv.Close()
	api, e := panel.New(&conf.ApiConfig{APIHost: srv.URL, NodeType: "shadowsocks", NodeID: 1, Key: "synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	core, e := singcore.New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close()
	if e = core.Start(); e != nil {
		t.Fatal(e)
	}
	opt := &conf.Options{}
	opt.Name = "rebuild-live"
	opt.ListenIP = "127.0.0.1"
	opt.SingOptions = &conf.SingOptions{}
	opt.TrafficReportingMode = "off"
	opt.TrafficSpoolDirectory = t.TempDir()
	c := NewController(core, api, opt)
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	defer c.Close()
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
	exchange := func(port int) {
		t.Helper()
		raw, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(2 * time.Second))
		m, e := shadowaead.New("aes-128-gcm", nil, user)
		if e != nil {
			t.Fatal(e)
		}
		conn, e := m.DialConn(raw, M.ParseSocksaddr(echo.Addr().String()))
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		p := bytes.Repeat([]byte{7}, 4096)
		if _, e = conn.Write(p); e != nil {
			t.Fatal(e)
		}
		got := make([]byte, len(p))
		if _, e = io.ReadFull(conn, got); e != nil || !bytes.Equal(p, got) {
			t.Fatalf("proxy failed: %v", e)
		}
	}
	exchange(oldPort)
	desired.Store(int64(occupied.Addr().(*net.TCPAddr).Port))
	if e = c.nodeInfoMonitor(); e != nil {
		t.Fatal(e)
	}
	if c.info.Common.ServerPort != oldPort || c.rebuildRecovery != nil {
		t.Fatal("failed bind did not restore applied node")
	}
	exchange(oldPort)
	// Return panel to the old body: hash-based no-change must still leave a live node.
	desired.Store(int64(oldPort))
	if e = c.nodeInfoMonitor(); e != nil {
		t.Fatal(e)
	}
	exchange(oldPort)
	// Receiving the response precedes the server's final Write return/count.
	// Wait for accounting completion instead of sampling that in-flight window.
	var up, down int64
	deadline := time.Now().Add(time.Second)
	for {
		rows, e := core.GetUserTrafficSlice(c.tag, false)
		if e != nil {
			t.Fatal(e)
		}
		up, down = 0, 0
		for _, r := range rows {
			up += r.Upload
			down += r.Download
		}
		if up == 3*4096 && down == 3*4096 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rebuild accounting: %d/%d", up, down)
		}
		time.Sleep(time.Millisecond)
	}
	t.Log("real panel HTTP + real port collision + restored SS proxy; both directions exactly 12288 bytes")
}
