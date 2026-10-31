package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestDeliveryControllerNoFalseSuccess(t *testing.T) {
	for _, mode := range []string{"paused", "backoff", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			old := log.StandardLogger().Hooks
			hook := logtest.NewGlobal()
			defer log.StandardLogger().ReplaceHooks(old)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
			}))
			defer srv.Close()
			limiter.Init()
			l := limiter.AddLimiter("status", &conf.LimitConfig{}, nil, nil)
			defer limiter.DeleteLimiter("status")
			d, s := deliveryFixture(t, srv.URL, 1)
			d.catchup = false
			enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 1000})
			if mode == "paused" {
				d.paused = true
			}
			if mode == "backoff" {
				d.retryAt = time.Now().Add(time.Hour)
			}
			c := &Controller{apiClient: d.api, trafficSpool: s, trafficDelivery: d, reliableTrafficEnabled: true, limiter: l, server: &deviceWindowCore{}, Options: &conf.Options{}, info: &panel.NodeInfo{PushInterval: time.Hour}}
			go d.run()
			for i := 0; i < 3; i++ {
				if e := c.reportUserTrafficTask(); e != nil {
					t.Fatal(e)
				}
			}
			success := 0
			for _, entry := range hook.AllEntries() {
				if strings.HasPrefix(entry.Message, "Report ") && strings.HasSuffix(entry.Message, " users traffic") {
					success++
				}
			}
			expected := 0
			if mode == "confirmed" {
				expected = 1
			}
			if success != expected || int(calls.Load()) != expected {
				t.Fatalf("mode=%s success_logs=%d http=%d", mode, success, calls.Load())
			}
			n, _ := s.Counts()
			if (n == 0) != (mode == "confirmed") {
				t.Fatal("queue changed unexpectedly")
			}
		})
	}
}
