package counter

import "github.com/InazumaV/V2bX/api/panel"

// PersistSnapshot must run under the core's exclusive accounting/identity lock.
// Data-plane Add operations remain lock-free while storage is syncing.
func PersistSnapshot(c *TrafficCounter, uid func(string) int, minimum func(string) int64, persist func([]panel.UserTraffic) error) error {
	type sample struct {
		storage  *TrafficStorage
		up, down int64
	}
	var samples []sample
	var rows []panel.UserTraffic
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
		samples = append(samples, sample{s, up, down})
		if index, ok := indexes[id]; ok {
			rows[index].Upload += up
			rows[index].Download += down
		} else {
			indexes[id] = len(rows)
			rows = append(rows, panel.UserTraffic{UID: id, Upload: up, Download: down})
		}
		return true
	})
	if len(rows) == 0 {
		return nil
	}
	if err := persist(rows); err != nil {
		return err
	}
	for _, s := range samples {
		s.storage.UpCounter.Add(-s.up)
		s.storage.DownCounter.Add(-s.down)
	}
	return nil
}
