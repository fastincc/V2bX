package hy2

import (
	"errors"
	"fmt"
	"strings"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type Hysteria2node struct {
	masquerade    *masqueradeServers
	Auth          *V2bX
	Hy2server     server.Server
	Tag           string
	Logger        *zap.Logger
	EventLogger   server.EventLogger
	TrafficLogger server.TrafficLogger
}

func (h *Hysteria2) AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error {
	defer h.trafficGate.Lock(tag)()
	h.nodesMu.Lock()
	defer h.nodesMu.Unlock()
	if _, exists := h.Hy2nodes[tag]; exists {
		return fmt.Errorf("node already exists")
	}
	var err error
	hyconfig := &server.Config{}
	var c serverConfig
	v := viper.New()
	if len(config.Hysteria2ConfigPath) != 0 {
		v.SetConfigFile(config.Hysteria2ConfigPath)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("failed to read server config: %w", err)
		}
		if err := v.Unmarshal(&c); err != nil {
			return fmt.Errorf("failed to parse server config: %w", err)
		}
	}
	auth := &V2bX{tag: tag, usersMap: make(map[string]int)}
	n := Hysteria2node{
		Auth:   auth,
		Tag:    tag,
		Logger: h.Logger,
		EventLogger: &serverLogger{
			Auth:   auth,
			Tag:    tag,
			logger: h.Logger,
		},
		TrafficLogger: &HookServer{
			Auth:                  auth,
			Tag:                   tag,
			logger:                h.Logger,
			ReportMinTrafficBytes: config.ReportMinTraffic * 1024,
		},
	}

	hyconfig, err = n.getHyConfig(info, config, &c)
	if err != nil {
		return err
	}
	hyconfig.Authenticator = auth
	s, err := server.NewServer(hyconfig)
	if err != nil {
		_ = hyconfig.Conn.Close()
		n.masquerade.Close()
		return err
	}
	n.Hy2server = s
	h.Hy2nodes[tag] = n
	n.masquerade.Start()
	go func() {
		if err := s.Serve(); err != nil {
			if !strings.Contains(err.Error(), "quic: server closed") {
				h.Logger.Error("Server Error", zap.Error(err))
			}
		}
	}()
	return nil
}

func (h *Hysteria2) DelNode(tag string) error {
	defer h.trafficGate.Lock(tag)()
	h.nodesMu.RLock()
	n, ok := h.Hy2nodes[tag]
	h.nodesMu.RUnlock()
	if !ok {
		return nil
	}
	n.masquerade.Close()
	if err := n.Hy2server.Close(); err != nil {
		return err
	}
	h.nodesMu.Lock()
	delete(h.Hy2nodes, tag)
	h.nodesMu.Unlock()
	return nil
}

// Close all nodes even when one close fails. Callers hold the all-node gate.
func (h *Hysteria2) closeNodes() error {
	h.nodesMu.RLock()
	nodes := make(map[string]Hysteria2node, len(h.Hy2nodes))
	for tag, n := range h.Hy2nodes {
		nodes[tag] = n
	}
	h.nodesMu.RUnlock()
	var result error
	for tag, n := range nodes {
		n.masquerade.Close()
		if err := n.Hy2server.Close(); err != nil {
			result = errors.Join(result, err)
			continue
		}
		h.nodesMu.Lock()
		delete(h.Hy2nodes, tag)
		h.nodesMu.Unlock()
	}
	return result
}

func (h *Hysteria2) HasNode(tag string) bool {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	_, ok := h.Hy2nodes[tag]
	return ok
}
