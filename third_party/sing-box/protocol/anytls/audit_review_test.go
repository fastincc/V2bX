package anytls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestAuditReviewEmissionAndCallback(t *testing.T) {
	initAuditInstance()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0}, {"eof", io.EOF, 0}, {"closed", net.ErrClosed, 0}, {"pipe", io.ErrClosedPipe, 0}, {"cancel", context.Canceled, 0},
		{"wrapped_eof", fmt.Errorf("copy: %w", io.EOF), 0}, {"joined_closed", errors.Join(io.EOF, net.ErrClosed), 0},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, 1},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, 1},
		{"deadline", context.DeadlineExceeded, 1}, {"unexpected_eof", io.ErrUnexpectedEOF, 1},
		{"dns", &net.DNSError{Err: "no such host", Name: "test.invalid", IsNotFound: true}, 1},
		{"wrapped_join", fmt.Errorf("copy: %w", errors.Join(io.EOF, syscall.ECONNREFUSED)), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			f, e := log.New(log.Options{Context: context.Background(), DefaultWriter: &b, Options: option.LogOptions{Level: "info", DisableColor: true}})
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			h := &Inbound{logger: f.Logger()}
			h.setAuditUser("synthetic-key", "1001")
			tr := newAuditTransport(M.ParseSocksaddr("127.0.0.1:1234"))
			ctx := h.newAuditStream(context.WithValue(context.Background(), auditTransportKey{}, tr), "synthetic-key")
			calls := 0
			close := h.auditAccess(ctx, adapter.InboundContext{Destination: M.ParseSocksaddr("example.test:443")}, "tcp", func(e error) {
				calls++
				if e != tc.err {
					t.Fatal("callback error changed")
				}
			})
			close(tc.err)
			close(tc.err)
			if calls != 2 || strings.Count(b.String(), "AUDIT_ACCESS ") != 1 || strings.Count(b.String(), "AUDIT_ERROR ") != tc.want {
				t.Fatalf("emission mismatch calls=%d log=%s", calls, b.String())
			}
		})
	}
}

func TestAuditReviewKernelErrors(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	l.Close()
	c, e := net.DialTimeout("tcp", addr, time.Second)
	if c != nil {
		c.Close()
	}
	if !errors.Is(e, syscall.ECONNREFUSED) || !auditStreamFailure(e) {
		t.Fatal("real refused error not retained", e)
	}
	for _, reset := range []bool{false, true} {
		l, e = net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		done := make(chan struct{})
		ready := make(chan struct{})
		go func() {
			defer close(done)
			server, err := l.Accept()
			if err != nil {
				return
			}
			defer server.Close()
			<-ready
			if reset {
				server.(*net.TCPConn).SetLinger(0)
			} else {
				time.Sleep(80 * time.Millisecond)
			}
		}()
		c, e = net.Dial("tcp", l.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		close(ready)
		c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		_, e = c.Read(make([]byte, 1))
		c.Close()
		l.Close()
		<-done
		if reset {
			if !errors.Is(e, syscall.ECONNRESET) {
				t.Fatal("expected actual reset", e)
			}
		} else {
			if ne, ok := e.(net.Error); !ok || !ne.Timeout() {
				t.Fatal("expected actual timeout", e)
			}
		}
		if !auditStreamFailure(e) {
			t.Fatal("actual socket failure suppressed", e)
		}
	}
}

func TestAuditReviewIdentitySnapshot(t *testing.T) {
	initAuditInstance()
	h := new(Inbound)
	h.setAuditUser("same-credential", "1001")
	tr := newAuditTransport(M.ParseSocksaddr("[::ffff:198.51.100.9]:1234"))
	ctx := context.WithValue(context.Background(), auditTransportKey{}, tr)
	one := h.newAuditStream(ctx, "same-credential")
	h.setAuditUser("same-credential", "1002")
	two := h.newAuditStream(ctx, "same-credential")
	h.auditUsers.Delete("same-credential")
	three := h.newAuditStream(ctx, "same-credential")
	if *one.Value(auditStreamKey{}).(*auditStream).user != "1001" || *two.Value(auditStreamKey{}).(*auditStream).user != "1002" || three.Value(auditStreamKey{}).(*auditStream).user != nil {
		t.Fatal("identity snapshot mismatch")
	}
	ip, _ := auditAddress(tr.client)
	if *ip != "198.51.100.9" {
		t.Fatal("mapped IPv4 not normalized")
	}
	if !strings.Contains(AuditUserLabel(three, "same-credential"), "user_id=unmapped") {
		t.Fatal("unmapped identity fallback")
	}
}
