package trafficbatch

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
)

func openRecoveryStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func requireStorageOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStorageRecoveryActiveSave(t *testing.T) {
	for _, restart := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "spool")
		s := openRecoveryStore(t, dir)
		requireStorageOK(t, s.Save([]panel.UserTraffic{{UID: 7, Upload: 11, Download: 13}}))
		rows := []panel.UserTraffic{{UID: 9, Upload: 17, Download: 19}, {UID: 7, Upload: 23, Download: 29}}
		s.syncDirectory = func(string) error { return syscall.EIO }
		if err := s.Save(rows); !errors.Is(err, ErrUnconfirmedSave) {
			t.Fatalf("save: %v", err)
		}
		if err := s.Seal(); !errors.Is(err, ErrUnconfirmedSave) {
			t.Fatalf("seal: %v", err)
		}
		if s.Pending() != nil {
			t.Fatal("unconfirmed journal reached delivery")
		}
		id := s.pendingSave.batch.ID
		if restart {
			requireStorageOK(t, s.Close())
			s = openRecoveryStore(t, dir)
		} else {
			if err := s.Save([]panel.UserTraffic{{UID: 9, Upload: 151}}); !errors.Is(err, ErrConflict) {
				t.Fatal("different checkpoint accepted")
			}
			if err := s.Save(rows); !errors.Is(err, syscall.EIO) {
				t.Fatal("ongoing sync error ignored")
			}
			s.syncDirectory = syncDirectory
			requireStorageOK(t, s.Save(rows))
		}
		requireStorageOK(t, s.Save([]panel.UserTraffic{{UID: 7, Download: 31}}))
		requireStorageOK(t, s.Seal())
		want := []panel.UserTraffic{{UID: 7, Upload: 34, Download: 73}, {UID: 9, Upload: 17, Download: 19}}
		got := s.Pending()
		if got == nil || got.ID != id || !slices.Equal(got.Traffic, want) {
			t.Fatalf("restart=%v: %#v", restart, got)
		}
	}
}

func TestStorageRecoverySealAndAck(t *testing.T) {
	for _, restart := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "spool")
		s := openRecoveryStore(t, dir)
		rows := []panel.UserTraffic{{UID: 5, Upload: 53, Download: 31}}
		requireStorageOK(t, s.Save(rows))
		id := s.active.ID
		s.syncDirectory = func(string) error { return syscall.EIO }
		if !errors.Is(s.Seal(), syscall.EIO) || s.Pending() != nil {
			t.Fatal("unconfirmed seal sent or failure ignored")
		}
		if !errors.Is(s.Save(rows), syscall.EIO) {
			t.Fatal("mutated pending seal")
		}
		if restart {
			requireStorageOK(t, s.Close())
			s = openRecoveryStore(t, dir)
		} else {
			s.syncDirectory = syncDirectory
		}
		newRows := []panel.UserTraffic{{UID: 5, Download: 9}}
		requireStorageOK(t, s.Save(newRows))
		requireStorageOK(t, s.Seal())
		if n, a := s.Counts(); n != 2 || a || s.Pending().ID != id || !slices.Equal(s.Pending().Traffic, rows) {
			t.Fatal("seal changed head")
		}
		s.syncDirectory = func(string) error { return syscall.EIO }
		if !errors.Is(s.Acknowledge(id), syscall.EIO) || s.Pending().ID != id {
			t.Fatal("head advanced before durable ACK")
		}
		if restart {
			requireStorageOK(t, s.Close())
			s = openRecoveryStore(t, dir)
		} else {
			s.syncDirectory = syncDirectory
			requireStorageOK(t, s.Acknowledge(id))
		}
		if n, a := s.Counts(); n != 1 || a || !slices.Equal(s.Pending().Traffic, newRows) {
			t.Fatal("successor changed after acknowledgement")
		}
	}
}

