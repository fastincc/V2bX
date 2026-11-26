package counter

import (
	"errors"
	"slices"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
)

func TestStorageRecoveryPinsCounterGeneration(t *testing.T) {
	c := NewTrafficCounter()
	c.Tx("test-generation", 40)
	c.Rx("test-generation", 70)
	uid := 1
	resolve := func(string) int { return uid }
	threshold := func(string) int64 { return 0 }
	err := PersistSnapshot(c, resolve, threshold, func([]panel.UserTraffic) error { return trafficbatch.ErrUnconfirmedSave })
	if !errors.Is(err, trafficbatch.ErrUnconfirmedSave) {
		t.Fatal(err)
	}
	c.Tx("test-generation", 13)
	c.Rx("test-generation", 17)
	uid = 2
	var committed []panel.UserTraffic
	err = PersistSnapshot(c, resolve, threshold, func(rows []panel.UserTraffic) error {
		committed = append(committed, rows...)
		return nil
	})
	want := []panel.UserTraffic{{UID: 1, Upload: 40, Download: 70}, {UID: 2, Upload: 13, Download: 17}}
	if err != nil || !slices.Equal(committed, want) || c.GetUpCount("test-generation") != 0 || c.GetDownCount("test-generation") != 0 {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
}
