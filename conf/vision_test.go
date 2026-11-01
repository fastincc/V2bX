package conf

import (
	"encoding/json"
	"testing"
)

func TestVisionPrototypeDefaultAndConfig(t *testing.T) {
	if NewXrayOptions().EnableVisionSplice {
		t.Fatal("default must be off")
	}
	var n NodeConfig
	if e := json.Unmarshal([]byte(`{"Core":"xray","EnableVisionSplice":true}`), &n); e != nil || n.Options.XrayOptions == nil || !n.Options.XrayOptions.EnableVisionSplice {
		t.Fatal("opt-in decode", e)
	}
	var legacy NodeConfig
	if e := json.Unmarshal([]byte(`{"Core":"xray"}`), &legacy); e != nil || legacy.Options.XrayOptions.EnableVisionSplice {
		t.Fatal("legacy enabled", e)
	}
}
