package hy2

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/apernet/hysteria/core/server"
	"github.com/apernet/hysteria/extras/correctnet"
	"github.com/apernet/hysteria/extras/masq"
	"github.com/apernet/hysteria/extras/obfs"
	"github.com/apernet/hysteria/extras/outbounds"
	"go.uber.org/zap"
)

type masqHandlerLogWrapper struct {
	H      http.Handler
	QUIC   bool
	Logger *zap.Logger
}

func (m *masqHandlerLogWrapper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.Logger.Debug("masquerade request",
		zap.String("addr", r.RemoteAddr),
		zap.String("method", r.Method),
		zap.String("host", r.Host),
		zap.String("url", r.URL.String()),
		zap.Bool("quic", m.QUIC))
	m.H.ServeHTTP(w, r)
}

const (
	Byte     = 1
	Kilobyte = Byte * 1000
	Megabyte = Kilobyte * 1000
	Gigabyte = Megabyte * 1000
	Terabyte = Gigabyte * 1000
)

const (
	defaultStreamReceiveWindow = 8388608                            // 8MB
	defaultConnReceiveWindow   = defaultStreamReceiveWindow * 5 / 2 // 20MB
	defaultMaxIdleTimeout      = 30 * time.Second
	defaultMaxIncomingStreams  = 1024
	defaultUDPIdleTimeout      = 60 * time.Second
)

func (n *Hysteria2node) getTLSConfig(config *conf.Options) (*server.TLSConfig, error) {
	if config.CertConfig == nil {
		return nil, fmt.Errorf("the CertConfig is not vail")
	}
	switch config.CertConfig.CertMode {
	case "none", "":
		return nil, fmt.Errorf("the CertMode cannot be none")
	default:
		var certs []tls.Certificate
		cert, err := tls.LoadX509KeyPair(config.CertConfig.CertFile, config.CertConfig.KeyFile)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
		return &server.TLSConfig{
			Certificates: certs,
			GetCertificate: func(tlsinfo *tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, err := tls.LoadX509KeyPair(config.CertConfig.CertFile, config.CertConfig.KeyFile)
				return &cert, err
			},
		}, nil
	}
}

func (n *Hysteria2node) getQUICConfig(config *conf.Options) (*server.QUICConfig, error) {
	quic := &server.QUICConfig{}
	if config.Hysteria2Options.QUICConfig.InitialStreamReceiveWindow == 0 {
		quic.InitialStreamReceiveWindow = defaultStreamReceiveWindow
	} else if config.Hysteria2Options.QUICConfig.InitialStreamReceiveWindow < 16384 {
		return nil, fmt.Errorf("QUICConfig.InitialStreamReceiveWindowf must be at least 16384")
	}
	if config.Hysteria2Options.QUICConfig.MaxStreamReceiveWindow == 0 {
		quic.MaxStreamReceiveWindow = defaultStreamReceiveWindow
	} else if config.Hysteria2Options.QUICConfig.MaxStreamReceiveWindow < 16384 {
		return nil, fmt.Errorf("QUICConfig.MaxStreamReceiveWindowf must be at least 16384")
	}
	if config.Hysteria2Options.QUICConfig.InitialConnectionReceiveWindow == 0 {
		quic.InitialConnectionReceiveWindow = defaultConnReceiveWindow
	} else if config.Hysteria2Options.QUICConfig.InitialConnectionReceiveWindow < 16384 {
		return nil, fmt.Errorf("QUICConfig.InitialConnectionReceiveWindowf must be at least 16384")
	}
	if config.Hysteria2Options.QUICConfig.MaxConnectionReceiveWindow == 0 {
		quic.MaxConnectionReceiveWindow = defaultConnReceiveWindow
	} else if config.Hysteria2Options.QUICConfig.MaxConnectionReceiveWindow < 16384 {
		return nil, fmt.Errorf("QUICConfig.MaxConnectionReceiveWindowf must be at least 16384")
	}
	if config.Hysteria2Options.QUICConfig.MaxIdleTimeout == 0 {
		quic.MaxIdleTimeout = defaultMaxIdleTimeout
	} else if config.Hysteria2Options.QUICConfig.MaxIdleTimeout < 4*time.Second || config.Hysteria2Options.QUICConfig.MaxIdleTimeout > 120*time.Second {
		return nil, fmt.Errorf("QUICConfig.MaxIdleTimeoutf must be between 4s and 120s")
	}
	if config.Hysteria2Options.QUICConfig.MaxIncomingStreams == 0 {
		quic.MaxIncomingStreams = defaultMaxIncomingStreams
	} else if config.Hysteria2Options.QUICConfig.MaxIncomingStreams < 8 {
		return nil, fmt.Errorf("QUICConfig.MaxIncomingStreamsf must be at least 8")
	}
	// todo fix !linux && !windows && !darwin
	quic.DisablePathMTUDiscovery = false

	return quic, nil
}
func (n *Hysteria2node) getConn(info *panel.NodeInfo, config *conf.Options) (net.PacketConn, error) {
	uAddr, err := net.ResolveUDPAddr("udp", formatAddress(config.ListenIP, info.Common.ServerPort))
	if err != nil {
		return nil, err
	}
	conn, err := correctnet.ListenUDP("udp", uAddr)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(info.Hysteria2.ObfsType) {
	case "", "plain":
		return conn, nil
	case "salamander":
		ob, err := obfs.NewSalamanderObfuscator([]byte(info.Hysteria2.ObfsPassword))
		if err != nil {
			return nil, err
		}
		return obfs.WrapPacketConn(conn, ob), nil
	default:
		return nil, fmt.Errorf("unsupported obfuscation type")
	}
}

