//go:build linux

package trafficbatch

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// This opt-in acceptance requires a dedicated, tiny tmpfs, so it cannot fill a
// node's ordinary disk or a large shared tmpfs.
func TestStorageRecoveryBoundedFilesystemENOSPC(t *testing.T) {
	root := os.Getenv("V2BX_TEST_PRIVATE_TMPFS")
	if root == "" {
		t.Skip("requires private tmpfs mount of at most 8 MiB")
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		t.Fatal(err)
	}
	if st.Type != unix.TMPFS_MAGIC || st.Blocks*uint64(st.Bsize) > 8<<20 || st.Blocks*uint64(st.Bsize) < 512<<10 {
		t.Fatal("expected a dedicated 512-KiB to 8-MiB tmpfs")
	}
	var here, parent unix.Stat_t
	if err := unix.Stat(root, &here); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(filepath.Dir(filepath.Clean(root)), &parent); err != nil {
		t.Fatal(err)
	}
	if here.Dev == parent.Dev {
		t.Fatal("test path must be the dedicated tmpfs mountpoint itself")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("tmpfs must be an empty dedicated mount", err)
	}
	dir, err := os.MkdirTemp(root, "storage-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := openRecoveryStore(t, filepath.Join(dir, "spool"))
	requireStorageOK(t, s.Save([]panel.UserTraffic{{UID: 17, Download: 31}}))
	fill := filepath.Join(dir, "filler")
	f, err := os.OpenFile(fill, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	requireStorageOK(t, err)
	block := make([]byte, st.Bsize)
	var writeErr error
	for i := uint64(0); i <= st.Blocks && writeErr == nil; i++ {
		_, writeErr = f.Write(block)
	}
	requireStorageOK(t, f.Close())
	if !errors.Is(writeErr, unix.ENOSPC) {
		t.Fatalf("expected actual tmpfs ENOSPC, got %v", writeErr)
	}
	rows := []panel.UserTraffic{{UID: 17, Upload: 10, Download: 2}, {UID: 18, Upload: 3, Download: 9}}
	if err = s.Save(rows); !errors.Is(err, unix.ENOSPC) || errors.Is(err, ErrUnconfirmedSave) {
		t.Fatalf("unexpected persistence result: %v", err)
	}
	requireStorageOK(t, os.Remove(fill))
	rows[0].Upload += 7
	rows[1].Download += 11
	requireStorageOK(t, s.Save(rows))
	requireStorageOK(t, s.Seal())
	want := []panel.UserTraffic{{UID: 17, Upload: 17, Download: 33}, {UID: 18, Upload: 3, Download: 20}}
	if got := s.Pending(); got == nil || !slices.Equal(got.Traffic, want) {
		t.Fatalf("restored queue: %#v", got)
	}
}
