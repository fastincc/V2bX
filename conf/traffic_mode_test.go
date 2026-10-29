package conf

import (
	"encoding/json"
	"testing"
)

func TestTrafficModeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input, mode string
		enabled     bool
	}{
		{`{}`, "auto", false}, {`{"TrafficBatchMode":"auto"}`, "auto", false},
		{`{"TrafficBatchMode":true}`, "on", true}, {`{"TrafficBatchMode":false}`, "off", false},
		{`{"TrafficBatchMode":"on"}`, "on", true}, {`{"TrafficBatchMode":"off"}`, "off", false},
	} {
		var o Options
		if err := json.Unmarshal([]byte(tc.input), &o); err != nil {
			t.Fatal(err)
		}
		if o.TrafficReportingMode != tc.mode || o.TrafficBatchMode != tc.enabled {
			t.Fatalf("%s: wrong mode", tc.input)
		}
		// Negotiated runtime state must never rewrite the configured policy.
		o.TrafficBatchMode = !tc.enabled
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		var round Options
		if err = json.Unmarshal(b, &round); err != nil {
			t.Fatal(err)
		}
		if round.TrafficReportingMode != tc.mode {
			t.Fatalf("lost configured policy: %s", b)
		}
	}
	for _, input := range []string{`null`, `1`, `"yes"`, `[]`, `{}`} {
		var o Options
		if json.Unmarshal([]byte(`{"TrafficBatchMode":`+input+`}`), &o) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, input := range []string{
		`{"NodeID":1,"NodeType":"anytls","Options":{"TrafficBatchMode":false}}`,
		`{"NodeID":1,"NodeType":"anytls","TrafficBatchMode":false}`,
	} {
		var n NodeConfig
		if err := json.Unmarshal([]byte(input), &n); err != nil {
			t.Fatal(err)
		}
		if n.Options.TrafficReportingMode != "off" {
			t.Fatal("legacy node layout lost explicit false")
		}
	}
}
