package xray

import (
	"sync"
	"testing"
)

func TestReportThresholdConcurrentReload(t *testing.T) {
	s := &Xray{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20000; j++ {
				s.setReportMinTraffic("node", int64(j))
				_ = s.reportMinTraffic("node")
			}
		}()
	}
	wg.Wait()
	s.setReportMinTraffic("node", 1234)
	if s.reportMinTraffic("node") != 1234 {
		t.Fatal("threshold update lost")
	}
}
