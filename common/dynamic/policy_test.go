package dynamic

import "testing"

func TestPolicyBounds(t *testing.T) {
	p := Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1e9, SpeedMbps: 20, DurationSeconds: 300}
	if p.Validate() != nil {
		t.Fatal("valid rule rejected")
	}
	for _, mutate := range []func(*Policy){func(p *Policy) { p.Version = 2 }, func(p *Policy) { p.Revision = -1 }, func(p *Policy) { p.WindowSeconds = 3601 }, func(p *Policy) { p.ThresholdBytes = -1 }, func(p *Policy) { p.SpeedMbps = 0 }, func(p *Policy) { p.DurationSeconds = 86401 }} {
		v := p
		mutate(&v)
		if v.Validate() == nil {
			t.Fatal(v)
		}
	}
	if (&Policy{Version: 1, Revision: 2}).Validate() != nil {
		t.Fatal("explicit disable rejected")
	}
}
