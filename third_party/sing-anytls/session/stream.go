package session

import (
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/anytls/sing-anytls/pipe"
)

// Leave room for the 7-byte header inside the reusable buffer size limit.
const maxDataPayload = 65535 - headerOverHeadSize

// Stream implements net.Conn
type Stream struct {
	id uint32

	sess *Session

	pipeR         *pipe.PipeReader
	pipeW         *pipe.PipeWriter
	writeDeadline pipe.PipeDeadline
	writeMu       sync.Mutex

	dieOnce sync.Once
	dieHook func()
	dieErr  error
	dieMu   sync.RWMutex

	reportOnce sync.Once
}

// newStream initiates a Stream struct
func newStream(id uint32, sess *Session) *Stream {
	s := new(Stream)
	s.id = id
	s.sess = sess
	s.pipeR, s.pipeW = pipe.Pipe()
	s.writeDeadline = pipe.MakePipeDeadline()
	return s
}

// Read implements net.Conn
func (s *Stream) Read(b []byte) (n int, err error) {
	n, err = s.pipeR.Read(b)
	if closeErr := s.closeError(); n == 0 && closeErr != nil {
		err = closeErr
	}
	return
}

// Write implements net.Conn
func (s *Stream) Write(b []byte) (n int, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for len(b) > 0 {
		select {
		case <-s.writeDeadline.Wait():
			return n, os.ErrDeadlineExceeded
		default:
		}
		if closeErr := s.closeError(); closeErr != nil {
			return n, closeErr
		}
		size := min(len(b), maxDataPayload)
		written, err := s.sess.writeDataFrame(s.id, b[:size])
		n += written
		if err != nil {
			return n, err
		}
		b = b[size:]
	}
	return n, nil
}

// Close implements net.Conn
func (s *Stream) Close() error {
	return s.closeWithError(io.ErrClosedPipe)
}

// closeLocally only closes Stream and don't notify remote peer
func (s *Stream) closeLocally() {
	var once bool
	s.dieOnce.Do(func() {
		s.setCloseError(net.ErrClosed)
		s.pipeR.Close()
		once = true
	})
	if once {
		if s.dieHook != nil {
			s.dieHook()
			s.dieHook = nil
		}
	}
}

func (s *Stream) closeWithError(err error) error {
	var once bool
	s.dieOnce.Do(func() {
		s.setCloseError(err)
		s.pipeR.Close()
		once = true
	})
	if once {
		if s.dieHook != nil {
			s.dieHook()
			s.dieHook = nil
		}
		return s.sess.streamClosed(s.id)
	} else {
		return s.closeError()
	}
}

func (s *Stream) SetReadDeadline(t time.Time) error {
	return s.pipeR.SetReadDeadline(t)
}

func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Set(t)
	return nil
}

func (s *Stream) SetDeadline(t time.Time) error {
	s.SetWriteDeadline(t)
	return s.SetReadDeadline(t)
}

// LocalAddr satisfies net.Conn interface
func (s *Stream) LocalAddr() net.Addr {
	if ts, ok := s.sess.conn.(interface {
		LocalAddr() net.Addr
	}); ok {
		return ts.LocalAddr()
	}
	return nil
}

// RemoteAddr satisfies net.Conn interface
func (s *Stream) RemoteAddr() net.Addr {
	if ts, ok := s.sess.conn.(interface {
		RemoteAddr() net.Addr
	}); ok {
		return ts.RemoteAddr()
	}
	return nil
}

// HandshakeFailure should be called when Server fail to create outbound proxy
func (s *Stream) HandshakeFailure(err error) error {
	var once bool
	s.reportOnce.Do(func() {
		once = true
	})
	if once && err != nil && s.sess.peerVersion >= 2 {
		f := newFrame(cmdSYNACK, s.id)
		f.data = []byte(err.Error())
		if _, err := s.sess.writeControlFrame(f); err != nil {
			return err
		}
	}
	return nil
}

// HandshakeSuccess should be called when Server success to create outbound proxy
func (s *Stream) HandshakeSuccess() error {
	var once bool
	s.reportOnce.Do(func() {
		once = true
	})
	if once && s.sess.peerVersion >= 2 {
		if _, err := s.sess.writeControlFrame(newFrame(cmdSYNACK, s.id)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) closeError() error {
	s.dieMu.RLock()
	defer s.dieMu.RUnlock()
	return s.dieErr
}
func (s *Stream) setCloseError(err error) {
	s.dieMu.Lock()
	s.dieErr = err
	s.dieMu.Unlock()
}
