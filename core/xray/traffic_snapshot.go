package xray

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
)

func (x *Xray) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	defer x.trafficGate.Lock(tag)()
	x.users.mapLock.Lock()
	var snapshot *counter.Snapshot
	if value, ok := x.dispatcher.Counter.Load(tag); ok {
		snapshot = counter.PrepareSnapshot(value.(*counter.TrafficCounter), func(uuid string) int { return x.users.uidMap[uuid] }, func(string) int64 { return 0 })
	}
	x.users.mapLock.Unlock()
	if snapshot != nil {
		if err := snapshot.Persist(persist); err != nil {
			return err
		}
	}
	x.users.mapLock.Lock()
	x.reapTrafficLocked(tag)
	x.users.mapLock.Unlock()
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
