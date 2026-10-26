package limiter

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
)

func deviceFixture(limit int) (*Limiter, string) {
	Init()
	l := AddLimiter("test", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "test-user", DeviceLimit: limit}}, map[int]int{})
	return l, format.UserTag("test", "test-user")
}
func TestDeviceConcurrentAdmission(t *testing.T) {
	l, u := deviceFixture(2)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, reject := l.CheckLimit(u, fmt.Sprintf("192.0.2.%d", i+1), true, true); !reject {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("accepted %d", accepted.Load())
	}
}
func TestDeviceReportingAndMappedIP(t *testing.T) {
	l, u := deviceFixture(2)
	for _, ip := range []string{"192.0.2.1", "::ffff:192.0.2.1", "192.0.2.2"} {
		if _, r := l.CheckLimit(u, ip, true, true); r {
			t.Fatal(ip)
		}
	}
	for i := 0; i < 3; i++ {
		l.GetOnlineDevice()
		if _, r := l.CheckLimit(u, "192.0.2.3", true, true); !r {
			t.Fatal("report reset admitted third IP")
		}
	}
	if _, r := l.CheckLimit(u, "192.0.2.1", true, true); r {
		t.Fatal("same IP rejected")
	}
}
func TestDeviceHotUpdateWithoutUserRemoval(t *testing.T) {
	l, u := deviceFixture(0)
	l.UpdateDeviceLimits("test", []panel.UserInfo{{Uuid: "test-user", DeviceLimit: 2}})
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		l.CheckLimit(u, ip, true, true)
	}
	if _, r := l.CheckLimit(u, "192.0.2.3", true, true); !r {
		t.Fatal("hot limit ignored")
	}
	l.UpdateDeviceLimits("test", []panel.UserInfo{{Uuid: "test-user", DeviceLimit: 0}})
	if _, r := l.CheckLimit(u, "192.0.2.3", true, true); r {
		t.Fatal("zero limit rejected")
	}
}
func TestDeviceActiveAndExpiry(t *testing.T) {
	l, u := deviceFixture(1)
	l.CheckLimit(u, "192.0.2.1", true, true)
	a, b := net.Pipe()
	defer b.Close()
	conn := l.TrackDeviceConn(a, u, "192.0.2.1")
	l.deviceLeases[u]["192.0.2.1"].expires = time.Now().Add(-time.Hour)
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); !r {
		t.Fatal("active slot expired")
	}
	conn.Close()
	conn.Close()
	if l.deviceLeases[u]["192.0.2.1"].active != 0 {
		t.Fatal("duplicate release")
	}
	l.deviceLeases[u]["192.0.2.1"].expires = time.Now().Add(-time.Hour)
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); r {
		t.Fatal("expired slot not released")
	}
}
func TestDeviceUpdatesConcurrentWithReports(t *testing.T) {
	l, u := deviceFixture(2)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.UpdateAliveList(map[int]int{1: 1})
				l.UpdateDeviceLimits("test", []panel.UserInfo{{Uuid: "test-user", DeviceLimit: 2}})
				l.CheckLimit(u, "192.0.2.1", true, true)
				l.GetOnlineDevice()
			}
		}()
	}
	wg.Wait()
}

func TestDeviceEventsSeparateFromOnlineCounts(t *testing.T) {
	l, u := deviceFixture(2)
	l.CheckLimit(u, "192.0.2.1", true, true)
	l.CheckLimit(u, "192.0.2.2", true, true)
	l.CheckLimit(u, "192.0.2.3", true, true)
	l.CheckLimit(u, "192.0.2.3", true, true)
	online, _ := l.GetOnlineDevice()
	if len(*online) != 2 {
		t.Fatal("denied source polluted online count")
	}
	events := l.GetDeviceEvents()
	if len(events) != 3 {
		t.Fatal("missing decision rows")
	}
	denied := 0
	for _, e := range events {
		if e.Decision == "denied" {
			denied += e.Count
			if e.IP != "192.0.2.3" || e.UserID != 1 || e.Limit != 2 {
				t.Fatal("wrong decision identity")
			}
		}
	}
	if denied != 2 || len(l.GetDeviceEvents()) != 0 {
		t.Fatal("aggregation/drain failed")
	}
}

func TestDeviceSnapshotCrossNodeRestartAndExpiry(t *testing.T) {
	l, u := deviceFixture(2)
	snapshot := &panel.DeviceSnapshot{Version: 1, TTL: 180, IPs: map[int]map[string]int{1: {"192.0.2.1": 180, "::ffff:192.0.2.2": 180}}}
	l.UpdateAliveSnapshot(map[int]int{1: 2}, snapshot)
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, r := l.CheckLimit(u, ip, true, true); r {
			t.Fatal("known source rejected", ip)
		}
	}
	if _, r := l.CheckLimit(u, "192.0.2.3", true, true); !r {
		t.Fatal("third source admitted")
	}
	// A leaves while B remains. An updated snapshot and expired local A lease
	// must release exactly one slot, without disconnecting B.
	l.stateMu.Lock()
	l.deviceLeases[u]["192.0.2.1"].expires = time.Now().Add(-time.Second)
	l.stateMu.Unlock()
	l.UpdateAliveSnapshot(map[int]int{1: 1}, &panel.DeviceSnapshot{Version: 1, TTL: 180, IPs: map[int]map[string]int{1: {"192.0.2.2": 180}}})
	if _, r := l.CheckLimit(u, "192.0.2.3", true, true); r {
		t.Fatal("released slot unavailable")
	}
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); r {
		t.Fatal("B interrupted")
	}
	if _, r := l.CheckLimit(u, "192.0.2.4", true, true); !r {
		t.Fatal("union not enforced")
	}
}
func TestDeviceExpiredSnapshotFailsClosedForNewSource(t *testing.T) {
	l, u := deviceFixture(2)
	l.UpdateAliveSnapshot(map[int]int{}, &panel.DeviceSnapshot{Version: 1, TTL: 180, IPs: map[int]map[string]int{}})
	if _, r := l.CheckLimit(u, "192.0.2.1", true, true); r {
		t.Fatal("initial source")
	}
	l.stateMu.Lock()
	l.snapshotUntil = time.Now().Add(-time.Second)
	l.stateMu.Unlock()
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); !r {
		t.Fatal("outage opened new slot")
	}
	if _, r := l.CheckLimit(u, "192.0.2.1", true, true); r {
		t.Fatal("outage interrupted local lease")
	}
}
func TestDeviceActiveLeaseContinuesReporting(t *testing.T) {
	l, u := deviceFixture(2)
	l.CheckLimit(u, "192.0.2.1", true, true)
	release := l.TrackDevice(u, "192.0.2.1")
	for i := 0; i < 3; i++ {
		rows, _ := l.GetOnlineDevice()
		if len(*rows) != 1 {
			t.Fatal("active stream disappeared")
		}
	}
	release()
	rows, _ := l.GetOnlineDevice()
	if len(*rows) != 0 {
		t.Fatal("idle source kept alive indefinitely")
	}
}