func TestStorageRecoveryConflictingCandidate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s := openRecoveryStore(t, dir)
	rows := []panel.UserTraffic{{UID: 5, Upload: 8}}
	s.syncDirectory = func(string) error { return syscall.EIO }
	if !errors.Is(s.Save(rows), ErrUnconfirmedSave) {
		t.Fatal("missing uncertain checkpoint")
	}
	key := filepath.Join(dir, "active.json")
	data, err := os.ReadFile(key)
	requireStorageOK(t, err)
	requireStorageOK(t, os.WriteFile(key, []byte("{}"), 0600))
	s.syncDirectory = syncDirectory
	if s.Save(rows) == nil || s.Pending() != nil {
		t.Fatal("conflicting journal confirmed")
	}
	got, err := os.ReadFile(key)
	requireStorageOK(t, err)
	if string(got) != "{}" {
		t.Fatal("conflicting contents overwritten")
	}
	requireStorageOK(t, os.WriteFile(key, data, 0600))
	requireStorageOK(t, s.Save(rows))
	requireStorageOK(t, s.Seal())
	if !slices.Equal(s.Pending().Traffic, rows) {
		t.Fatal("wrong recovered traffic")
	}
}

func TestStorageRecoveryRecognizesExactPreimage(t *testing.T) {
	for _, hasBase := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "spool")
		s := openRecoveryStore(t, dir)
		file := filepath.Join(dir, "active.json")
		var previous []byte
		if hasBase {
			requireStorageOK(t, s.Save([]panel.UserTraffic{{UID: 7, Upload: 4}}))
			var err error
			previous, err = os.ReadFile(file)
			requireStorageOK(t, err)
		}
		rows := []panel.UserTraffic{{UID: 7, Upload: 11, Download: 3}}
		s.syncDirectory = func(string) error { return syscall.EIO }
		if !errors.Is(s.Save(rows), ErrUnconfirmedSave) {
			t.Fatal("missing uncertain checkpoint")
		}
		id := s.pendingSave.batch.ID
		if hasBase {
			requireStorageOK(t, os.WriteFile(file, previous, 0600))
		} else {
			requireStorageOK(t, os.Remove(file))
		}
		s.syncDirectory = syncDirectory
		requireStorageOK(t, s.Save(rows))
		requireStorageOK(t, s.Seal())
		want := int64(11)
		if hasBase {
			want += 4
		}
		got := s.Pending()
		if got.ID != id || got.Traffic[0].Upload != want || got.Traffic[0].Download != 3 {
			t.Fatalf("base=%v got=%#v", hasBase, got)
		}
	}
}

func TestStorageRecoverySealDestinationConflict(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s := openRecoveryStore(t, dir)
	requireStorageOK(t, s.Save([]panel.UserTraffic{{UID: 9, Download: 15}}))
	name := "batch-00000000000000000001-" + s.active.ID + ".json"
	path := filepath.Join(dir, name)
	requireStorageOK(t, os.WriteFile(path, []byte("unexpected content"), 0600))
	if !errors.Is(s.Seal(), ErrConflict) || s.Pending() != nil {
		t.Fatal("overwrote a conflicting batch")
	}
	got, err := os.ReadFile(path)
	requireStorageOK(t, err)
	if string(got) != "unexpected content" {
		t.Fatal("conflict was altered")
	}
}

func TestStorageRecoveryInterruptedLegacyMigration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s := openRecoveryStore(t, dir)
	requireStorageOK(t, s.Save([]panel.UserTraffic{{UID: 8, Upload: 10, Download: 5}}))
	requireStorageOK(t, s.Seal())
	id := s.Pending().ID
	existing, err := os.ReadFile(filepath.Join(dir, s.queue[0].name))
	requireStorageOK(t, err)
	requireStorageOK(t, s.Close())
	// Legacy batch was copied but migration marker was not yet replaced.
	requireStorageOK(t, os.WriteFile(filepath.Join(dir, "pending.json"), existing, 0600))
	s = openRecoveryStore(t, dir)
	if n, a := s.Counts(); n != 1 || a || s.Pending().ID != id {
		t.Fatal("legacy migration duplicated batch")
	}
}
