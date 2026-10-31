package sing

import (
	"context"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"sync"
	"testing"
	"time"
)

func TestAnyTLSBatchUpdateWithActiveTraffic(t *testing.T) {
	capture := new(auditCapture)
	f := newReviewFixtureAudit(t, true, []option.AnyTLSUser{{Name: "keep", Password: "synthetic-keep", AuditID: "123"}}, auditLogger(t, capture), true)
	users := &UserMap{uidMap: map[userKey]int{{"review", "keep"}: 123}, active: map[userKey]string{{"review", "keep"}: "keep"}}
	f.router.hook.users = users
	core := &Sing{users: users, hookServer: &f.router.hook}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := 0; round < 20; round++ {
			add := make([]option.AnyTLSUser, 250)
			for i := range add {
				name := fmt.Sprintf("new-%d-%d", round, i)
				add[i] = option.AnyTLSUser{Name: name, Password: "synthetic-" + name}
			}
			if e := f.inbound.AddUsers(add); e != nil {
				t.Error(e)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	const workers, streams, size = 8, 20, 4096
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := f.clientPassword(t, "synthetic-keep")
			defer c.Close()
			for j := 0; j < streams; j++ {
				s, e := c.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if e != nil {
					t.Error(e)
					return
				}
				s.SetDeadline(time.Now().Add(5 * time.Second))
				p := make([]byte, size)
				if _, e = s.Write(p); e == nil {
					_, e = io.ReadFull(s, p)
				}
				s.Close()
				if e != nil {
					t.Error(e)
					return
				}
			}
		}()
	}
	wg.Wait()
	for i := 0; i < workers*streams; i++ {
		select {
		case <-f.router.done:
		case <-time.After(5 * time.Second):
			t.Fatal("stream cleanup timeout")
		}
		if ip := <-f.router.sources; ip != "198.51.100.9" {
			t.Fatal("real IP changed")
		}
	}
	var rows []panel.UserTraffic
	if e := core.PersistTrafficSnapshot("review", func(v []panel.UserTraffic) error { rows = v; return nil }); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].UID != 123 || rows[0].Upload != workers*streams*size || rows[0].Download != workers*streams*size {
		t.Fatal("concurrent update accounting", rows)
	}
	if n := len(capture.events(t)); n != workers*streams {
		t.Fatal("audit missing", n)
	}
	value, _ := f.router.hook.counter.Load("review")
	if value.(*counter.TrafficCounter).GetUpCount("keep") != 0 {
		t.Fatal("checkpoint not consumed")
	}
	t.Logf("5000 added users, %d actual streams, original real IP/audit retained, %d bytes each direction", workers*streams, workers*streams*size)
}
