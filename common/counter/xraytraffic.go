package counter

import (
	"github.com/InazumaV/V2bX/common/dynamic"
	"sync/atomic"

	fstats "github.com/xtls/xray-core/features/stats"
)

var _ fstats.Counter = (*XrayTrafficCounter)(nil)

type XrayTrafficCounter struct {
	V       *atomic.Int64
	Dynamic *dynamic.Control
}

func (c *XrayTrafficCounter) Value() int64 {
	return c.V.Load()
}

func (c *XrayTrafficCounter) Set(newValue int64) int64 {
	return c.V.Swap(newValue)
}

func (c *XrayTrafficCounter) Add(delta int64) int64 {
	c.Dynamic.Observe(delta)
	return c.V.Add(delta)
}
