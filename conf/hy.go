package conf

import "time"

type Hysteria2Config struct {
	LogConfig Hysteria2LogConfig `json:"Log"`
}

type Hysteria2LogConfig struct {
	Level string `json:"Level"`
}

func NewHysteria2Config() *Hysteria2Config {
	return &Hysteria2Config{
		LogConfig: Hysteria2LogConfig{
			Level: "error",
		},
	}
}

type Hysteria2Options struct {
	QUICConfig            QUICConfig             `json:"QUICConfig"`
	Outbounds             []Outbounds            `json:"Outbounds"`
	IgnoreClientBandwidth bool                   `json:"IgnoreClientBandwidth"`
	DisableUDP            bool                   `json:"DisableUDP"`
	UDPIdleTimeout        time.Duration          `json:"UDPIdleTimeout"`
	Masquerade            serverConfigMasquerade `json:"masquerade"`
}

type QUICConfig struct {
	InitialStreamReceiveWindow     uint64
	MaxStreamReceiveWindow         uint64
	InitialConnectionReceiveWindow uint64
	MaxConnectionReceiveWindow     uint64
	MaxIdleTimeout                 time.Duration
	MaxIncomingStreams             int64
	DisablePathMTUDiscovery        bool // The server may still override this to true on unsupported platforms.
}

type ServerConfigOutboundDirect struct {
	Mode       string `json:"mode"`
	BindIPv4   string `json:"bindIPv4"`
	BindIPv6   string `json:"bindIPv6"`
	BindDevice string `json:"bindDevice"`
}

type ServerConfigOutboundSOCKS5 struct {
	Addr     string `json:"addr"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type ServerConfigOutboundHTTP struct {
	URL      string `json:"url"`
	Insecure bool   `json:"insecure"`
}

type Outbounds struct {
	Name   string                     `json:"name"`
	Type   string                     `json:"type"`
	Direct ServerConfigOutboundDirect `json:"direct"`
	SOCKS5 ServerConfigOutboundSOCKS5 `json:"socks5"`
	HTTP   ServerConfigOutboundHTTP   `json:"http"`
}

type serverConfigMasqueradeFile struct {
	Dir string `json:"dir"`
}

type serverConfigMasqueradeProxy struct {
	URL         string `json:"url"`
	RewriteHost bool   `json:"rewriteHost"`
}

type serverConfigMasqueradeString struct {
	Content    string            `json:"content"`
	Headers    map[string]string `json:"headers"`
	StatusCode int               `json:"statusCode"`
}

type serverConfigMasquerade struct {
	Type        string                       `json:"type"`
	File        serverConfigMasqueradeFile   `json:"file"`
	Proxy       serverConfigMasqueradeProxy  `json:"proxy"`
	String      serverConfigMasqueradeString `json:"string"`
	ListenHTTP  string                       `json:"listenHTTP"`
	ListenHTTPS string                       `json:"listenHTTPS"`
	ForceHTTPS  bool                         `json:"forceHTTPS"`
}

func NewHysteria2Options() *Hysteria2Options {
	return &Hysteria2Options{
		QUICConfig:            QUICConfig{},
		Outbounds:             []Outbounds{},
		IgnoreClientBandwidth: false,
		DisableUDP:            false,
		UDPIdleTimeout:        time.Duration(time.Duration.Seconds(30)),
		Masquerade:            serverConfigMasquerade{},
	}
}
