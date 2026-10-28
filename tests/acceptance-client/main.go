// Acceptance client for an explicitly selected isolated node. Credentials are
// fetched in memory from the test panel and are never logged or written.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	anytls "github.com/anytls/sing-anytls"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	api := flag.String("panel", "", "isolated panel base URL")
	addr := flag.String("node", "127.0.0.1:25443", "isolated AnyTLS address")
	ca := flag.String("ca", "", "test certificate")
	target := flag.String("target", "127.0.0.1:25444", "isolated echo target")
	phase := flag.String("phase", "echo", "echo or await-revoke")
	uid := flag.Int("uid", 0, "explicit isolated user ID")
	flag.Parse()
	if *api == "" || *ca == "" || *uid <= 0 {
		panic("explicit test panel and CA required")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	response, e := httpClient.Get(*api + "/api/v1/server/UniProxy/user")
	if e != nil {
		panic("test panel unavailable")
	}
	var users struct {
		Users []struct {
			ID       int    `json:"id"`
			Password string `json:"uuid"`
		} `json:"users"`
	}
	e = json.NewDecoder(response.Body).Decode(&users)
	response.Body.Close()
	if e != nil || len(users.Users) != 1 || users.Users[0].ID != *uid {
		panic("unexpected test user response")
	}
	pem, e := os.ReadFile(*ca)
	if e != nil {
		panic(e)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		panic("invalid CA")
	}
	c, e := anytls.NewClient(context.Background(), anytls.ClientConfig{Password: users.Users[0].Password, Logger: log.NewNOPFactory().Logger(), IdleSessionCheckInterval: time.Minute, IdleSessionTimeout: time.Minute, MinIdleSession: 1, DialOut: func(ctx context.Context) (net.Conn, error) {
		raw, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", *addr)
		if e != nil {
			return nil, e
		}
		conn := tls.Client(raw, &tls.Config{RootCAs: roots, ServerName: "test.invalid"})
		if e = conn.HandshakeContext(ctx); e != nil {
			raw.Close()
			return nil, e
		}
		return conn, nil
	}})
	if e != nil {
		panic("client initialization failed")
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, e := c.CreateProxy(ctx, M.ParseSocksaddr(*target))
	if e != nil {
		panic("proxy stream failed")
	}
	defer stream.Close()
	block := bytes.Repeat([]byte("accepted-byte-count-"), 512)
	transfer := func() error {
		stream.SetDeadline(time.Now().Add(3 * time.Second))
		if _, e := stream.Write(block); e != nil {
			return e
		}
		back := make([]byte, len(block))
		if _, e := io.ReadFull(stream, back); e != nil {
			return e
		}
		if !bytes.Equal(block, back) {
			return fmt.Errorf("corruption")
		}
		return nil
	}
	if *phase == "echo" {
		for i := 0; i < 12; i++ {
			if e = transfer(); e != nil {
				panic("echo failed")
			}
		}
		fmt.Printf("{\"phase\":\"echo\",\"bytes_each_direction\":%d,\"verified\":true}\n", 12*len(block))
		return
	}
	if e = transfer(); e != nil {
		panic("initial stream failed")
	}
	fmt.Println(`{"phase":"long-stream-ready"}`)
	until := time.Now().Add(20 * time.Second)
	for time.Now().Before(until) {
		time.Sleep(100 * time.Millisecond)
		if e = transfer(); e != nil {
			denied := false
			retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
			fresh, retryErr := c.CreateProxy(retryCtx, M.ParseSocksaddr(*target))
			if retryErr != nil {
				denied = true
			} else {
				fresh.SetDeadline(time.Now().Add(2 * time.Second))
				_, writeErr := fresh.Write([]byte("new-auth-probe"))
				_, readErr := io.ReadFull(fresh, make([]byte, 14))
				denied = writeErr != nil || readErr != nil
				fresh.Close()
			}
			retryCancel()
			if !denied {
				panic("new connection accepted revoked credentials")
			}
			fmt.Println(`{"phase":"revocation","blocked":true,"new_connection_denied":true}`)
			return
		}
	}
	panic("revoked stream remained usable")
}
