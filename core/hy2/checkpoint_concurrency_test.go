package hy2

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"sync"
	"testing"
	"time"
)

func TestCheckpointAllowsTrafficAndRevocation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			limiter.Init()
			tag := "checkpoint"
			u := panel.UserInfo{Id: 1, Uuid: "synthetic"}
			limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{u}, nil)
			defer limiter.DeleteLimiter(tag)
			auth := &V2bX{usersMap: map[string]int{}}
			hook := &HookServer{Auth: auth, Tag: tag}
			h := &Hysteria2{Hy2nodes: map[string]Hysteria2node{tag: {Auth: auth, TrafficLogger: hook}}}
			add := func(uid int) {
				if _, e := h.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: []panel.UserInfo{{Id: uid, Uuid: u.Uuid}}}); e != nil {
					t.Fatal(e)
				}
			}
			add(1)
			id := hyTestID(auth, u.Uuid)
			hook.LogTraffic(id, 10, 20)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			saved := make(chan error, 1)
			failure := errors.New("disk failure")
			go func() {
				saved <- h.PersistTrafficSnapshot(tag, func(rows []panel.UserTraffic) error {
					close(entered)
					<-release
					if fail {
						return failure
					}
					return nil
				})
			}()
			<-entered
			done := make(chan bool, 1)
			go func() { done <- hook.LogTraffic(id, 7, 9) }()
			select {
			case ok := <-done:
				if !ok {
					t.Fatal("denied active user")
				}
			case <-time.After(time.Second):
				t.Fatal("traffic waited for disk")
			}
			if e := h.DelUsers([]panel.UserInfo{u}, tag, nil); e != nil {
				t.Fatal(e)
			}
			if hook.LogTraffic(id, 999, 999) {
				t.Fatal("revoked session accepted")
			}
			add(2)
			newID := hyTestID(auth, u.Uuid)
			if !hook.LogTraffic(newID, 11, 13) {
				t.Fatal("re-added user denied")
			}
			// A lifecycle drain must wait without holding nodesMu/Auth.mutex.
			drained := make(chan error, 1)
			go func() { drained <- h.DrainTraffic(tag) }()
			select {
			case <-drained:
				t.Fatal("lifecycle overlapped save")
			case <-time.After(20 * time.Millisecond):
			}
			if !hook.LogTraffic(newID, 1, 1) {
				t.Fatal("waiting lifecycle blocked valid user")
			}
			finish()
			e := <-saved
			if fail && e != failure || !fail && e != nil {
				t.Fatal(e)
			}
			if e = <-drained; e != nil {
				t.Fatal(e)
			}
			h.ResumeTraffic(tag)
			seen := map[int]panel.UserTraffic{}
			h.PersistTrafficSnapshot(tag, func(rows []panel.UserTraffic) error {
				for _, r := range rows {
					seen[r.UID] = r
				}
				return nil
			})
			want := int64(7)
			if fail {
				want = 17
			}
			if seen[1].Upload != want || seen[2].Upload != 12 {
				t.Fatal("accounting mismatch", seen)
			}
		})
	}
}
