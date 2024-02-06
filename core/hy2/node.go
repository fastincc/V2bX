package hy2

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/apernet/hysteria/core/server"
	"go.uber.org/zap"
)

type Hysteria2node struct {
	Hy2server     server.Server
	Tag           string
	Logger        *zap.Logger
	EventLogger   server.EventLogger
	TrafficLogger server.TrafficLogger
}

func (n *Hysteria2node) getHyConfig(tag string, info *panel.NodeInfo, config *conf.Options) (*server.Config, error) {
	tls, err := n.getTLSConfig(config)
	if err != nil {
		return nil, err
	}
	quic, err := n.getQUICConfig(config)
	if err != nil {
		return nil, err
	}
	conn, err := n.getConn(info, config)
	if err != nil {
		return nil, err
	}
	Outbound, err := n.getOutboundConfig(config)
	if err != nil {
		return nil, err
	}
	Masq, err := n.getMasqHandler(tls, conn, info, config)
	if err != nil {
		return nil, err
	}
	return &server.Config{
		TLSConfig:             *tls,
		QUICConfig:            *quic,
		Conn:                  conn,
		Outbound:              Outbound,
		BandwidthConfig:       *n.getBandwidthConfig(info),
		IgnoreClientBandwidth: config.Hysteria2Options.IgnoreClientBandwidth,
		DisableUDP:            config.Hysteria2Options.DisableUDP,
		UDPIdleTimeout:        config.Hysteria2Options.UDPIdleTimeout,
		EventLogger:           n.EventLogger,
		TrafficLogger:         n.TrafficLogger,
		MasqHandler:           Masq,
	}, nil
}

func (h *Hysteria2) AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error {
	n := Hysteria2node{
		Tag:    tag,
		Logger: h.Logger,
		EventLogger: &serverLogger{
			Tag:    tag,
			logger: h.Logger,
		},
		TrafficLogger: &HookServer{
			Tag: tag,
		},
	}
	hyconfig, err := n.getHyConfig(tag, info, config)
	if err != nil {
		return err
	}
	hyconfig.Authenticator = h.Auth
	s, err := server.NewServer(hyconfig)
	if err != nil {
		return err
	}
	n.Hy2server = s
	h.Hy2nodes[tag] = n
	go func() {
		if err := s.Serve(); err != nil {
			h.Logger.Error("Server Error", zap.Error(err))
		}
	}()
	return nil
}

func (h *Hysteria2) DelNode(tag string) error {
	err := h.Hy2nodes[tag].Hy2server.Close()
	if err != nil {
		return err
	}
	delete(h.Hy2nodes, tag)
	return nil
}
