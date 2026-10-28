package sing

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
)

func (b *Sing) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	if value, ok := b.hookServer.counter.Load(tag); ok {
		return counter.PersistSnapshot(value.(*counter.TrafficCounter), func(uuid string) int { return b.users.uidMap[userKey{tag, uuid}] }, func(uuid string) int64 {
			if b.users.retired[userKey{tag, uuid}] {
				return 0
			}
			return b.reportMinTraffic(tag)
		}, persist)
	}
	return nil
}