func (n *Hysteria2node) getBandwidthConfig(info *panel.NodeInfo) *server.BandwidthConfig {
	band := &server.BandwidthConfig{}
	if info.Hysteria2.UpMbps != 0 {
		band.MaxTx = (uint64)(info.Hysteria2.UpMbps * Megabyte / 8)
	}
	if info.Hysteria2.DownMbps != 0 {
		band.MaxRx = (uint64)(info.Hysteria2.DownMbps * Megabyte / 8)

	}
	return band
}

func (n *Hysteria2node) getOutboundConfig(config *conf.Options) (server.Outbound, error) {
	var obs []outbounds.OutboundEntry
	if len(config.Hysteria2Options.Outbounds) == 0 {
		// Guarantee we have at least one outbound
		obs = []outbounds.OutboundEntry{{
			Name:     "default",
			Outbound: outbounds.NewDirectOutboundSimple(outbounds.DirectOutboundModeAuto),
		}}
	} else {
		obs = make([]outbounds.OutboundEntry, len(config.Hysteria2Options.Outbounds))
		for i, entry := range config.Hysteria2Options.Outbounds {
			if entry.Name == "" {
				return nil, fmt.Errorf("outbounds.name empty outbound name")
			}
			var ob outbounds.PluggableOutbound
			var err error
			switch strings.ToLower(entry.Type) {
			case "direct":
				ob, err = serverConfigOutboundDirectToOutbound(entry.Direct)
			case "socks5":
				ob, err = serverConfigOutboundSOCKS5ToOutbound(entry.SOCKS5)
			case "http":
				ob, err = serverConfigOutboundHTTPToOutbound(entry.HTTP)
			default:
				err = fmt.Errorf("outbounds.type unsupported outbound type")
			}
			if err != nil {
				return nil, err
			}
			obs[i] = outbounds.OutboundEntry{Name: entry.Name, Outbound: ob}
		}
	}
	var uOb outbounds.PluggableOutbound // "unified" outbound

	hasACL := false
	if hasACL {
		// todo fix ACL
	} else {
		// No ACL, use the first outbound
		uOb = obs[0].Outbound
	}
	Outbound := &outbounds.PluggableOutboundAdapter{PluggableOutbound: uOb}

	return Outbound, nil
}

