//go:build sing

package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/InazumaV/V2bX/node"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCandidateBindFailureRestoresRealListener(t *testing.T) {
	limiter.Init()
	free, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	held, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	busyPort := held.Addr().(*net.TCPAddr).Port
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			p := port
			if r.URL.Query().Get("node_id") == "2" {
				p = busyPort
			}
			fmt.Fprintf(w, `{"server_port":%d,"base_config":{"push_interval":3600,"pull_interval":3600}}`, p)
		case strings.HasSuffix(r.URL.Path, "/user"):
			fmt.Fprint(w, `{"users":[{"id":41,"uuid":"synthetic-fixture-password"}]}`)
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			fmt.Fprint(w, `{"data":true}`)
		}
	}))
	defer panel.Close()
	dir := t.TempDir()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	pk, _ := x509.MarshalPKCS8PrivateKey(key)
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600)
	c := conf.New()
	e = json.Unmarshal([]byte(fmt.Sprintf(`{"Cores":[{"Type":"sing"}],"Nodes":[{"Name":"last-good","Core":"sing","NodeType":"anytls","NodeID":1,"ApiHost":%q,"ApiKey":"synthetic","ListenIP":"127.0.0.1","TrafficBatchMode":false,"CertConfig":{"CertMode":"file","CertFile":%q,"KeyFile":%q}}]}`, panel.URL, certFile, keyFile)), c)
	if e != nil {
		t.Fatal(e)
	}
	backup, e := backupConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	defer backup.Close()
	vc, e := newCoreSafely(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = vc.Start(); e != nil {
		t.Fatal(e)
	}
	nodes := node.New()
	defer func() { nodes.Close(); vc.Close() }()
	if e = nodes.Start(c.NodeConfig, vc); e != nil {
		t.Fatal(e)
	}
	candidate := c.Clone()
	second := candidate.NodeConfig[0]
	second.ApiConfig.NodeID = 2
	second.Options.Name = "must-fail"
	candidate.NodeConfig = append(candidate.NodeConfig, second)
	if e = replaceRuntime(&vc, &nodes, backup, candidate); e == nil || errors.Is(e, errRecoveryIncomplete) {
		t.Fatal("failed candidate did not roll back", e)
	}
	conn, e := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if e != nil {
		t.Fatal("last-good listener unavailable", e)
	}
	conn.Close()
	if _, e = limiter.GetLimiter("must-fail"); e == nil {
		t.Fatal("failed candidate limiter leaked")
	}
	t.Log("real sing core: second-node bind failure rolls back first-node service")
}
