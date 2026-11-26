// Package trafficbatch persists an accumulating journal and immutable delivery batches.
package trafficbatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
)

type Batch struct {
	Version int                 `json:"version"`
	Binding string              `json:"binding"`
	ID      string              `json:"id"`
	Traffic []panel.UserTraffic `json:"traffic"`
}
type queued struct {
	name  string
	batch *Batch
	size  int64
}
type Store struct {
	lastSeal      time.Time
	mu            sync.Mutex
	dir, binding  string
	lock          *os.File
	active        *Batch
	queue         []queued
	sequence      uint64
	pendingSave   *unconfirmedSave
	pendingSeal   *queued
	ackUnlinked   bool
	verifyAckHead bool
	verifyActive  bool
	syncDirectory func(string) error
	maxBytes      int64
	maxBatches    int
}

func Open(dir, binding string) (*Store, error) { return OpenWithLimits(dir, binding, 64<<20, 4096) }
func OpenWithLimits(dir, binding string, maxBytes int64, maxBatches int) (s *Store, err error) {
	if maxBytes <= 0 || maxBytes > 1<<30 || maxBatches <= 0 || maxBatches > 65536 {
		return nil, fmt.Errorf("invalid spool capacity")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create traffic spool failed")
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("traffic spool must be private")
	}
	f, e := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, fmt.Errorf("open spool lock failed")
	}
	if e = lockFile(f); e != nil {
		f.Close()
		return nil, fmt.Errorf("traffic spool is locked or unsupported")
	}
	s = &Store{dir: dir, binding: binding, lock: f, maxBytes: maxBytes, maxBatches: maxBatches, lastSeal: time.Now(), syncDirectory: syncDirectory}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	var legacy *Batch
	marker, e := os.ReadFile(filepath.Join(dir, "pending.json"))
	if e == nil {
		var b Batch
		if json.Unmarshal(marker, &b) != nil || b.Binding != binding {
			return nil, fmt.Errorf("invalid spool marker or binding")
		}
		if b.Version == 1 {
			if e = validBatch(&b, binding); e != nil {
				return nil, e
			}
			legacy = &b
		} else if b.Version != 2 {
			return nil, fmt.Errorf("unsupported spool version")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, fmt.Errorf("read spool marker failed")
	}
	files, e := filepath.Glob(filepath.Join(dir, "batch-*.json"))
	if e != nil {
		return nil, e
	}
	sort.Strings(files)
	if len(files) > maxBatches {
		return nil, fmt.Errorf("existing queue exceeds configured count; raise limit to drain")
	}
	seen := map[string]bool{}
	seenSequence := map[uint64]bool{}
	for _, p := range files {
		info, e := os.Lstat(p)
		if e != nil || !info.Mode().IsRegular() || s.bytes()+info.Size() > maxBytes {
			return nil, fmt.Errorf("invalid queue file or capacity")
		}
		b, e := readBatch(p, binding)
		if e != nil {
			return nil, e
		}
		parts := strings.Split(filepath.Base(p), "-")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid queue name")
		}
		seq, e := strconv.ParseUint(parts[1], 10, 64)
		if e != nil || seq == 0 || seen[b.ID] || seenSequence[seq] || strings.TrimSuffix(parts[2], ".json") != b.ID {
			return nil, fmt.Errorf("invalid queue sequence or duplicate identity")
		}
		seen[b.ID] = true
		seenSequence[seq] = true
		if seq > s.sequence {
			s.sequence = seq
		}

		s.queue = append(s.queue, queued{filepath.Base(p), b, info.Size()})
	}
	activePath := filepath.Join(dir, "active.json")
	if _, e = os.Stat(activePath); e == nil {
		s.active, e = readBatch(activePath, binding)
		if e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	// Finish a seal interrupted after the immutable batch became durable.
	if s.active != nil && seen[s.active.ID] {
		for _, q := range s.queue {
			if q.batch.ID == s.active.ID {
				a, _ := json.Marshal(s.active)
				b, _ := json.Marshal(q.batch)
				if string(a) != string(b) {
					return nil, fmt.Errorf("conflicting active journal")
				}
			}
		}
		if e = os.Remove(activePath); e != nil {
			return nil, e
		}
		if e = s.syncDirectory(s.dir); e != nil {
			return nil, e
		}
		s.active = nil
	}
	if legacy != nil {
		if !seen[legacy.ID] {
			if e = s.enqueue(legacy); e != nil {
				return nil, e
			}
		} else {
			for _, q := range s.queue {
				if q.batch.ID == legacy.ID {
					a, _ := json.Marshal(legacy)
					b, _ := json.Marshal(q.batch)
					if string(a) != string(b) {
						return nil, fmt.Errorf("conflicting legacy migration")
					}
				}
			}
		}
	}
	// Old binaries reject this marker, including when this queue is empty.
	data, _ := json.Marshal(Batch{Version: 2, Binding: binding})
	if _, e = s.write("pending.json", data); e != nil {
		return nil, e
	}
	return s, nil
}
func readBatch(path, binding string) (*Batch, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, fmt.Errorf("read batch failed")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || info.Size() > 64<<20 {
		return nil, fmt.Errorf("batch too large")
	}
	var b Batch
	decoder := json.NewDecoder(f)
	if e = decoder.Decode(&b); e != nil {
		return nil, fmt.Errorf("corrupt batch: %w", ErrConflict)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing batch content: %w", ErrConflict)
	}
	if e = validBatch(&b, binding); e != nil {
		return nil, errors.Join(ErrConflict, e)
	}
	return &b, nil
}
func validBatch(b *Batch, binding string) error {
	if b.Version != 1 || b.Binding != binding || len(b.ID) != 32 || len(b.Traffic) == 0 {
		return fmt.Errorf("invalid batch or binding")
	}
	if _, e := hex.DecodeString(b.ID); e != nil {
		return fmt.Errorf("invalid batch identity")
	}
	return validate(b.Traffic)
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
func clone(b *Batch) *Batch {
	if b == nil {
		return nil
	}
	c := *b
	c.Traffic = append([]panel.UserTraffic(nil), b.Traffic...)
	return &c
}
func (s *Store) Pending() *Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	return clone(s.queue[0].batch)
}
func (s *Store) Counts() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue), s.active != nil
}

