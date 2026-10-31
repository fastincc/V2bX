package server

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type recordedPacer struct {
	bytes int
	err   error
}

func (p *recordedPacer) Wait(ctx context.Context, n int) error {
	p.bytes += n
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.err
}
func TestPacedPutbackAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		buffer := new(bytes.Buffer)
		pacer := new(recordedPacer)
		rw := &pacedReadWriter{ReadWriter: buffer, ctx: ctx, pacer: pacer}
		log := &putbackLogger{allow: true}
		stats := new(StreamStats)
		err := writePutback(rw, []byte("synthetic-prefix"), "opaque-id", log, stats)
		if pacer.bytes != 16 || log.tx != 16 || log.rx != 0 || log.calls != 1 {
			t.Fatal("prefix quota or ingress accounting changed")
		}
		if cancelled {
			if !errors.Is(err, context.Canceled) || buffer.Len() != 0 {
				t.Fatal("cancelled prefix forwarded")
			}
		} else {
			if err != nil || buffer.String() != "synthetic-prefix" {
				t.Fatal("prefix lost")
			}
		}
		cancel()
	}
}
