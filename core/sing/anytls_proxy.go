package sing

import (
	"fmt"
	"time"

	"github.com/InazumaV/V2bX/conf"
	"github.com/sagernet/sing-box/common/trustedproxy"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func anyTLSProxyOptions(c *conf.AnyTLSProxyProtocolConfig) (*option.AnyTLSProxyProtocolOptions, error) {
	if c == nil || !c.Enable {
		return nil, nil
	}
	if c.HeaderTimeoutSeconds < 0 || c.HeaderTimeoutSeconds > 30 {
		return nil, fmt.Errorf("AnyTLS proxy HeaderTimeoutSeconds must be 0..30 (0 defaults to 3)")
	}
	timeout := time.Duration(c.HeaderTimeoutSeconds) * time.Second
	if _, err := trustedproxy.NewWithTrust(c.TrustedProxies, timeout, c.Version, c.RequireTrustedProxy); err != nil {
		return nil, fmt.Errorf("AnyTLS proxy configuration: %w", err)
	}
	return &option.AnyTLSProxyProtocolOptions{
		Enabled:             true,
		RequireTrustedProxy: c.RequireTrustedProxy,
		TrustedProxies:      append([]string(nil), c.TrustedProxies...),
		HeaderTimeout:       badoption.Duration(timeout),
		Version:             c.Version,
	}, nil
}
