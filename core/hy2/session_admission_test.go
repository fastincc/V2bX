package hy2

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/quic-go"
	"net"
	"sync"
	"testing"
)

type admissionStream struct{ net.Conn }

func (*admissionStream) StreamID() quic.StreamID { return 1 }
func admissionFixture(t *testing.T) (*V2bX, *HookServer, *serverLogger) {
	t.Helper()
	limiter.Init()
	tag := "session-admission"
	limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 7, Uuid: "synthetic", DeviceLimit: 1}}, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	a := &V2bX{tag: tag, usersMap: map[string]int{}}
	a.addLocked("synthetic", 7)
	return a, &HookServer{Auth: a, Tag: tag}, &serverLogger{Auth: a, Tag: tag}
}
func admissionAddr(ip string) net.Addr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: 1234} }
func TestHy2SessionAdmissionIsolation(t *testing.T) {
	a, h, l := admissionFixture(t)
	good, bad := admissionAddr("192.0.2.1"), admissionAddr("192.0.2.2")
	ok, first := a.Authenticate(good, "synthetic", 0)
	if !ok {
		t.Fatal("first denied")
	}
	ok, second := a.Authenticate(good, "synthetic", 0)
	if !ok || first == second {
		t.Fatal("connections must have separate handles")
	}
	if ok, _ := a.Authenticate(bad, "synthetic", 0); ok {
		t.Fatal("over-limit authentication accepted")
	}
	if !h.LogTraffic(first, 3, 5) || !h.LogTraffic(second, 7, 11) {
		t.Fatal("rejected third connection damaged valid sessions")
	}
	l.checkAdmission(bad, second) // Address changed to a disallowed source.
	l.checkAdmission(good, first)
	l.checkAdmission(good, second) // Rejection is terminal for this connection.
	if h.LogTraffic(second, 100, 100) || !h.LogTraffic(first, 13, 17) {
		t.Fatal("connection decisions interfered")
	}
	counter := h.CounterValueForTest(t)
	if counter[0] != 23 || counter[1] != 33 {
		t.Fatal("wrong direction or rejected traffic counted", counter)
	}
	a.disconnect(first)
	a.disconnect(second)
	if len(a.sessions) != 0 {
		t.Fatal("connection handles leaked")
	}
	if ok, _ := a.Authenticate(nil, "synthetic", 0); ok {
		t.Fatal("unknown source authorized")
	}
}
func (h *HookServer) CounterValueForTest(t *testing.T) [2]int64 {
	t.Helper()
	var up, down int64
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	v, _ := h.Counter.Load(h.Tag)
	if v != nil {
		c := v.(interface {
			GetUpCount(string) int64
			GetDownCount(string) int64
		})
		for id := range h.Auth.identities {
			up += c.GetUpCount(id)
			down += c.GetDownCount(id)
		}
	}
	return [2]int64{up, down}
}
func TestHy2SessionDisconnectTailAndChurn(t *testing.T) {
	a, h, _ := admissionFixture(t)
	addr := admissionAddr("192.0.2.1")
	_, id := a.Authenticate(addr, "synthetic", 0)
	stream := &admissionStream{}
	h.TraceStream(stream, &server.StreamStats{AuthID: id})
	a.disconnect(id)
	if !h.LogTraffic(id, 3, 5) {
		t.Fatal("in-flight TCP tail discarded")
	}
	h.UntraceStream(stream)
	h.UntraceStream(stream)
	if len(a.sessions) != 0 || len(a.streams) != 0 || h.LogTraffic(id, 9, 9) {
		t.Fatal("closed handle retained or accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				ok, id := a.Authenticate(addr, "synthetic", 0)
				if !ok {
					t.Error("same IP denied")
					return
				}
				if !h.LogTraffic(id, 1, 2) {
					t.Error("active flow denied")
				}
				a.disconnect(id)
			}
		}()
	}
	wg.Wait()
	if len(a.sessions) != 0 || len(a.streams) != 0 {
		t.Fatal("churn leaked handles")
	}
	v := h.CounterValueForTest(t)
	if v != [2]int64{8003, 16005} {
		t.Fatal("disconnect lost counters", v)
	}
}
func TestHy2SessionGenerationPersistence(t *testing.T) {
	a, h, _ := admissionFixture(t)
	_, id := a.Authenticate(admissionAddr("192.0.2.1"), "synthetic", 0)
	h.LogTraffic(id, 3, 5)
	core := &Hysteria2{Hy2nodes: map[string]Hysteria2node{a.tag: {Auth: a, TrafficLogger: h}}}
	if e := core.DelUsers([]panel.UserInfo{{Id: 7, Uuid: "synthetic"}}, a.tag, nil); e != nil {
		t.Fatal(e)
	}
	a.mutex.Lock()
	a.addLocked("synthetic", 8)
	a.mutex.Unlock()
	_, fresh := a.Authenticate(admissionAddr("192.0.2.1"), "synthetic", 0)
	if h.LogTraffic(id, 99, 99) || !h.LogTraffic(fresh, 7, 11) {
		t.Fatal("revocation generation crossed")
	}
	fail := errors.New("injected save failure")
	if e := core.PersistTrafficSnapshot(a.tag, func([]panel.UserTraffic) error { return fail }); e != fail {
		t.Fatal(e)
	}
	got := map[int]panel.UserTraffic{}
	if e := core.PersistTrafficSnapshot(a.tag, func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			got[r.UID] = r
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if got[7].Upload != 3 || got[7].Download != 5 || got[8].Upload != 7 || got[8].Download != 11 {
		t.Fatal("attribution or tail changed", got)
	}
	a.disconnect(id)
	a.disconnect(fresh)
}
