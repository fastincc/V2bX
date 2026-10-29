package xray

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
)

func (x *Xray) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	x.users.mapLock.Lock()
	defer x.users.mapLock.Unlock()
	if value, ok := x.dispatcher.Counter.Load(tag); ok {
		err := counter.PersistSnapshot(value.(*counter.TrafficCounter), func(email string) int { return x.users.uidMap[email] }, func(string) int64 { return 0 }, persist)
		if err == nil {
			x.reapTrafficLocked(tag)
		}
		return err
	}
	return nil
}

func (x *Xray) reapTrafficLocked(tag string) {
	var counts *counter.TrafficCounter
	if v, ok := x.dispatcher.Counter.Load(tag); ok {
		counts = v.(*counter.TrafficCounter)
	}
	scanned := 0
	for key, node := range x.users.retired {
		if node != tag {
			continue
		}
		scanned++
		if scanned > 256 {
			break
		}
		if v, ok := x.dispatcher.LinkManagers.Load(key); ok && !v.(*dispatcher.LinkManager).Quiescent() {
			continue
		}
		if counts != nil {
			if counts.GetUpCount(key) != 0 || counts.GetDownCount(key) != 0 {
				continue
			}
			counts.Delete(key)
		}
		delete(x.users.retired, key)
		delete(x.users.uidMap, key)
		delete(x.users.node, key)
		x.dispatcher.LinkManagers.Delete(key)
	}
}
