package server

import (
	"context"
	"io"
)

type pacedReadWriter struct {
	io.ReadWriter
	ctx   context.Context
	pacer TrafficPacer
}

func (p *pacedReadWriter) Write(b []byte) (int, error) {
	if err := p.pacer.Wait(p.ctx, len(b)); err != nil {
		return 0, err
	}
	return p.ReadWriter.Write(b)
}
