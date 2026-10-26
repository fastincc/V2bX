package limiter

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"sync/atomic"
	"testing"
)

func BenchmarkDeviceAdmission(b *testing.B) {
	for _, limit := range []int{0, 2} {
		for _, mode := range []string{"sequential", "shared_user_parallel", "distinct_users_parallel"} {
			b.Run(fmt.Sprintf("limit%d/%s", limit, mode), func(b *testing.B) {
				Init()
				users := make([]panel.UserInfo, 128)
				tags := make([]string, 128)
				for i := range users {
					users[i] = panel.UserInfo{Id: i + 1, Uuid: fmt.Sprintf("fixture-%d", i), DeviceLimit: limit}
					tags[i] = format.UserTag("bench", users[i].Uuid)
				}
				l := AddLimiter("bench", &conf.LimitConfig{}, users, map[int]int{})
				for _, tag := range tags {
					l.CheckLimit(tag, "192.0.2.1", true, true)
				}
				b.ReportAllocs()
				b.ResetTimer()
				if mode == "sequential" {
					for i := 0; i < b.N; i++ {
						l.CheckLimit(tags[0], "192.0.2.1", true, true)
					}
				} else {
					var next atomic.Uint32
					b.RunParallel(func(pb *testing.PB) {
						idx := 0
						if mode == "distinct_users_parallel" {
							idx = int(next.Add(1)-1) % len(tags)
						}
						for pb.Next() {
							l.CheckLimit(tags[idx], "192.0.2.1", true, true)
						}
					})
				}
			})
		}
	}
}
