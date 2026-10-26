package sing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/conf"
)

func TestAnyTLSProxyExplicitDisabled(t *testing.T) {
	for _, c := range []*conf.AnyTLSProxyProtocolConfig{{Enable: false, TrustedProxies: []string{"invalid"}}} {
		o, err := anyTLSProxyOptions(c)
		if err != nil || o != nil {
			t.Fatal("disabled config changed existing behavior", o, err)
		}
	}
}

func TestAnyTLSProxyConfigFromNodeJSON(t *testing.T) {
	var n conf.NodeConfig
	err := json.Unmarshal([]byte(`{"Core":"sing","NodeType":"anytls","NodeID":42,"AnyTLSProxyProtocol":{"Enable":true,"TrustedProxies":["192.0.2.5/32","2001:db8::5/128"],"HeaderTimeoutSeconds":3,"Version":2}}`), &n)
	if err != nil {
		t.Fatal(err)
	}
	o, err := anyTLSProxyOptions(n.Options.SingOptions.AnyTLSProxyProtocol)
	if err != nil || o == nil {
		t.Fatal("node setting was not parsed", err)
	}
	if o.Version != 2 || time.Duration(o.HeaderTimeout) != 3*time.Second || len(o.TrustedProxies) != 2 || n.ApiConfig.NodeID != 42 {
		t.Fatal("config mapping changed", o)
	}
	n.Options.SingOptions.AnyTLSProxyProtocol.TrustedProxies[0] = "192.0.2.9/32"
	if o.TrustedProxies[0] != "192.0.2.5/32" {
		t.Fatal("trust list was aliased")
	}
}

func TestAnyTLSProxyInvalidConfigFailsClosed(t *testing.T) {
	for _, c := range []conf.AnyTLSProxyProtocolConfig{
		{Enable: true, RequireTrustedProxy: true},
		{Enable: true, TrustedProxies: []string{"0.0.0.0/0"}},
		{Enable: true, TrustedProxies: []string{"192.0.2.1/32"}, HeaderTimeoutSeconds: -1},
		{Enable: true, TrustedProxies: []string{"192.0.2.1/32"}, HeaderTimeoutSeconds: 31},
		{Enable: true, TrustedProxies: []string{"192.0.2.1/32"}, Version: 3},
	} {
		if _, err := anyTLSProxyOptions(&c); err == nil {
			t.Fatal("invalid config accepted", c)
		}
	}
}

func TestAnyTLSProxyOptionalTrust(t *testing.T) {
	o, e := anyTLSProxyOptions(&conf.AnyTLSProxyProtocolConfig{Enable: true, Version: 2})
	if e != nil || o == nil || o.RequireTrustedProxy || len(o.TrustedProxies) != 0 {
		t.Fatal("optional trust mapping failed", e)
	}
	o, e = anyTLSProxyOptions(&conf.AnyTLSProxyProtocolConfig{Enable: true, RequireTrustedProxy: true, TrustedProxies: []string{"192.0.2.1/32"}})
	if e != nil || !o.RequireTrustedProxy {
		t.Fatal("strict trust mapping failed", e)
	}
}

func TestAnyTLSProxyEnabledByDefault(t *testing.T) {
	for _, raw := range []string{
		`{"Core":"sing","NodeType":"anytls","NodeID":42}`,
		`{"Core":"sing","NodeType":"anytls","AnyTLSProxyProtocol":{}}`,
		`{"Core":"sing","NodeType":"anytls","AnyTLSProxyProtocol":null}`,
	} {
		var n conf.NodeConfig
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		o, err := anyTLSProxyOptions(n.Options.SingOptions.AnyTLSProxyProtocol)
		if err != nil || o == nil || !o.Enabled || o.Version != 2 || time.Duration(o.HeaderTimeout) != 3*time.Second || o.RequireTrustedProxy || len(o.TrustedProxies) != 0 {
			t.Fatalf("unexpected defaults: %+v %v", o, err)
		}
		if !n.Options.SingOptions.AnyTLSAuditAccess {
			t.Fatal("audit must default enabled")
		}
	}
	var n conf.NodeConfig
	if err := json.Unmarshal([]byte(`{"Core":"sing","NodeType":"anytls","AnyTLSProxyProtocol":{"Enable":false}}`), &n); err != nil {
		t.Fatal(err)
	}
	if o, err := anyTLSProxyOptions(n.Options.SingOptions.AnyTLSProxyProtocol); err != nil || o != nil {
		t.Fatal("explicit disable ignored", o, err)
	}
	a, b := conf.NewSingOptions(), conf.NewSingOptions()
	a.AnyTLSProxyProtocol.Enable = false
	if !b.AnyTLSProxyProtocol.Enable {
		t.Fatal("default settings shared between nodes")
	}
}
