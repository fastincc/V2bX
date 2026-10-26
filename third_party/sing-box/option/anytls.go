package option

import "github.com/sagernet/sing/common/json/badoption"

type AnyTLSInboundOptions struct {
	AuditAccess bool `json:"audit_access,omitempty"`
	ListenOptions
	InboundTLSOptionsContainer
	Users                []AnyTLSUser                `json:"users,omitempty"`
	PaddingScheme        badoption.Listable[string]  `json:"padding_scheme,omitempty"`
	TrustedProxyProtocol *AnyTLSProxyProtocolOptions `json:"trusted_proxy_protocol,omitempty"`
}

// AnyTLS-only, mandatory pre-TLS PROXY protocol. This deliberately does not
// reuse the removed global listener proxy_protocol options.
type AnyTLSProxyProtocolOptions struct {
	RequireTrustedProxy bool               `json:"require_trusted_proxy,omitempty"`
	Enabled             bool               `json:"enabled,omitempty"`
	TrustedProxies      []string           `json:"trusted_proxies,omitempty"`
	HeaderTimeout       badoption.Duration `json:"header_timeout,omitempty"`
	Version             int                `json:"version,omitempty"`
}

type AnyTLSUser struct {
	AuditID  string `json:"audit_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Password string `json:"password,omitempty"`
}

type AnyTLSOutboundOptions struct {
	DialerOptions
	ServerOptions
	OutboundTLSOptionsContainer
	Password                 string             `json:"password,omitempty"`
	IdleSessionCheckInterval badoption.Duration `json:"idle_session_check_interval,omitempty"`
	IdleSessionTimeout       badoption.Duration `json:"idle_session_timeout,omitempty"`
	MinIdleSession           int                `json:"min_idle_session,omitempty"`
}
