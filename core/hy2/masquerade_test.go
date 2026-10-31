package hy2

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func masqFixture(t *testing.T) (*Hysteria2, *panel.NodeInfo, *conf.Options, string) {
	t.Helper()
	dir := t.TempDir()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(16), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	pk, _ := x509.MarshalPKCS8PrivateKey(key)
	cert, kp := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if e := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); e != nil {
		t.Fatal(e)
	}

	tcp, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := tcp.Addr().String()
	tcp.Close()
	udp, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	cfg := filepath.Join(dir, "hy.json")
	data, _ := json.Marshal(map[string]any{"masquerade": map[string]any{"listenHTTPS": addr, "type": "string", "string": map[string]any{"content": "masq-ok", "statusCode": 203}}})
	if e = os.WriteFile(cfg, data, 0600); e != nil {
		t.Fatal(e)
	}
	h := &Hysteria2{Hy2nodes: map[string]Hysteria2node{}, Logger: zap.NewNop()}
	opt := &conf.Options{ListenIP: "127.0.0.1", Hysteria2ConfigPath: cfg, CertConfig: &conf.CertConfig{CertMode: "file", CertFile: cert, KeyFile: kp}}
	info := &panel.NodeInfo{Type: "hysteria2", Common: &panel.CommonNode{ServerPort: port}, Hysteria2: &panel.Hysteria2Node{}}
	t.Cleanup(func() { h.Close() })
	return h, info, opt, addr
}
func TestMasqueradeNodeRebuild(t *testing.T) {
	other := newHy2SessionFixture(t, 1)
	client, e := other.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	stream, e := client.TCP(other.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	h, info, opt, addr := masqFixture(t)
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // generated local certificate
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: time.Second}
	for i := 0; i < 30; i++ {
		if e = h.AddNode("masq", info, opt); e != nil {
			t.Fatal(e)
		}
		response, e := hc.Get("https://" + addr)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 203 || string(b) != "masq-ok" || response.Header.Get("Alt-Svc") != fmt.Sprintf(`h3=":%d"; ma=2592000`, info.Common.ServerPort) {
			t.Fatal("HTTP compatibility")
		}
		if e = hyExchange(stream, []byte("other-node")); e != nil {
			t.Fatal(e)
		}
		if e = h.DelNode("masq"); e != nil {
			t.Fatal(e)
		}
		l, e := net.Listen("tcp", addr)
		if e != nil {
			t.Fatalf("port retained: %v", e)
		}
		l.Close()
	}
	rows, e := other.h.GetUserTrafficSlice(other.tag, false)
	if e != nil || len(rows) != 1 || rows[0].Upload != 300 || rows[0].Download != 300 {
		t.Fatalf("other-node accounting %+v %v", rows, e)
	}
}
func TestMasqueradeRollback(t *testing.T) {
	for _, failure := range []string{"tcp_conflict", "invalid_server"} {
		t.Run(failure, func(t *testing.T) {
			h, info, opt, addr := masqFixture(t)
			if failure == "tcp_conflict" {
				l, e := net.Listen("tcp", addr)
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			} else {
				b, e := os.ReadFile(opt.Hysteria2ConfigPath)
				if e != nil {
					t.Fatal(e)
				}
				var c map[string]any
				json.Unmarshal(b, &c)
				c["udpIdleTimeout"] = "1s"
				b, _ = json.Marshal(c)
				os.WriteFile(opt.Hysteria2ConfigPath, b, 0600)
			}
			if e := h.AddNode("masq", info, opt); e == nil {
				t.Fatal("expected startup error")
			}
			if h.HasNode("masq") {
				t.Fatal("failed node published")
			}
			u, e := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", info.Common.ServerPort))
			if e != nil {
				t.Fatal("UDP retained", e)
			}
			u.Close()
			if failure == "invalid_server" {
				l, e := net.Listen("tcp", addr)
				if e != nil {
					t.Fatal("TCP retained", e)
				}
				l.Close()
			}
		})
	}
}
func TestMasqueradeSecondBindRollback(t *testing.T) {
	first, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a := first.Addr().String()
	first.Close()
	busy, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer busy.Close()
	if _, e = newMasqueradeServers(a, busy.Addr().String(), nil, http.NotFoundHandler(), 443, 443, false, zap.NewNop()); e == nil {
		t.Fatal("expected second bind failure")
	}
	first, e = net.Listen("tcp", a)
	if e != nil {
		t.Fatal("first listener leaked", e)
	}
	first.Close()
}
func TestMasqueradeHijackedAndIdleClose(t *testing.T) {
	hijacked := make(chan net.Conn, 1)
	m, e := newMasqueradeServers("127.0.0.1:0", "", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, rw, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
		rw.Flush()
		hijacked <- c
	}), 443, 443, false, zap.NewNop())
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	m.Start()
	addr := m.endpoints[0].listener.Addr().String()
	c, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: local\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
	br := bufio.NewReader(c)
	for {
		line, e := br.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		if line == "\r\n" {
			break
		}
	}
	owned := <-hijacked
	idle, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	defer idle.Close()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Close() }()
	}
	wg.Wait()
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("hijacked client survived")
	}
	owned.Close()
	l, e := net.Listen("tcp", addr)
	if e != nil {
		t.Fatal("listener retained", e)
	}
	l.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.connections) != 0 {
		t.Fatal("connections retained")
	}
}
func TestMasqueradeRedirect(t *testing.T) {
	m, e := newMasqueradeServers("127.0.0.1:0", "", nil, http.NotFoundHandler(), 443, 8443, true, zap.NewNop())
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	m.Start()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: time.Second}
	response, e := client.Get("http://" + m.endpoints[0].listener.Addr().String() + "/a?b=1")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 301 || !strings.HasSuffix(response.Header.Get("Location"), ":8443/a?b=1") {
		t.Fatal("redirect changed", response.Header)
	}
}

func TestMasqueradeProxyCompatibility(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" || r.Header.Get("X-Forwarded-For") == "" {
			t.Error("proxy request changed")
		}
		w.WriteHeader(201)
		io.WriteString(w, "proxy-ok")
	}))
	defer upstream.Close()
	_, _, opt, _ := masqFixture(t)
	pair, e := tls.LoadX509KeyPair(opt.CertConfig.CertFile, opt.CertConfig.KeyFile)
	if e != nil {
		t.Fatal(e)
	}
	packet, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer packet.Close()
	cfg := &serverConfig{}
	cfg.Masquerade.Type = "proxy"
	cfg.Masquerade.Proxy.URL = upstream.URL
	cfg.Masquerade.ListenHTTPS = "127.0.0.1:0"
	n := &Hysteria2node{Logger: zap.NewNop()}
	if _, e = n.getMasqHandler(&server.TLSConfig{Certificates: []tls.Certificate{pair}}, packet, cfg); e != nil {
		t.Fatal(e)
	}
	defer n.masquerade.Close()
	n.masquerade.Start()
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: time.Second}
	resp, e := hc.Get("https://" + n.masquerade.endpoints[0].listener.Addr().String() + "/hello")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || string(body) != "proxy-ok" {
		t.Fatal("response changed")
	}
}
