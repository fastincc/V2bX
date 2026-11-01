// Package visionsplice provides an opt-in, cancellable Linux zero-copy pump.
package visionsplice

import (
	"context"
	"errors"
	"sync/atomic"
)

var ErrUnsupported = errors.New("managed Vision splice is unavailable for this transport/platform")

type Stats struct{ Calls, Bytes atomic.Int64 }

// Before reserves quota before a pipe-to-socket transfer. Progress accounts
// only successful output bytes, including partial success followed by error.
type Options struct {
	Before   func(context.Context, int64) error
	Limited  func() bool
	Progress func(int64)
	Stats    *Stats
}
