package sing

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"time"
)

func (b *Sing) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	if value, ok := b.hookServer.counter.Load(tag); ok {
		err := counter.PersistSnapshot(value.(*counter.TrafficCounter), func(uuid string) int { return b.users.uidMap[userKey{tag, uuid}] }, func(string) int64 { return 0 }, persist)
		if err == nil {
			b.reapTrafficLocked(tag)
		}
		return err
	}
	b.reapTrafficLocked(tag)
	return nil
}

// Reclaim only accounting generations with no possible writers and no byte tail.
func (b *Sing) reapTrafficLocked(tag string) {
	v, ok := b.hookServer.counter.Load(tag)
	var counts *counter.TrafficCounter
	if ok {
		counts = v.(*counter.TrafficCounter)
	}
	scanned := 0
	for key := range b.users.retired {
		if key.tag != tag {
			continue
		}
		scanned++
		if scanned > 256 {
			break
		}
		if counts != nil {
			if v, ok := counts.Counters.Load(key.uuid); ok {
				s := v.(*counter.TrafficStorage)
				if s.Owners.Load() != 0 || s.UpCounter.Load() != 0 || s.DownCounter.Load() != 0 {
					continue
				}
				counts.Counters.Delete(key.uuid)
			}
		}
		delete(b.users.retired, key)
		delete(b.users.uidMap, key)

	}
}
func (b *Sing) ReclaimTraffic(tag string) {
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	b.reapTrafficLocked(tag)
}

// Stop acquisition, close tracked writers, then wait for their in-flight counts.
func (b *Sing) DrainTraffic(tag string) error {
	b.users.mapLock.Lock()
	if b.users.paused == nil {
		b.users.paused = make(map[userKey]pausedUser)
	}
	for raw, key := range b.users.active {
		if raw.tag == tag {
			b.users.paused[raw] = pausedUser{key: key, uid: b.users.uidMap[userKey{tag, key}]}
			b.users.retired[userKey{tag, key}] = true
			delete(b.users.active, raw)
		}
	}
	b.users.mapLock.Unlock()
	v, ok := b.hookServer.counter.Load(tag)
	if !ok {
		return nil
	}
	counts := v.(*counter.TrafficCounter)
	counts.Counters.Range(func(_, v any) bool { v.(*counter.TrafficStorage).CloseWriters(); return true })
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		counts.Counters.Range(func(_, v any) bool { busy = busy || v.(*counter.TrafficStorage).Owners.Load() != 0; return true })
		if !busy {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("traffic writers did not drain")
		}
		time.Sleep(time.Millisecond * 10)
	}
}

func (b *Sing) ResumeTraffic(tag string) error {
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	for raw, p := range b.users.paused {
		if raw.tag == tag {
			b.users.active[raw] = p.key
			b.users.uidMap[userKey{tag, p.key}] = p.uid
			delete(b.users.retired, userKey{tag, p.key})
			delete(b.users.paused, raw)
		}
	}
	return nil
}
