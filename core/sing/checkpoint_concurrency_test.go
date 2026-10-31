package sing

import (
	"context"
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"net"
	"sync"
	"testing"
	"time"
)

func TestCheckpointDoesNotBlockAdmissionOrOtherNode(t *testing.T) {
	limiter.Init()
	for _, tag := range []string{"A", "B"} {
		limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic"}}, nil)
		defer limiter.DeleteLimiter(tag)
	}
	users := &UserMap{uidMap: map[userKey]int{{"A", "synthetic"}: 1, {"B", "synthetic"}: 1}, active: map[userKey]string{{"A", "synthetic"}: "synthetic", {"B", "synthetic"}: "synthetic"}}
	hook := &HookServer{users: users}
	b := &Sing{users: users, hookServer: hook}
	counts := counter.LoadOrCreate(&hook.counter, "A")
	counts.Tx("synthetic", 10)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	saved := make(chan error, 1)
	go func() {
		saved <- b.PersistTrafficSnapshot("A", func([]panel.UserTraffic) error { close(entered); <-release; return nil })
	}()
	<-entered
	for _, tag := range []string{"A", "B"} {
		raw, peer := net.Pipe()
		done := make(chan net.Conn, 1)
		go func() {
			done <- hook.RoutedConnection(context.Background(), raw, adapter.InboundContext{Inbound: tag, User: "synthetic", Source: M.ParseSocksaddr("192.0.2.1:1234"), Destination: M.ParseSocksaddr("192.0.2.2:443")}, nil, nil)
		}()
		select {
		case c := <-done:
			c.Close()
		case <-time.After(time.Second):
			finish()
			raw.Close()
			peer.Close()
			t.Fatal("admission waited for disk", tag)
		}
		peer.Close()
	}
	other := make(chan error, 1)
	go func() { other <- b.PersistTrafficSnapshot("B", func([]panel.UserTraffic) error { return nil }) }()
	select {
	case e := <-other:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("other-node checkpoint waited for disk")
	}
	reset := make(chan int64, 1)
	go func() { u, _ := b.GetUserTraffic("A", "synthetic", true); reset <- u }()
	select {
	case <-reset:
		t.Fatal("reset overlapped pending save")
	case <-time.After(20 * time.Millisecond):
	}
	counts.Tx("synthetic", 7)
	finish()
	if e := <-saved; e != nil {
		t.Fatal(e)
	}
	if u := <-reset; u != 7 {
		t.Fatal("concurrent bytes lost or double counted", u)
	}
}
func TestCheckpointFreezesAttributionAcrossRevocation(t *testing.T) {
	users := &UserMap{uidMap: map[userKey]int{{"A", "old"}: 1}, retired: map[userKey]bool{}}
	hook := &HookServer{users: users}
	b := &Sing{users: users, hookServer: hook}
	counts := counter.LoadOrCreate(&hook.counter, "A")
	counts.Tx("old", 10)
	failure := errors.New("injected disk failure")
	e := b.PersistTrafficSnapshot("A", func(rows []panel.UserTraffic) error {
		if len(rows) != 1 || rows[0].UID != 1 || rows[0].Upload != 10 {
			t.Fatal(rows)
		}
		users.mapLock.Lock()
		users.retired[userKey{"A", "old"}] = true
		users.uidMap[userKey{"A", "new"}] = 2
		users.mapLock.Unlock()
		counts.Tx("old", 7)
		counts.Tx("new", 11)
		return failure
	})
	if e != failure {
		t.Fatal(e)
	}
	seen := map[int]int64{}
	if e = b.PersistTrafficSnapshot("A", func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			seen[r.UID] += r.Upload
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if seen[1] != 17 || seen[2] != 11 {
		t.Fatal("wrong generation", seen)
	}
	if counts.Len() != 1 {
		t.Fatal("retired storage not reclaimed", counts.Len())
	}
}
