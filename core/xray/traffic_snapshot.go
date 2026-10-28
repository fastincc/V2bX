package xray

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
)

func (x *Xray) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	x.users.mapLock.Lock()
	defer x.users.mapLock.Unlock()
	if value, ok := x.dispatcher.Counter.Load(tag); ok {
		return counter.PersistSnapshot(value.(*counter.TrafficCounter), func(email string) int { return x.users.uidMap[email] }, func(string) int64 { return x.reportMinTraffic(tag) }, persist)
	}
	return nil
}