var (
	ErrClosed   = errors.New("traffic spool closed")
	ErrConflict = errors.New("traffic journal conflicts with the expected contents")
	// Only this error requires replaying the original counter/UID snapshot.
	ErrUnconfirmedSave = errors.New("traffic checkpoint awaiting storage confirmation")
)

type unconfirmedSave struct {
	batch *Batch
	rows  []panel.UserTraffic
}

func (s *Store) usable() error {
	if s.lock == nil {
		return ErrClosed
	}
	if s.pendingSave != nil || s.pendingSeal != nil || s.verifyActive || s.verifyAckHead || s.ackUnlinked {
		// A remounted or replaced directory may be protected by someone else's
		// lock. Recover only on the original, exclusively held spool.
		held, err := s.lock.Stat()
		if err != nil {
			return err
		}
		named, err := os.Lstat(filepath.Join(s.dir, "lock"))
		if err != nil {
			return err
		}
		if !os.SameFile(held, named) {
			return ErrConflict
		}
	}
	return nil
}
func (s *Store) bytes() int64 {
	var n int64
	for _, q := range s.queue {
		n += q.size
	}
	return n
}

// Save checkpoints new increments into an untransmitted journal. Its ID/content
// become immutable at Seal, before any request is allowed to use them.
func (s *Store) Save(rows []panel.UserTraffic) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.usable(); e != nil {
		return e
	}
	if s.pendingSeal != nil {
		if err := s.recoverSeal(); err != nil {
			return err
		}
	}
	if s.pendingSave != nil {
		return s.confirmSave(rows)
	}
	if s.verifyActive {
		actual, err := s.inspect("active.json")
		if err != nil {
			return err
		}
		if !sameBatch(actual, s.active) {
			return ErrConflict
		}
		s.verifyActive = false
	}
	if len(rows) == 0 {
		return nil
	}
	if e := validate(rows); e != nil {
		return e
	}
	b := clone(s.active)
	if b == nil {
		var id [16]byte
		if _, e := rand.Read(id[:]); e != nil {
			return fmt.Errorf("batch identity unavailable")
		}
		b = &Batch{Version: 1, Binding: s.binding, ID: hex.EncodeToString(id[:])}
	}
	byID := map[int]panel.UserTraffic{}
	for _, r := range b.Traffic {
		byID[r.UID] = r
	}
	for _, r := range rows {
		old := byID[r.UID]
		if r.Upload > math.MaxInt64-old.Upload || r.Download > math.MaxInt64-old.Download {
			return fmt.Errorf("traffic total overflow")
		}
		r.Upload += old.Upload
		r.Download += old.Download
		byID[r.UID] = r
	}
	b.Traffic = nil
	for _, r := range byID {
		b.Traffic = append(b.Traffic, r)
	}
	sort.Slice(b.Traffic, func(i, j int) bool { return b.Traffic[i].UID < b.Traffic[j].UID })
	data, e := json.Marshal(b)
	if e != nil || len(data) > 64<<20 || s.bytes()+int64(len(data)) > s.maxBytes {
		return fmt.Errorf("traffic spool capacity reached; counters retained in memory")
	}
	publicationAttempted, e := s.write("active.json", data)
	if e != nil {
		if publicationAttempted {
			s.pendingSave = &unconfirmedSave{b, slices.Clone(rows)}
			return errors.Join(ErrUnconfirmedSave, e)
		}
		s.verifyActive = true
		return fmt.Errorf("journal write failed: %w", e)
	}
	s.active = b
	return nil
}

