// Package trafficbatch stores one immutable, acknowledged traffic batch per node.
package trafficbatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"os"
	"path/filepath"
)

type Batch struct {
	Version int                 `json:"version"`
	Binding string              `json:"binding"`
	ID      string              `json:"id"`
	Traffic []panel.UserTraffic `json:"traffic"`
}
type Store struct {
	dir, binding string
	lock         *os.File
	pending      *Batch
	poisoned     bool
}

func Open(dir, binding string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create traffic spool failed")
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("traffic spool must be a private directory")
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open traffic spool lock failed")
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("traffic spool is locked or unsupported")
	}
	s := &Store{dir: dir, binding: binding, lock: f}
	data, err := os.ReadFile(filepath.Join(dir, "pending.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || len(data) > 64<<20 {
		s.Close()
		return nil, fmt.Errorf("read traffic spool failed")
	}
	var b Batch
	if json.Unmarshal(data, &b) != nil || b.Version != 1 || b.Binding != binding || len(b.ID) != 32 || len(b.Traffic) == 0 {
		s.Close()
		return nil, fmt.Errorf("invalid traffic spool or node binding")
	}
	if _, err = hex.DecodeString(b.ID); err != nil {
		s.Close()
		return nil, fmt.Errorf("invalid batch identity")
	}
	if err = validate(b.Traffic); err != nil {
		s.Close()
		return nil, err
	}
	s.pending = &b
	return s, nil
}
func validate(rows []panel.UserTraffic) error {
	seen := map[int]bool{}
	for _, r := range rows {
		if r.UID <= 0 || r.Upload < 0 || r.Download < 0 || seen[r.UID] {
			return fmt.Errorf("invalid or duplicate traffic identity")
		}
		seen[r.UID] = true
	}
	return nil
}
func (s *Store) Pending() *Batch { return s.pending }
func (s *Store) Save(rows []panel.UserTraffic) error {
	if s.poisoned {
		return fmt.Errorf("traffic spool requires restart after storage failure")
	}
	if s.pending != nil {
		return fmt.Errorf("unacknowledged batch exists")
	}
	if len(rows) == 0 {
		return nil
	}
	if err := validate(rows); err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("batch identity unavailable")
	}
	b := &Batch{Version: 1, Binding: s.binding, ID: hex.EncodeToString(id[:]), Traffic: append([]panel.UserTraffic(nil), rows...)}
	data, err := json.Marshal(b)
	if err != nil || len(data) > 64<<20 {
		return fmt.Errorf("traffic batch too large")
	}
	if err = s.write(data); err != nil {
		s.poisoned = true
		return fmt.Errorf("traffic batch persistence failed; restart required")
	}
	s.pending = b
	return nil
}
func (s *Store) write(data []byte) error {
	f, err := os.CreateTemp(s.dir, "pending-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.dir, "pending.json")); err != nil {
		return err
	}
	return s.syncDir()
}
func (s *Store) syncDir() error {
	f, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) Acknowledge(id string) error {
	if s.pending == nil || id != s.pending.ID {
		return fmt.Errorf("batch acknowledgement mismatch")
	}
	if err := os.Remove(filepath.Join(s.dir, "pending.json")); err != nil {
		return fmt.Errorf("clear acknowledged batch failed")
	}
	if err := s.syncDir(); err != nil {
		s.poisoned = true
		return fmt.Errorf("sync acknowledged batch failed; restart required")
	}
	s.pending = nil
	return nil
}
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}
