package counter

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"testing"
)

func TestPersistSnapshotCommitAndFailure(t *testing.T) {
	c := NewTrafficCounter()
	c.Tx("user", 100)
	c.Rx("user", 200)
	resolve := func(string) int { return 1 }
	err := PersistSnapshot(c, resolve, func(string) int64 { return 0 }, func([]panel.UserTraffic) error { return errors.New("disk full") })
	if err == nil || c.GetUpCount("user") != 100 || c.GetDownCount("user") != 200 {
		t.Fatal("failed write drained counters")
	}
	err = PersistSnapshot(c, resolve, func(string) int64 { return 0 }, func(rows []panel.UserTraffic) error {
		if len(rows) != 1 || rows[0].Upload != 100 || rows[0].Download != 200 {
			t.Fatal("wrong snapshot")
		}
		c.Tx("user", 50)
		c.Rx("user", 70)
		return nil
	})
	if err != nil || c.GetUpCount("user") != 50 || c.GetDownCount("user") != 70 {
		t.Fatal("new bytes lost during persistence")
	}
}

func TestPersistSnapshotAggregatesRotatedIdentity(t *testing.T) {
	c := NewTrafficCounter()
	c.Tx("old-password", 3)
	c.Tx("new-password", 5)
	err := PersistSnapshot(c, func(string) int { return 1 }, func(string) int64 { return 0 }, func(rows []panel.UserTraffic) error {
		if len(rows) != 1 || rows[0].Upload != 8 {
			t.Fatal("rotated identities not aggregated")
		}
		return nil
	})
	if err != nil || c.GetUpCount("old-password") != 0 || c.GetUpCount("new-password") != 0 {
		t.Fatal("wrong snapshot commit")
	}
}
