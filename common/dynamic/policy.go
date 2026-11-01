// Package dynamic implements node-local, panel-managed temporary speed limits.
package dynamic

import (
	"encoding/json"
	"errors"

	"github.com/vmihailenco/msgpack/v5"
)

const CapabilityHeader = "X-V2bX-Dynamic-Speed-Limit"

// Policy is optional on each user. Omission/null in a complete successful user
// response disables the policy. Invalid extensions must not revoke the user.
type Policy struct {
	Version         int   `json:"version" msgpack:"version"`
	Revision        int64 `json:"revision" msgpack:"revision"`
	Enabled         bool  `json:"enabled" msgpack:"enabled"`
	WindowSeconds   int   `json:"window_seconds" msgpack:"window_seconds"`
	ThresholdBytes  int64 `json:"threshold_bytes" msgpack:"threshold_bytes"`
	SpeedMbps       int   `json:"speed_mbps" msgpack:"speed_mbps"`
	DurationSeconds int   `json:"duration_seconds" msgpack:"duration_seconds"`
	Invalid         bool  `json:"-" msgpack:"-"`
}

type wirePolicy Policy

func (p *Policy) UnmarshalJSON(b []byte) error {
	var v wirePolicy
	var state struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		*p = Policy{Invalid: true}
		return nil
	}
	if err := json.Unmarshal(b, &state); err != nil || state.Enabled == nil {
		*p = Policy{Invalid: true}
		return nil
	}
	*p = Policy(v)
	return nil
}

func (p *Policy) UnmarshalMsgpack(b []byte) error {
	var v wirePolicy
	var state struct {
		Enabled *bool `msgpack:"enabled"`
	}
	if err := msgpack.Unmarshal(b, &v); err != nil {
		*p = Policy{Invalid: true}
		return nil
	}
	if err := msgpack.Unmarshal(b, &state); err != nil || state.Enabled == nil {
		*p = Policy{Invalid: true}
		return nil
	}
	*p = Policy(v)
	return nil
}

func (p *Policy) Validate() error {
	if p == nil {
		return nil
	}
	if p.Invalid || p.Version != 1 || p.Revision < 1 {
		return errors.New("invalid dynamic policy version or revision")
	}
	if !p.Enabled {
		return nil
	}
	// Bounded per-user memory and arithmetic. Units are decimal Mbps and bytes.
	if p.WindowSeconds < 1 || p.WindowSeconds > 3600 || p.ThresholdBytes < 1 || p.ThresholdBytes > 1<<60 || p.SpeedMbps < 1 || p.SpeedMbps > 1000000 || p.DurationSeconds < 1 || p.DurationSeconds > 86400 {
		return errors.New("dynamic policy parameters out of range")
	}
	return nil
}
