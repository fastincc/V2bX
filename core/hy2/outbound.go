package hy2

import (
	"fmt"
	"net"
	"strings"

	"github.com/InazumaV/V2bX/conf"
	"github.com/apernet/hysteria/extras/outbounds"
)

func serverConfigOutboundDirectToOutbound(c conf.ServerConfigOutboundDirect) (outbounds.PluggableOutbound, error) {
	var mode outbounds.DirectOutboundMode
	switch strings.ToLower(c.Mode) {
	case "", "auto":
		mode = outbounds.DirectOutboundModeAuto
	case "64":
		mode = outbounds.DirectOutboundMode64
	case "46":
		mode = outbounds.DirectOutboundMode46
	case "6":
		mode = outbounds.DirectOutboundMode6
	case "4":
		mode = outbounds.DirectOutboundMode4
	default:
		return nil, fmt.Errorf("outbounds.direct.mode unsupported mode")
	}
	bindIP := len(c.BindIPv4) > 0 || len(c.BindIPv6) > 0
	bindDevice := len(c.BindDevice) > 0
	if bindIP && bindDevice {
		return nil, fmt.Errorf("outbounds.direct cannot bind both IP and device")
	}
	if bindIP {
		ip4, ip6 := net.ParseIP(c.BindIPv4), net.ParseIP(c.BindIPv6)
		if len(c.BindIPv4) > 0 && ip4 == nil {
			return nil, fmt.Errorf("outbounds.direct.bindIPv4 invalid IPv4 address")
		}
		if len(c.BindIPv6) > 0 && ip6 == nil {
			return nil, fmt.Errorf("outbounds.direct.bindIPv6 invalid IPv6 address")
		}
		return outbounds.NewDirectOutboundBindToIPs(mode, ip4, ip6)
	}
	if bindDevice {
		return outbounds.NewDirectOutboundBindToDevice(mode, c.BindDevice)
	}
	return outbounds.NewDirectOutboundSimple(mode), nil
}

func serverConfigOutboundSOCKS5ToOutbound(c conf.ServerConfigOutboundSOCKS5) (outbounds.PluggableOutbound, error) {
	if c.Addr == "" {
		return nil, fmt.Errorf("outbounds.socks5.addr empty socks5 address")
	}
	return outbounds.NewSOCKS5Outbound(c.Addr, c.Username, c.Password), nil
}

func serverConfigOutboundHTTPToOutbound(c conf.ServerConfigOutboundHTTP) (outbounds.PluggableOutbound, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("outbounds.http.url empty http address")
	}
	return outbounds.NewHTTPOutbound(c.URL, c.Insecure)
}