// After rename the candidate is visible, but a failed directory sync means
// durability is unknown. Keep the exact counter observations until it is proven.
func (s *Store) confirmSave(rows []panel.UserTraffic) error {
	p := s.pendingSave
	if !slices.Equal(p.rows, rows) {
		return errors.Join(ErrUnconfirmedSave, ErrConflict)
	}
	actual, err := s.inspect("active.json")
	if err == nil {
		switch {
		case sameBatch(actual, p.batch):
			err = s.syncDirectory(s.dir)
		case sameBatch(actual, s.active):
			// Storage recovery exposed the exact previous version. Reinstall
			// the same candidate, including its ID, before deducting the sample.
			var data []byte
			data, err = json.Marshal(p.batch)
			if err == nil {
				_, err = s.write("active.json", data)
			}
		default:
			err = ErrConflict
		}
	}
	if err != nil {
		return errors.Join(ErrUnconfirmedSave, err)
	}
	s.active = p.batch
	s.pendingSave = nil
	return nil
}

// inspect distinguishes absence, a verified batch and unreadable data.
func (s *Store) inspect(name string) (*Batch, error) {
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrConflict
	}
	return readBatch(path, s.binding)
}
func sameBatch(a, b *Batch) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Binding == b.Binding && a.ID == b.ID && a.Version == b.Version && slices.Equal(a.Traffic, b.Traffic)
}
func (s *Store) enqueue(b *Batch) error {
	if len(s.queue) >= s.maxBatches {
		return fmt.Errorf("traffic queue full; journal retained")
	}
	if s.sequence == math.MaxUint64 {
		return fmt.Errorf("traffic queue sequence exhausted")
	}
	s.sequence++
	name := fmt.Sprintf("batch-%020d-%s.json", s.sequence, b.ID)
	data, e := json.Marshal(b)
	if e != nil {
		return e
	}
	if _, e = s.write(name, data); e != nil {
		return fmt.Errorf("batch seal failed: %w", e)
	}
	s.queue = append(s.queue, queued{name, clone(b), int64(len(data))})
	return nil
}
func (s *Store) Seal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealLocked()
}
func (s *Store) SealIfDue(interval time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interval > 0 && time.Since(s.lastSeal) < interval {
		return nil
	}
	return s.sealLocked()
}
func (s *Store) sealLocked() error {
	if e := s.usable(); e != nil {
		return e
	}
	if s.pendingSave != nil {
		return ErrUnconfirmedSave
	}
	if s.pendingSeal != nil {
		return s.recoverSeal()
	}
	if s.active == nil {
		return nil
	}
	if len(s.queue) >= s.maxBatches {
		return fmt.Errorf("traffic queue full; journal retained")
	}
	if s.sequence == math.MaxUint64 {
		return fmt.Errorf("traffic queue sequence exhausted")
	}
	// The active file already contains the immutable batch payload. A rename
	// changes its role without creating another independently mutable copy.
	activePath := filepath.Join(s.dir, "active.json")
	actual, err := s.inspect("active.json")
	if err != nil {
		return err
	}
	if !sameBatch(actual, s.active) {
		return ErrConflict
	}
	name := fmt.Sprintf("batch-%020d-%s.json", s.sequence+1, s.active.ID)
	destination := filepath.Join(s.dir, name)
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	info, err := os.Stat(activePath)
	if err != nil {
		return err
	}
	s.pendingSeal = &queued{name, s.active, info.Size()}
	if err = os.Rename(activePath, destination); err != nil {
		return err
	}
	return s.finishSeal()
}
func (s *Store) recoverSeal() error {
	q := s.pendingSeal
	target, err := s.inspect(q.name)
	if err != nil {
		return err
	}
	active, err := s.inspect("active.json")
	if err != nil {
		return err
	}
	switch {
	case sameBatch(target, q.batch) && active == nil:
	case target == nil && sameBatch(active, q.batch):
		if err := os.Rename(filepath.Join(s.dir, "active.json"), filepath.Join(s.dir, q.name)); err != nil {
			return err
		}
	case sameBatch(target, q.batch) && sameBatch(active, q.batch):
		// First ensure that the immutable copy is durable, then retire its twin.
		if err := s.syncDirectory(s.dir); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(s.dir, "active.json")); err != nil {
			return err
		}
	default:
		return ErrConflict
	}
	return s.finishSeal()
}
func (s *Store) finishSeal() error {
	if err := s.syncDirectory(s.dir); err != nil {
		return fmt.Errorf("batch seal not yet confirmed: %w", err)
	}
	s.queue = append(s.queue, *s.pendingSeal)
	s.sequence++
	s.pendingSeal = nil
	s.active = nil
	s.lastSeal = time.Now()
	return nil
}

