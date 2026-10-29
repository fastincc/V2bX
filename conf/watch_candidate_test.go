package conf

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchCandidatePreservesLastGood(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	valid := `{"Cores":[{"Type":"sing"}],"Nodes":[]}`
	os.WriteFile(path, []byte(valid), 0600)
	c := New()
	if e := c.LoadFromPath(path); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	var reject atomic.Bool
	reject.Store(true)
	w, e := c.watch(path, "", "", func(*Conf) error {
		calls.Add(1)
		if reject.Load() {
			return errors.New("test")
		}
		return nil
	}, 10*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	os.WriteFile(path, []byte(`{`), 0600)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 || len(c.CoresConfig) != 1 {
		t.Fatal("invalid candidate disturbed running state")
	}
	os.WriteFile(path, []byte(valid), 0600)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("apply not attempted")
	}
	reject.Store(false)
	other := filepath.Join(root, "new.json")
	os.WriteFile(other, []byte(valid), 0600)
	os.Rename(other, path)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatal("atomic replace not detected")
	}
}
