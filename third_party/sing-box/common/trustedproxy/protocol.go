// Package trustedproxy accepts a mandatory PROXY header from explicitly trusted
// TCP peers, or accepts declared addresses in explicitly optional peer mode. It is intended to run once, before the TLS handshake.
package trustedproxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxV2Payload = 512

var signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

type Policy struct {
	trusted      []netip.Prefix
	allowAnyPeer bool
	timeout      time.Duration
	version      int
}

// New validates configuration at startup. Version 0 accepts v1 and v2.
func New(cidrs []string, timeout time.Duration, version int) (*Policy, error) {
	return NewWithTrust(cidrs, timeout, version, true)
}

// NewWithTrust accepts any peer only when requireTrusted is false and no CIDRs
// are supplied. Existing nonempty allowlists remain enforced.
func NewWithTrust(cidrs []string, timeout time.Duration, version int, requireTrusted bool) (*Policy, error) {
	if requireTrusted && len(cidrs) == 0 {
		return nil, errors.New("trusted proxy CIDRs must not be empty")
	}
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	if timeout < 0 || timeout > 30*time.Second {
		return nil, errors.New("proxy header timeout must be positive and at most 30s")
	}
	if version < 0 || version > 2 {
		return nil, errors.New("proxy protocol version must be 0, 1 or 2")
	}
	p := &Policy{timeout: timeout, version: version, allowAnyPeer: !requireTrusted && len(cidrs) == 0}
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR: %w", err)
		}
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		// An all-address trust list defeats source authenticity.
		if prefix.Bits() == 0 {
			return nil, errors.New("trusting all proxy peers is not allowed")
		}
		p.trusted = append(p.trusted, prefix.Masked())
	}
	return p, nil
}

// Accept applies configured peer policy before consuming exactly one header.
// A missing-header fallback is never allowed on an enabled port.
// The caller owns and must close the connection on any error.
func (p *Policy) Accept(conn net.Conn) (result net.Conn, err error) {
	peer, parseErr := netip.ParseAddrPort(conn.RemoteAddr().String())
	if parseErr != nil || !p.trusts(peer.Addr().Unmap()) {
		return nil, errors.New("untrusted proxy peer")
	}
	if err = conn.SetReadDeadline(time.Now().Add(p.timeout)); err != nil {
		return nil, err
	}
	defer func() {
		if clearErr := conn.SetReadDeadline(time.Time{}); err == nil && clearErr != nil {
			result, err = nil, clearErr
		}
	}()
	r := bufio.NewReaderSize(conn, 1024)
	first, err := r.Peek(1)
	if err != nil {
		return nil, fmt.Errorf("read proxy header: %w", err)
	}
	var source netip.AddrPort
	switch {
	case first[0] == 'P' && p.version != 2:
		source, err = readV1(r)
	case first[0] == '\r' && p.version != 1:
		source, err = readV2(r)
	default:
		err = errors.New("missing or unsupported proxy header")
	}
	if err != nil {
		return nil, err
	}
	return &sourceConn{Conn: conn, reader: r, source: net.TCPAddrFromAddrPort(source)}, nil
}

