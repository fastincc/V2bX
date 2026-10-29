//go:build hysteria2

package hy2

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"net"
	"strings"
	"testing"
)

func TestLoggerNeverEmitsCredential(t *testing.T) {
	limiter.Init()
	credential := "synthetic-auth-secret"
	limiter.AddLimiter("test", &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: credential}}, nil)
	observed, logs := observer.New(zap.DebugLevel)
	l := &serverLogger{Auth: &V2bX{usersMap: map[string]int{credential: 42}}, Tag: "test", logger: zap.New(observed)}
	addr := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1234}
	e := errors.New("transport " + credential)
	l.Connect(addr, credential, 1)
	l.Disconnect(addr, credential, e)
	l.TCPRequest(addr, credential, "test.invalid:443")
	l.TCPError(addr, credential, "test.invalid:443", e)
	l.UDPRequest(addr, credential, 1, "test.invalid:53")
	l.UDPError(addr, credential, 1, e)
	if len(logs.All()) != 6 {
		t.Fatal("missing events")
	}
	for _, entry := range logs.All() {
		if strings.Contains(fmt.Sprint(entry.ContextMap()), credential) {
			t.Fatal("credential leaked")
		}
		if entry.ContextMap()["user_id"] != "42" {
			t.Fatal("safe user ID missing")
		}
	}
}
