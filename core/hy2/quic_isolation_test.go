package hy2

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	client "github.com/apernet/hysteria/core/v2/client"
	"go.uber.org/zap"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHysteriaQUICNodeIsolation(t *testing.T) {
	limiter.Init()
	dir := t.TempDir()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	pk, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certfile, keyfile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certfile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyfile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
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
	h := &Hysteria2{Hy2nodes: make(map[string]Hysteria2node), Logger: zap.NewNop()}
	defer h.Close()
	addresses := map[string]*net.UDPAddr{}
	for _, tag := range []string{"quic-A", "quic-B"} {
		socket, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if e != nil {
			t.Fatal(e)
		}
		addr := socket.LocalAddr().(*net.UDPAddr)
		socket.Close()
		addresses[tag] = addr
		users := []panel.UserInfo{{Id: 1, Uuid: tag}, {Id: 2, Uuid: "shared"}}
		limiter.AddLimiter(tag, &conf.LimitConfig{}, users, map[int]int{})
		info := &panel.NodeInfo{Type: "hysteria2", Common: &panel.CommonNode{ServerPort: addr.Port}, Hysteria2: &panel.Hysteria2Node{}}
		options := &conf.Options{ListenIP: "127.0.0.1", CertConfig: &conf.CertConfig{CertMode: "file", CertFile: certfile, KeyFile: keyfile}}
		if e = h.AddNode(tag, info, options); e != nil {
			t.Fatal(e)
		}
		if _, e = h.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: users}); e != nil {
			t.Fatal(e)
		}
	}
	dial := func(tag, identity string) (client.Client, error) {
		c, _, e := client.NewClient(&client.Config{ServerAddr: addresses[tag], Auth: identity, TLSConfig: client.TLSConfig{ServerName: "test.invalid", RootCAs: roots}})
		return c, e
	}
	for _, tag := range []string{"quic-A", "quic-B"} {
		wrong := "quic-A"
		if tag == wrong {
			wrong = "quic-B"
		}
		if c, e := dial(tag, wrong); e == nil {
			c.Close()
			t.Fatal("cross-node QUIC authentication accepted")
		}
		c, e := dial(tag, tag)
		if e != nil {
			t.Fatal(e)
		}
		stream, e := c.TCP(echo.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		stream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, e = stream.Write([]byte("verified")); e != nil {
			t.Fatal(e)
		}
		data := make([]byte, 8)
		if _, e = io.ReadFull(stream, data); e != nil || string(data) != "verified" {
			t.Fatal("QUIC transport failed", e)
		}
		stream.Close()
		c.Close()
	}
	if e = h.DelUsers([]panel.UserInfo{{Id: 2, Uuid: "shared"}}, "quic-A", nil); e != nil {
		t.Fatal(e)
	}
	if c, e := dial("quic-A", "shared"); e == nil {
		c.Close()
		t.Fatal("revoked QUIC identity accepted")
	}
	c, e := dial("quic-B", "shared")
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	t.Log("real QUIC: cross-node denied, own-node echo passed, shared identity isolated on revoke")
}
