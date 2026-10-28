package trafficbatch

import (
	"github.com/InazumaV/V2bX/api/panel"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRestartLockAndAcknowledgement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s, e := Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Open(dir, "node"); e == nil {
		t.Fatal("concurrent writer allowed")
	}
	rows := []panel.UserTraffic{{UID: 1, Upload: 123, Download: 456}}
	if e = s.Save(rows); e != nil {
		t.Fatal(e)
	}
	if e = s.Seal(); e != nil {
		t.Fatal(e)
	}
	id := s.Pending().ID
	rows[0].Upload = 999
	if s.Pending().Traffic[0].Upload != 123 {
		t.Fatal("mutable batch")
	}
	if e = s.Acknowledge("wrong"); e == nil {
		t.Fatal("wrong acknowledgement accepted")
	}
	s.Close()
	s, e = Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.Pending().ID != id || s.Pending().Traffic[0].Upload != 123 {
		t.Fatal("restart lost batch")
	}
	if e = s.Acknowledge(id); e != nil {
		t.Fatal(e)
	}
	if n, active := s.Counts(); n != 0 || active {
		t.Fatal("acknowledged batch retained")
	}
	if _, e = os.Stat(filepath.Join(dir, "pending.json")); e != nil {
		t.Fatal("downgrade protection missing")
	}
}
func TestStoreRejectsWrongBindingAndCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s, e := Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 1}})
	s.Close()
	if _, e = Open(dir, "other"); e == nil {
		t.Fatal("wrong node binding accepted")
	}
	os.WriteFile(filepath.Join(dir, "pending.json"), []byte("broken"), 0600)
	if _, e = Open(dir, "node"); e == nil {
		t.Fatal("corruption ignored")
	}
}

func TestStoreWriteFailureRequiresRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s, e := Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = os.Mkdir(filepath.Join(dir, "active.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = s.Save([]panel.UserTraffic{{UID: 1, Upload: 1}}); e == nil {
		t.Fatal("failed rename ignored")
	}
	os.Remove(filepath.Join(dir, "active.json"))
	if e = s.Save([]panel.UserTraffic{{UID: 1, Upload: 1}}); e == nil {
		t.Fatal("ambiguous store failure reused")
	}
}
