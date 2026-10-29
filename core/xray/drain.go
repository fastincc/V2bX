package xray

import (
	"fmt"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
	"time"
)

func (x *Xray) DrainTraffic(tag string) error {
	x.users.mapLock.Lock()
	var managers []*dispatcher.LinkManager
	for key, node := range x.users.node {
		if node == tag {
			if v, ok := x.dispatcher.LinkManagers.Load(key); ok {
				managers = append(managers, v.(*dispatcher.LinkManager))
			}
		}
	}
	for _, m := range managers {
		m.CloseAll()
	}
	x.users.mapLock.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		for _, m := range managers {
			if !m.Quiescent() {
				busy = true
				break
			}
		}
		if !busy {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("xray traffic writers did not drain")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (x *Xray) ResumeTraffic(tag string) error {
	x.users.mapLock.Lock()
	defer x.users.mapLock.Unlock()
	for _, key := range x.users.active {
		if x.users.node[key] == tag {
			if v, ok := x.dispatcher.LinkManagers.Load(key); ok {
				v.(*dispatcher.LinkManager).Resume()
			}
		}
	}
	return nil
}