func (p *Policy) trusts(peer netip.Addr) bool {
	if p.allowAnyPeer {
		return true
	}
	for _, prefix := range p.trusted {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

type sourceConn struct {
	net.Conn
	reader *bufio.Reader
	source *net.TCPAddr
	readMu sync.Mutex
}

func (c *sourceConn) RemoteAddr() net.Addr { return c.source }

func (c *sourceConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	// Drain prefetched TLS bytes without losing them, then use the original
	// connection directly rather than retaining a buffered copy on every read.
	if c.reader != nil {
		if c.reader.Buffered() > 0 {
			return c.reader.Read(b)
		}
		c.reader = nil
	}
	return c.Conn.Read(b)
}

func validAddress(a netip.Addr) bool {
	// Validate the same normalized address that is later exposed as Source.
	// Preserve the original zone check because Unmap can discard a zone.
	return a.IsValid() && a.Zone() == "" && !a.Unmap().IsUnspecified() && !a.Unmap().IsMulticast()
}

func readV1(r *bufio.Reader) (netip.AddrPort, error) {
	line := make([]byte, 0, 108)
	for len(line) < 108 {
		b, err := r.ReadByte()
		if err != nil {
			return netip.AddrPort{}, err
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return netip.AddrPort{}, errors.New("invalid or oversized proxy v1 header")
	}
	f := strings.Split(string(line[:len(line)-2]), " ")
	if len(f) != 6 || f[0] != "PROXY" || (f[1] != "TCP4" && f[1] != "TCP6") {
		return netip.AddrPort{}, errors.New("proxy v1 requires TCP4 or TCP6 addresses")
	}
	src, e1 := netip.ParseAddr(f[2])
	dst, e2 := netip.ParseAddr(f[3])
	sp, e3 := strconv.ParseUint(f[4], 10, 16)
	dp, e4 := strconv.ParseUint(f[5], 10, 16)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || sp == 0 || dp == 0 || !validAddress(src) || !validAddress(dst) {
		return netip.AddrPort{}, errors.New("invalid proxy v1 address or port")
	}
	if (f[1] == "TCP4" && (!src.Is4() || !dst.Is4())) || (f[1] == "TCP6" && (!src.Is6() || !dst.Is6())) {
		return netip.AddrPort{}, errors.New("proxy v1 address family mismatch")
	}
	return netip.AddrPortFrom(src.Unmap(), uint16(sp)), nil
}

func readV2(r *bufio.Reader) (netip.AddrPort, error) {
	var header [16]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return netip.AddrPort{}, err
	}
	if !bytes.Equal(header[:12], signature) || header[12] != 0x21 {
		return netip.AddrPort{}, errors.New("proxy v2 requires PROXY command")
	}
	addressLen := 0
	switch header[13] {
	case 0x11: // INET + STREAM
		addressLen = 12
	case 0x21: // INET6 + STREAM
		addressLen = 36
	default:
		return netip.AddrPort{}, errors.New("proxy v2 requires TCP IPv4 or IPv6")
	}
	n := int(binary.BigEndian.Uint16(header[14:]))
	if n < addressLen || n > maxV2Payload {
		return netip.AddrPort{}, errors.New("invalid or oversized proxy v2 payload")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return netip.AddrPort{}, err
	}
	ipLen := (addressLen - 4) / 2
	src, _ := netip.AddrFromSlice(payload[:ipLen])
	dst, _ := netip.AddrFromSlice(payload[ipLen : 2*ipLen])
	sp := binary.BigEndian.Uint16(payload[2*ipLen:])
	dp := binary.BigEndian.Uint16(payload[2*ipLen+2:])
	// Nyanpass omits the destination as an unspecified address and zero port.
	// It is not used for routing; only the authenticated AnyTLS target is used.
	// Keep source validation strict and reject partially empty destinations.
	emptyDestination := dst.IsUnspecified() && dp == 0
	if !validAddress(src) || sp == 0 || (!emptyDestination && (!validAddress(dst) || dp == 0)) {
		return netip.AddrPort{}, errors.New("invalid proxy v2 address or port")
	}
	// Ignore well-formed optional TLVs, without trusting their authentication or
	// SSL claims. Reject truncated TLVs instead of consuming subsequent TLS data.
	for rest := payload[addressLen:]; len(rest) > 0; {
		if len(rest) < 3 || int(binary.BigEndian.Uint16(rest[1:3])) > len(rest)-3 {
			return netip.AddrPort{}, errors.New("truncated proxy v2 TLV")
		}
		rest = rest[3+int(binary.BigEndian.Uint16(rest[1:3])):]
	}
	return netip.AddrPortFrom(src.Unmap(), sp), nil
}