// The flag marks an attempted publication. Even rename errors can be ambiguous
// on a failing filesystem, so the caller retains both candidate and preimage.
func (s *Store) write(name string, data []byte) (bool, error) {
	f, e := os.CreateTemp(s.dir, "journal-*.tmp")
	if e != nil {
		return false, e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return false, e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return false, e
	}
	if e = f.Close(); e != nil {
		return false, e
	}
	if e = os.Rename(tmp, filepath.Join(s.dir, name)); e != nil {
		return true, e
	}
	return true, s.syncDirectory(s.dir)
}
func syncDirectory(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) Acknowledge(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.usable(); e != nil {
		return e
	}
	if len(s.queue) == 0 || s.queue[0].batch.ID != id {
		return fmt.Errorf("batch acknowledgement mismatch: %w", ErrConflict)
	}
	head := s.queue[0]
	if s.verifyAckHead || s.ackUnlinked {
		actual, err := s.inspect(head.name)
		if err != nil {
			return err
		}
		switch {
		case sameBatch(actual, head.batch):
			s.ackUnlinked = false // exact pre-unlink state is safe to retry
		case actual == nil && s.ackUnlinked:
		default:
			return ErrConflict
		}
	}
	if !s.ackUnlinked {
		if err := os.Remove(filepath.Join(s.dir, head.name)); err != nil {
			s.verifyAckHead = true
			if errors.Is(err, os.ErrNotExist) {
				return ErrConflict
			}
			// EIO does not establish whether unlink took effect. Reconcile the
			// original ID and the two possible filesystem states on retry.
			s.ackUnlinked = errors.Is(err, syscall.EIO)
			return fmt.Errorf("clear batch failed: %w", err)
		}
		s.ackUnlinked = true
	}
	if err := s.syncDirectory(s.dir); err != nil {
		return fmt.Errorf("acknowledgement not yet durable: %w", err)
	}
	s.ackUnlinked = false
	s.verifyAckHead = false
	s.queue = s.queue[1:]
	return nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	e := s.lock.Close()
	s.lock = nil
	return e
}
