package trafficbatch

import (
	"encoding/json"
	"github.com/InazumaV/V2bX/api/panel"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalContinuesDuringPendingAndRestarts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	os.Mkdir(dir, 0700)
	s, e := Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 100}})
	s.Seal()
	first := s.Pending()
	for i := 0; i < 10; i++ {
		if e = s.Save([]panel.UserTraffic{{UID: 1, Upload: 7}}); e != nil {
			t.Fatal(e)
		}
	}
	if s.Pending().ID != first.ID || s.Pending().Traffic[0].Upload != 100 {
		t.Fatal("pending changed")
	}
	s.Close()
	s, e = Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Seal()
	if n, active := s.Counts(); n != 2 || active {
		t.Fatal("journal lost")
	}
	s.Acknowledge(first.ID)
	if s.Pending().Traffic[0].Upload != 70 {
		t.Fatal("new traffic lost")
	}
}
func TestLegacyMigrationPreservesIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	os.Mkdir(dir, 0700)
	b := Batch{Version: 1, Binding: "node", ID: "00112233445566778899aabbccddeeff", Traffic: []panel.UserTraffic{{UID: 1, Upload: 37}}}
	data, _ := json.Marshal(b)
	os.WriteFile(filepath.Join(dir, "pending.json"), data, 0600)
	s, e := Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.Pending().ID != b.ID || s.Pending().Traffic[0].Upload != 37 {
		t.Fatal("migration changed payload")
	}
	marker, _ := os.ReadFile(filepath.Join(dir, "pending.json"))
	var v Batch
	json.Unmarshal(marker, &v)
	if v.Version != 2 {
		t.Fatal("old binary would accept queue")
	}
}
func TestQueueFullCanDrainAndKeepJournal(t *testing.T) {
	s, e := OpenWithLimits(filepath.Join(t.TempDir(), "spool"), "node", 4096, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 1}})
	s.Seal()
	id := s.Pending().ID
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 2}})
	if s.Seal() == nil {
		t.Fatal("capacity ignored")
	}
	s.Acknowledge(id)
	if e = s.Seal(); e != nil {
		t.Fatal(e)
	}
	if s.Pending().Traffic[0].Upload != 2 {
		t.Fatal("full queue lost journal")
	}
}
func TestInterruptedSealRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	os.Mkdir(dir, 0700)
	s, e := Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 9}})
	if e = s.enqueue(s.active); e != nil {
		t.Fatal(e)
	}
	s.Close() // crash before retiring active
	s, e = Open(dir, "node")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if n, a := s.Counts(); n != 1 || a {
		t.Fatal("duplicate recovered journal")
	}
}
func BenchmarkJournalCheckpoint(b *testing.B) {
	s, e := Open(filepath.Join(b.TempDir(), "spool"), "node")
	if e != nil {
		b.Fatal(e)
	}
	defer s.Close()
	rows := make([]panel.UserTraffic, 1000)
	for i := range rows {
		rows[i] = panel.UserTraffic{UID: i + 1, Upload: 123, Download: 456}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if e = s.Save(rows); e != nil {
			b.Fatal(e)
		}
	}
}

func TestReportAndCheckpointShareSealWindow(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "spool"), "node")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 1}})
	s.lastSeal = time.Now().Add(-2 * time.Hour)
	if e = s.SealIfDue(time.Hour); e != nil {
		t.Fatal(e)
	}
	first := s.Pending().ID
	s.Save([]panel.UserTraffic{{UID: 1, Upload: 3}})
	s.SealIfDue(time.Hour)
	if n, a := s.Counts(); n != 1 || !a || s.Pending().ID != first {
		t.Fatal("double sealed reporting window")
	}
}
