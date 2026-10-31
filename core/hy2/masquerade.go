package hy2

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/apernet/hysteria/extras/v2/correctnet"
	"go.uber.org/zap"
)

// Own the TCP side of masquerading separately from the QUIC server. Binding is
// synchronous; serving starts only after the entire inbound has been created.
type masqueradeServers struct {
	mu              sync.Mutex
	closed, started bool
	endpoints       []masqueradeEndpoint
	connections     map[net.Conn]struct{}
	wg              sync.WaitGroup
	closeOnce       sync.Once
	logger          *zap.Logger
}

type masqueradeEndpoint struct {
	listener net.Listener
	server   *http.Server
	tls      bool
}

func newMasqueradeServers(httpAddr, httpsAddr string, tlsConfig *tls.Config, handler http.Handler, quicPort, httpsPort int, forceHTTPS bool, logger *zap.Logger) (*masqueradeServers, error) {
	m := &masqueradeServers{connections: make(map[net.Conn]struct{}), logger: logger}
	for _, address := range []struct {
		addr string
		tls  bool
	}{{httpAddr, false}, {httpsAddr, true}} {
		if address.addr == "" {
			continue
		}
		listener, err := correctnet.Listen("tcp", address.addr)
		if err != nil {
			m.Close()
			return nil, fmt.Errorf("bind masquerade listener: %w", err)
		}
		redirect := !address.tls && forceHTTPS
		s := &http.Server{TLSConfig: tlsConfig, ConnState: m.connectionState}
		s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				host := r.Host
				if httpsPort != 0 && httpsPort != 443 {
					host = fmt.Sprintf("%s:%d", host, httpsPort)
				}
				http.Redirect(w, r, "https://"+host+r.RequestURI, http.StatusMovedPermanently)
				return
			}
			response := &masqueradeResponse{ResponseWriter: w, port: quicPort}
			if hijacker, ok := w.(http.Hijacker); ok {
				handler.ServeHTTP(&masqueradeHijacker{masqueradeResponse: response, hijacker: hijacker, owner: m}, r)
			} else {
				handler.ServeHTTP(response, r)
			}
		})
		m.endpoints = append(m.endpoints, masqueradeEndpoint{listener, s, address.tls})
	}
	return m, nil
}

func (m *masqueradeServers) Start() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed || m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.wg.Add(len(m.endpoints))
	m.mu.Unlock()
	for _, endpoint := range m.endpoints {
		go func(e masqueradeEndpoint) {
			defer m.wg.Done()
			var err error
			if e.tls {
				err = e.server.ServeTLS(e.listener, "", "")
			} else {
				err = e.server.Serve(e.listener)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				m.logger.Error("masquerade listener stopped", zap.Error(err))
			}
		}(endpoint)
	}
}

func (m *masqueradeServers) connectionState(conn net.Conn, state http.ConnState) {
	m.mu.Lock()
	if state == http.StateClosed {
		delete(m.connections, conn)
	} else if !m.closed {
		m.connections[conn] = struct{}{}
	}
	closed := m.closed
	m.mu.Unlock()
	if closed {
		_ = conn.Close()
	}
}

// Close stops listeners and all accepted connections, including hijacked ones.
// It waits for Serve, not arbitrary user handlers, and takes no core identity lock.
func (m *masqueradeServers) Close() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		connections := make([]net.Conn, 0, len(m.connections))
		for c := range m.connections {
			connections = append(connections, c)
		}
		m.connections = make(map[net.Conn]struct{})
		m.mu.Unlock()
		for _, e := range m.endpoints {
			_ = e.listener.Close()
			_ = e.server.Close()
		}
		for _, c := range connections {
			_ = c.Close()
		}
		m.wg.Wait()
	})
}

type masqueradeResponse struct {
	http.ResponseWriter
	port int
}

func (w *masqueradeResponse) WriteHeader(status int) {
	w.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%d"; ma=2592000`, w.port))
	w.ResponseWriter.WriteHeader(status)
}

type masqueradeHijacker struct {
	*masqueradeResponse
	hijacker http.Hijacker
	owner    *masqueradeServers
}

func (w *masqueradeHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := w.hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &masqueradeHijackedConn{Conn: c, owner: w.owner}, rw, nil
}

type masqueradeHijackedConn struct {
	net.Conn
	owner *masqueradeServers
}

func (c *masqueradeHijackedConn) Close() error {
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.connections, c.Conn)
	c.owner.mu.Unlock()
	return err
}
