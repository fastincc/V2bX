package node

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/conf"
	"github.com/go-acme/lego/v4/certificate"
	log "github.com/sirupsen/logrus"
)

func assertCertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
	}
}

func assertCertHandshake(t *testing.T, certPath, keyPath string) {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(leaf)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	server := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{pair}})
	client := tls.Client(b, &tls.Config{ServerName: "node.example", RootCAs: roots})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.HandshakeContext(ctx) }()
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestCertPrivateIssuanceAndRenewal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "issued.crt")
	firstKey := filepath.Join(dir, "issued.key")
	if err := generateSelfSslCertificate("node.example", first, firstKey); err != nil {
		t.Fatal(err)
	}
	assertCertMode(t, firstKey, 0600)
	assertCertHandshake(t, first, firstKey)
	lego := &Lego{config: &conf.CertConfig{
		CertFile: filepath.Join(dir, "live.crt"), KeyFile: filepath.Join(dir, "live.key"),
	}}
	for i := 0; i < 2; i++ {
		if i == 1 {
			if err := os.Chmod(lego.config.KeyFile, 0640); err != nil {
				t.Fatal(err)
			}
			if err := generateSelfSslCertificate("node.example", first, firstKey); err != nil {
				t.Fatal(err)
			}
		}
		cert, err := os.ReadFile(first)
		if err != nil {
			t.Fatal(err)
		}
		key, err := os.ReadFile(firstKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := lego.writeCert(&certificate.Resource{Certificate: cert, PrivateKey: key}); err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if i == 1 {
			want = 0640
		}
		assertCertMode(t, lego.config.KeyFile, want)
		assertCertHandshake(t, lego.config.CertFile, lego.config.KeyFile)
	}
}

func TestCertPrivateAccountAndLegacyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	cfg := &conf.CertConfig{CertMode: "dns", Email: "fixture-private@example.test"}
	dir := t.TempDir()
	cfg.CertFile = filepath.Join(dir, "cert.pem")
	cfg.KeyFile = filepath.Join(dir, "fixture-private.key")
	if err := generateSelfSslCertificate("node.example", cfg.CertFile, cfg.KeyFile); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	accountPath := legoAccountPath(cfg)
	account := User{Email: cfg.Email, key: key}
	if err := account.Save(accountPath); err != nil {
		t.Fatal(err)
	}
	assertCertMode(t, accountPath, 0600)
	assertCertMode(t, filepath.Dir(accountPath), 0700)
	if err := os.Chmod(filepath.Dir(accountPath), 0750); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cfg.KeyFile, accountPath} {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
	}
	previous := log.StandardLogger().Out
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	ctrl := &Controller{Options: &conf.Options{CertConfig: cfg}}
	if err := ctrl.requestCert(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "TLS private key") || !strings.Contains(output.String(), "ACME account") {
		t.Fatal("missing legacy permission warning")
	}
	encoded, err := EncodePrivate(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{dir, cfg.Email, "fixture-private", "BEGIN", strings.Split(encoded, "\n")[1]} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("private metadata in permission warning")
		}
	}
	assertCertMode(t, accountPath, 0644)
	assertCertMode(t, cfg.KeyFile, 0644)
	assertCertMode(t, filepath.Dir(accountPath), 0750)
	if err := os.Chmod(accountPath, 0640); err != nil {
		t.Fatal(err)
	}
	if err := account.Save(accountPath); err != nil {
		t.Fatal(err)
	}
	assertCertMode(t, accountPath, 0640)
	assertCertMode(t, filepath.Dir(accountPath), 0750)
	loaded, err := NewLegoUser(accountPath, cfg.Email)
	if err != nil || !key.Equal(loaded.GetPrivateKey()) {
		t.Fatal("ACME account did not round-trip")
	}
	cfg.CertMode = "file"
	externalKey := cfg.KeyFile
	cfg.KeyFile = filepath.Join(dir, "external-link.key")
	if err := os.Symlink(externalKey, cfg.KeyFile); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := ctrl.requestCert(); err != nil || output.Len() != 0 {
		t.Fatal("external certificate handling changed")
	}
	target, err := os.Readlink(cfg.KeyFile)
	if err != nil || target != externalKey {
		t.Fatal("external symlink changed")
	}
	assertCertMode(t, externalKey, 0644)
	assertCertHandshake(t, cfg.CertFile, cfg.KeyFile)
}
