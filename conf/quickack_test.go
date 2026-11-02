package conf

import (
	"encoding/json"
	"testing"
)

func TestAnyTLSQuickACKDefaultAndExplicit(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"Core":"sing","NodeType":"anytls"}`, false},
		{`{"Core":"sing","NodeType":"anytls","AnyTLSAdaptiveQuickACK":false}`, false},
		{`{"Core":"sing","NodeType":"anytls","AnyTLSAdaptiveQuickACK":true}`, true},
		{`{"ApiConfig":{"NodeType":"anytls"},"Options":{"Core":"sing","AnyTLSAdaptiveQuickACK":true}}`, true},
	} {
		var n NodeConfig
		if e := json.Unmarshal([]byte(tc.raw), &n); e != nil {
			t.Fatal(e)
		}
		if n.Options.SingOptions.AnyTLSAdaptiveQuickACK != tc.want {
			t.Fatal("wrong default or explicit setting")
		}
	}
}
