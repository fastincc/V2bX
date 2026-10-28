package core

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
)

// TrafficSnapshotter persists bytes before subtracting them from live counters.
// A failed persistence callback leaves all counters untouched.
type TrafficSnapshotter interface {
	PersistTrafficSnapshot(string, func([]panel.UserTraffic) error) error
}

func (s *Selector) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	selected, ok := s.nodes.Load(tag)
	if !ok {
		return fmt.Errorf("traffic node not found")
	}
	source, ok := selected.(TrafficSnapshotter)
	if !ok {
		return fmt.Errorf("core lacks persistent traffic snapshots")
	}
	return source.PersistTrafficSnapshot(tag, persist)
}
