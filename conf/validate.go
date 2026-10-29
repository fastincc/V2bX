package conf

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/InazumaV/V2bX/common/json5"
	"net/url"
	"os"
)

// Validate checks local configuration without opening listeners or contacting a panel.
func (c *Conf) Validate() error {
	if len(c.CoresConfig) == 0 {
		return fmt.Errorf("no cores configured")
	}
	names := map[string]bool{}
	types := map[string]bool{}
	for _, core := range c.CoresConfig {
		switch core.Type {
		case "sing", "xray", "hysteria2":
		default:
			return fmt.Errorf("unsupported core")
		}
		name := core.Name
		if name == "" {
			name = core.Type
		}
		if names[name] {
			return fmt.Errorf("duplicate core name")
		}
		names[name] = true
		types[core.Type] = true
		var paths []string
		if core.SingConfig != nil {
			paths = append(paths, core.SingConfig.OriginalPath)
		}
		if core.XrayConfig != nil {
			x := core.XrayConfig
			paths = append(paths, x.DnsConfigPath, x.RouteConfigPath, x.InboundConfigPath, x.OutboundConfigPath)
		}
		for _, path := range paths {
			if path == "" {
				continue
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return fmt.Errorf("core input unavailable")
			}
			var v any
			if json.NewDecoder(json5.NewTrimNodeReader(bytes.NewReader(b))).Decode(&v) != nil {
				return fmt.Errorf("invalid core input")
			}
		}
	}
	bindings := map[string]bool{}
	for _, node := range c.NodeConfig {
		u, e := url.Parse(node.ApiConfig.APIHost)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid panel address")
		}
		if node.ApiConfig.NodeID <= 0 {
			return fmt.Errorf("invalid node ID")
		}
		key := fmt.Sprintf("%s/%s/%d", node.ApiConfig.APIHost, node.ApiConfig.NodeType, node.ApiConfig.NodeID)
		if bindings[key] {
			return fmt.Errorf("duplicate node binding")
		}
		bindings[key] = true
		if node.Options.CoreName != "" && !names[node.Options.CoreName] {
			return fmt.Errorf("unknown core name")
		}
		if node.Options.Core != "" && !types[node.Options.Core] {
			return fmt.Errorf("unknown node core")
		}
		cc := node.Options.CertConfig
		if cc != nil && cc.CertMode == "file" {
			if _, e := tls.LoadX509KeyPair(cc.CertFile, cc.KeyFile); e != nil {
				return fmt.Errorf("invalid certificate/key pair")
			}
		}
	}
	return nil
}
