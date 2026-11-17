//go:build linux && vision_live

package xray

import (
	"encoding/binary"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/proxy"
)

// Document the actual direct-path prerequisite used by the pinned core. A
// successful TLS transfer alone does not guarantee record-aligned TCP reads.
func TestVisionDirectRecordBoundaryPrerequisite(t *testing.T) {
	record := make([]byte, 5+(16<<10))
	copy(record, []byte{0x17, 3, 3})
	binary.BigEndian.PutUint16(record[3:5], 16<<10)
	check := func(t *testing.T, want bool, chunks ...[]byte) {
		t.Helper()
		var mb buf.MultiBuffer
		for _, chunk := range chunks {
			mb = append(mb, buf.FromBytes(chunk))
		}
		defer buf.ReleaseMulti(mb)
		if got := proxy.IsCompleteRecord(mb); got != want {
			t.Fatalf("direct record prerequisite: got %v, want %v", got, want)
		}
	}
	t.Run("whole-record", func(t *testing.T) { check(t, true, record) })
	t.Run("buffers-within-one-read", func(t *testing.T) {
		check(t, true, record[:4096], record[4096:])
	})
	t.Run("separate-reads-do-not-retain-framing", func(t *testing.T) {
		check(t, false, record[:4096])
		check(t, false, record[4096:])
		check(t, true, record)
	})
}
