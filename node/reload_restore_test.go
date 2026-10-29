package node

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	core "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"testing"
	"time"
)

type closeRestoreCore struct {
	core.Core
	live    map[string]bool
	fail    string
	resumes int
}

func (f *closeRestoreCore) DrainTraffic(tag string) error {
	if tag == f.fail {
		return errors.New("injected drain failure")
	}
	return nil
}
func (f *closeRestoreCore) ResumeTraffic(string) error { f.resumes++; return nil }
func (f *closeRestoreCore) PersistTrafficSnapshot(string, func([]panel.UserTraffic) error) error {
	return nil
}
func (f *closeRestoreCore) DelNode(tag string) error { delete(f.live, tag); return nil }
func (f *closeRestoreCore) AddNode(tag string, _ *panel.NodeInfo, _ *conf.Options) error {
	f.live[tag] = true
	return nil
}
func (f *closeRestoreCore) AddUsers(*core.AddUsersParams) (int, error) { return 0, nil }
func TestReloadDrainFailureRestoresAllNodes(t *testing.T) {
	for _, order := range [][]string{{"bad", "good"}, {"good", "bad"}} {
		t.Run(order[0], func(t *testing.T) {
			limiter.Init()
			f := &closeRestoreCore{live: map[string]bool{"good": true, "bad": true}, fail: "bad"}
			n := New()
			for i, tag := range order {
				api, _ := panel.New(&conf.ApiConfig{APIHost: "http://127.0.0.1:1", NodeType: "anytls", NodeID: i + 1})
				c := NewController(f, api, &conf.Options{TrafficSpoolDirectory: t.TempDir()})
				c.tag = tag
				c.info = &panel.NodeInfo{PullInterval: time.Hour, PushInterval: time.Hour}
				c.nodeRemoved = false
				c.limiter = limiter.AddLimiter(tag, &conf.LimitConfig{}, nil, nil)
				if tag == "bad" {
					c.reliableTrafficEnabled = true
					if e := c.openTrafficSpool(); e != nil {
						t.Fatal(e)
					}
				}
				n.controllers = append(n.controllers, c)
			}
			if e := n.CloseForReload(); e == nil {
				t.Fatal("failure hidden")
			}
			if len(n.controllers) != 2 || !f.live["good"] || !f.live["bad"] {
				t.Fatal("partial shutdown retained")
			}
			for _, c := range n.controllers {
				if c.closed {
					t.Fatal("controller remained stopped")
				}
			}
			if f.resumes == 0 {
				t.Fatal("failed drain not resumed")
			}
			f.fail = ""
			if e := n.Close(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
