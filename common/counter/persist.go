package counter

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	"sync"
)

type trafficSample struct {
	storage  *TrafficStorage
	up, down int64
}

// Snapshot pins the original counter references and user attribution. A caller
// holds its node gate from PrepareSnapshot through Persist, including reaping;
// identity locks are needed only during preparation. Never reset or destroy the
// same node's counters outside that gate. Writers continue to append atomically.
type Snapshot struct {
	samples []trafficSample
	rows    []panel.UserTraffic
}

func PrepareSnapshot(c *TrafficCounter, uid func(string) int, minimum func(string) int64) *Snapshot {
	snapshot := new(Snapshot)
	indexes := make(map[int]int)
	c.Counters.Range(func(key, value any) bool {
		id := uid(key.(string))
		if id == 0 {
			return true
		}
		s := value.(*TrafficStorage)
		up, down, ready := s.Sample(minimum(key.(string)), false)
		if !ready {
			return true
		}
		snapshot.samples = append(snapshot.samples, trafficSample{s, up, down})
		if index, ok := indexes[id]; ok {
			snapshot.rows[index].Upload += up
			snapshot.rows[index].Download += down
		} else {
			indexes[id] = len(snapshot.rows)
			snapshot.rows = append(snapshot.rows, panel.UserTraffic{UID: id, Upload: up, Download: down})
		}
		return true
	})
	return snapshot
}

// Persist is called once, without core identity locks. Only a successful durable
// save subtracts the sampled bytes; concurrent increments and failed saves remain.
func (snapshot *Snapshot) Persist(persist func([]panel.UserTraffic) error) error {
	if len(snapshot.rows) == 0 {
		return nil
	}
	if err := persist(snapshot.rows); err != nil {
		return err
	}
	for _, s := range snapshot.samples {
		s.storage.UpCounter.Add(-s.up)
		s.storage.DownCounter.Add(-s.down)
	}
	return nil
}

// PersistWithIdentityLock holds the identity lock only during sampling. If a
// published checkpoint lacked a durability confirmation, retry that exact UID
// assignment before observing any new increments, even during a final drain.
func (c *TrafficCounter) PersistWithIdentityLock(identity sync.Locker, uid func(string) int, minimum func(string) int64, persist func([]panel.UserTraffic) error) error {
	if c.unconfirmed != nil {
		if err := c.unconfirmed.Persist(persist); err != nil {
			return err
		}
		c.unconfirmed = nil
	}
	if identity != nil {
		identity.Lock()
	}
	sample := PrepareSnapshot(c, uid, minimum)
	if identity != nil {
		identity.Unlock()
	}
	err := sample.Persist(persist)
	if errors.Is(err, trafficbatch.ErrUnconfirmedSave) {
		c.unconfirmed = sample
	}
	return err
}

// PersistSnapshot is for callers with immutable identity mappings and their own node gate.
func PersistSnapshot(c *TrafficCounter, uid func(string) int, minimum func(string) int64, persist func([]panel.UserTraffic) error) error {
	return c.PersistWithIdentityLock(nil, uid, minimum, persist)
}
