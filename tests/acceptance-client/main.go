// Acceptance client for an explicitly selected isolated node. Credentials are
// fetched in memory from the test panel and are never logged or written.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
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
	"strconv"
	"time"
)

func main() {
	api := flag.String("panel", "", "isolated panel base URL")
	addr := flag.String("node", "127.0.0.1:25443", "isolated AnyTLS address")
	ca := flag.String("ca", "", "test certificate")
	target := flag.String("target", "127.0.0.1:25444", "isolated echo target")
	phase := flag.String("phase", "echo", "echo, hold, or await-revoke")
	apiNode := flag.Int("api-node", 0, "isolated panel node ID")
	proxyVersion := flag.Int("proxy-version", 0, "PROXY protocol version (0, 1, 2)")
	sourceIP := flag.String("source-ip", "198.51.100.10", "declared fixture client address")
	duration := flag.Duration("duration", 20*time.Second, "hold/revocation duration")
	blockSize := flag.Int("block-size", 10240, "bytes in a single Write")
	blocks := flag.Int("blocks", 12, "echo block count")
	wrongPassword := flag.Bool("wrong-password", false, "use a synthetic invalid password")
	uid := flag.Int("uid", 0, "explicit isolated user ID")
	flag.Parse()
	if *api == "" || *ca == "" || *uid <= 0 {
		panic("explicit test panel and CA required")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	userURL := *api + "/api/v1/server/UniProxy/user"
	if *apiNode > 0 {
		userURL += "?node_id=" + strconv.Itoa(*apiNode)
	}
	response, e := httpClient.Get(userURL)
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
	password := users.Users[0].Password
	if *wrongPassword {
		password = "synthetic-wrong-test-password"
	}
	c, e := anytls.NewClient(context.Background(), anytls.ClientConfig{Password: password, Logger: log.NewNOPFactory().Logger(), IdleSessionCheckInterval: time.Minute, IdleSessionTimeout: time.Minute, MinIdleSession: 1, DialOut: func(ctx context.Context) (net.Conn, error) {
		raw, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", *addr)
		if e != nil {
			return nil, e
		}
		if *proxyVersion != 0 {
			ip := net.ParseIP(*sourceIP)
			if ip == nil {
				raw.Close()
				return nil, fmt.Errorf("invalid fixture IP")
			}
			var header []byte
			if *proxyVersion == 1 {
				family, dst := "TCP6", "::1"
				if ip.To4() != nil {
					family, dst = "TCP4", "127.0.0.1"
				}
				header = []byte(fmt.Sprintf("PROXY %s %s %s 42345 26443\r\n", family, ip.String(), dst))
			} else if *proxyVersion == 2 {
				family := byte(0x21)
				address := append(ip.To16(), net.ParseIP("::1").To16()...)
				if ip.To4() != nil {
					family = 0x11
					address = append(ip.To4(), net.IPv4(127, 0, 0, 1).To4()...)
				}
				header = append([]byte("\r\n\r\n\x00\r\nQUIT\n"), 0x21, family, 0, 0)
				binary.BigEndian.PutUint16(header[14:16], uint16(len(address)+4))
				header = append(header, address...)
				header = append(header, 0xa5, 0x69, 0x67, 0x4b)
			} else {
				raw.Close()
				return nil, fmt.Errorf("unsupported fixture PROXY version")
			}
			if _, err := raw.Write(header); err != nil {
				raw.Close()
				return nil, err
			}
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
	ctx, cancel := context.WithTimeout(context.Background(), *duration+30*time.Second)
	defer cancel()
	stream, e := c.CreateProxy(ctx, M.ParseSocksaddr(*target))
	if e != nil {
		panic("proxy stream failed")
	}
	defer stream.Close()
	if *blockSize <= 0 || *blockSize > 8<<20 {
		panic("invalid test block size")
	}
	block := bytes.Repeat([]byte{0x53}, *blockSize)
	transfer := func() error {
		stream.SetDeadline(time.Now().Add(3 * time.Second))

		back := make([]byte, len(block))
		done := make(chan error, 1)
		go func() { _, err := io.ReadFull(stream, back); done <- err }()
		n, err := stream.Write(block)
		if err != nil || n != len(block) {
			stream.Close()
			<-done
			return fmt.Errorf("test write failed")
		}
		if err := <-done; err != nil {
			return err
		}
		if !bytes.Equal(block, back) {
			return fmt.Errorf("corruption")
		}
		return nil
	}
	if *phase == "echo" {
		for i := 0; i < *blocks; i++ {
			if e = transfer(); e != nil {
				panic("echo failed")
			}
		}
		fmt.Printf("{\"phase\":\"echo\",\"bytes_each_direction\":%d,\"verified\":true}\n", *blocks*len(block))
		return
	}
	if e = transfer(); e != nil {
		panic("initial stream failed")
	}
	fmt.Println(`{"phase":"long-stream-ready"}`)
	until := time.Now().Add(*duration)
	for time.Now().Before(until) {
		time.Sleep(100 * time.Millisecond)
		if e = transfer(); e != nil {
			if *phase == "hold" {
				panic("unrelated live stream interrupted")
			}
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
	if *phase == "hold" {
		fmt.Println(`{"phase":"hold","uninterrupted":true}`)
		return
	}
	panic("revoked stream remained usable")
}
