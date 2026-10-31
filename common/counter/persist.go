package counter

import "github.com/InazumaV/V2bX/api/panel"

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

// PersistSnapshot is a convenience for callers with immutable identity mappings
// and their own serialization. Cores use the two-phase form above.
func PersistSnapshot(c *TrafficCounter, uid func(string) int, minimum func(string) int64, persist func([]panel.UserTraffic) error) error {
	return PrepareSnapshot(c, uid, minimum).Persist(persist)
}