func (n *Hysteria2node) getMasqHandler(tlsconfig *server.TLSConfig, conn net.PacketConn, info *panel.NodeInfo, config *conf.Options) (http.Handler, error) {
	var handler http.Handler
	switch strings.ToLower(config.Hysteria2Options.Masquerade.Type) {
	case "", "404":
		handler = http.NotFoundHandler()
	case "file":
		if config.Hysteria2Options.Masquerade.File.Dir == "" {
			return nil, fmt.Errorf("masquerade.file.dir empty file directory")
		}
		handler = http.FileServer(http.Dir(config.Hysteria2Options.Masquerade.File.Dir))
	case "proxy":
		if config.Hysteria2Options.Masquerade.Proxy.URL == "" {
			return nil, fmt.Errorf("masquerade.proxy.url empty proxy url")
		}
		u, err := url.Parse(config.Hysteria2Options.Masquerade.Proxy.URL)
		if err != nil {
			return nil, fmt.Errorf(fmt.Sprintf("masquerade.proxy.url %s", err))
		}
		handler = &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(u)
				// SetURL rewrites the Host header,
				// but we don't want that if rewriteHost is false
				if !config.Hysteria2Options.Masquerade.Proxy.RewriteHost {
					r.Out.Host = r.In.Host
				}
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				n.Logger.Error("HTTP reverse proxy error", zap.Error(err))
				w.WriteHeader(http.StatusBadGateway)
			},
		}
	case "string":
		if config.Hysteria2Options.Masquerade.String.Content == "" {
			return nil, fmt.Errorf("masquerade.string.content empty string content")
		}
		if config.Hysteria2Options.Masquerade.String.StatusCode != 0 &&
			(config.Hysteria2Options.Masquerade.String.StatusCode < 200 ||
				config.Hysteria2Options.Masquerade.String.StatusCode > 599 ||
				config.Hysteria2Options.Masquerade.String.StatusCode == 233) {
			// 233 is reserved for Hysteria authentication
			return nil, fmt.Errorf("masquerade.string.statusCode invalid status code (must be 200-599, except 233)")
		}
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range config.Hysteria2Options.Masquerade.String.Headers {
				w.Header().Set(k, v)
			}
			if config.Hysteria2Options.Masquerade.String.StatusCode != 0 {
				w.WriteHeader(config.Hysteria2Options.Masquerade.String.StatusCode)
			} else {
				w.WriteHeader(http.StatusOK) // Use 200 OK by default
			}
			_, _ = w.Write([]byte(config.Hysteria2Options.Masquerade.String.Content))
		})
	default:
		return nil, fmt.Errorf("masquerade.type unsupported masquerade type")
	}
	MasqHandler := &masqHandlerLogWrapper{H: handler, QUIC: true, Logger: n.Logger}

	if config.Hysteria2Options.Masquerade.ListenHTTP != "" || config.Hysteria2Options.Masquerade.ListenHTTPS != "" {
		if config.Hysteria2Options.Masquerade.ListenHTTP != "" && config.Hysteria2Options.Masquerade.ListenHTTPS == "" {
			return nil, fmt.Errorf("masquerade.listenHTTPS having only HTTP server without HTTPS is not supported")
		}
		s := masq.MasqTCPServer{
			QUICPort:  extractPortFromAddr(conn.LocalAddr().String()),
			HTTPSPort: extractPortFromAddr(config.Hysteria2Options.Masquerade.ListenHTTPS),
			Handler:   &masqHandlerLogWrapper{H: handler, QUIC: false},
			TLSConfig: &tls.Config{
				Certificates:   tlsconfig.Certificates,
				GetCertificate: tlsconfig.GetCertificate,
			},
			ForceHTTPS: config.Hysteria2Options.Masquerade.ForceHTTPS,
		}
		go runMasqTCPServer(&s, config.Hysteria2Options.Masquerade.ListenHTTP, config.Hysteria2Options.Masquerade.ListenHTTPS, n.Logger)
	}

	return MasqHandler, nil
}

func runMasqTCPServer(s *masq.MasqTCPServer, httpAddr, httpsAddr string, logger *zap.Logger) {
	errChan := make(chan error, 2)
	if httpAddr != "" {
		go func() {
			logger.Info("masquerade HTTP server up and running", zap.String("listen", httpAddr))
			errChan <- s.ListenAndServeHTTP(httpAddr)
		}()
	}
	if httpsAddr != "" {
		go func() {
			logger.Info("masquerade HTTPS server up and running", zap.String("listen", httpsAddr))
			errChan <- s.ListenAndServeHTTPS(httpsAddr)
		}()
	}
	err := <-errChan
	if err != nil {
		logger.Fatal("failed to serve masquerade HTTP(S)", zap.Error(err))
	}
}

func extractPortFromAddr(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

func formatAddress(ip string, port int) string {
	// 检查 IP 地址是否为 IPv6
	if strings.Contains(ip, ":") {
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	// 对于 IPv4 地址，直接返回 IP:Port 格式
	return fmt.Sprintf("%s:%d", ip, port)
}
