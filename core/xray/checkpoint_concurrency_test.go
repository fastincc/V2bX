package xray

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
	"sync"
	"testing"
	"time"
)

func TestCheckpointSerializesSamplingWithoutHoldingIdentityLock(t *testing.T) {
	x := &Xray{users: &UserMap{uidMap: map[string]int{"old": 1}, retired: map[string]string{}, node: map[string]string{"old": "A"}}, dispatcher: &dispatcher.DefaultDispatcher{}}
	c := counter.LoadOrCreate(&x.dispatcher.Counter, "A")
	c.Tx("old", 10)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	saved := make(chan error, 1)
	go func() {
		saved <- x.PersistTrafficSnapshot("A", func([]panel.UserTraffic) error { close(entered); <-release; return nil })
	}()
	<-entered
	changed := make(chan struct{})
	go func() {
		x.users.mapLock.Lock()
		x.users.retired["old"] = "A"
		x.users.uidMap["new"] = 2
		x.users.node["new"] = "A"
		x.users.mapLock.Unlock()
		close(changed)
	}()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("identity update blocked by storage")
	}
	c.Tx("old", 7)
	c.Tx("new", 11)
	drained := make(chan []panel.UserTraffic, 1)
	go func() { rows, _ := x.GetUserTrafficSlice("A", true); drained <- rows }()
	select {
	case <-drained:
		t.Fatal("concurrent consumer sampled saved bytes")
	case <-time.After(20 * time.Millisecond):
	}
	finish()
	if e := <-saved; e != nil {
		t.Fatal(e)
	}
	rows := <-drained
	seen := map[int]int64{}
	for _, r := range rows {
		seen[r.UID] += r.Upload
	}
	if seen[1] != 7 || seen[2] != 11 {
		t.Fatal("wrong remaining totals", seen)
	}
}
