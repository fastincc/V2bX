package node

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"golang.org/x/time/rate"
)

func deliveryFixture(t *testing.T, host string, id int, dirs ...string) (*trafficDelivery, *trafficbatch.Store) {
	t.Helper()
	api, err := panel.New(&conf.ApiConfig{APIHost: host, NodeType: "anytls", NodeID: id, Key: "synthetic", Timeout: 30})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "spool")
	if len(dirs) > 0 {
		root = dirs[0]
	}
	spool, err := trafficbatch.Open(root, fmt.Sprint(id))
	if err != nil {
		t.Fatal(err)
	}
	d := newTrafficDelivery(api, spool, "test-delivery")
	t.Cleanup(func() { d.cancel(); <-d.done; spool.Close() })
	return d, spool
}
func enqueueDelivery(t *testing.T, s *trafficbatch.Store, rows ...panel.UserTraffic) {
	t.Helper()
	if e := s.Save(rows); e != nil {
		t.Fatal(e)
	}
	if e := s.Seal(); e != nil {
		t.Fatal(e)
	}
}
func awaitDelivered(t *testing.T, s *trafficbatch.Store, timeout time.Duration) {
	t.Helper()
	end := time.Now().Add(timeout)
	for {
		n, active := s.Counts()
		if n == 0 && !active {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("queue remains: %d active=%v", n, active)
		}
		time.Sleep(time.Millisecond)
	}
}
func fastDelivery(d *trafficDelivery) {
	d.policy = deliveryPolicy{time.Millisecond, 20 * time.Millisecond, time.Millisecond, 30 * time.Millisecond, 100 * time.Millisecond, 8}
	d.budget = rate.NewLimiter(10000, 1)
}
func TestDeliveryFIFOWhileProducing(t *testing.T) {
	var mu sync.Mutex
	var values []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rows map[int][]int64
		json.NewDecoder(r.Body).Decode(&rows)
		mu.Lock()
		values = append(values, rows[1][0])
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	fastDelivery(d)
	for i := 1; i <= 8; i++ {
		enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: int64(i)})
	}
	go d.run()
	for i := 9; i <= 28; i++ {
		enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: int64(i)})
		d.notify() // checkpoint announces a newly sealed batch
		time.Sleep(10 * time.Millisecond)
	}
	awaitDelivered(t, s, 3*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(values) != 28 {
		t.Fatalf("lost/duplicate batches %d", len(values))
	}
	for i, v := range values {
		if v != int64(i+1) {
			t.Fatalf("reordered payload %d: %d", i, v)
		}
	}
}
func TestDeliveryBackoffLostAckAndRetryAfter(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	var ids []string
	charged := map[string]int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rows map[int][]int64
		json.NewDecoder(r.Body).Decode(&rows)
		id := r.Header.Get("X-Traffic-Batch-ID")
		mu.Lock()
		times = append(times, time.Now())
		ids = append(ids, id)
		charged[id] = rows[1][0]
		attempt := len(times)
		mu.Unlock()
		switch attempt {
		case 1:
			w.WriteHeader(503)
		case 2:
			fmt.Fprint(w, `{"data":true,"batch_id":"wrong"}`)
		case 3:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		default:
			fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, id)
		}
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	fastDelivery(d)
	enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 7})
	first := s.Pending().ID
	enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 9})
	go d.run()
	// Normal ticks must not bypass failed-request backoff.
	for i := 0; i < 30; i++ {
		d.report()
		time.Sleep(5 * time.Millisecond)
	}
	awaitDelivered(t, s, 4*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(times) != 5 || len(charged) != 2 {
		t.Fatalf("unexpected attempts %d unique %d", len(times), len(charged))
	}
	for i := 0; i < 4; i++ {
		if ids[i] != first {
			t.Fatal("failed head changed or was skipped")
		}
	}
	if times[1].Sub(times[0]) < 30*time.Millisecond || times[3].Sub(times[2]) < time.Second {
		t.Fatal("backoff bypassed")
	}
	total := int64(0)
	for _, v := range charged {
		total += v
	}
	if total != 16 {
		t.Fatal("billing changed")
	}
}
func TestDeliverySharedPanelBudget(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	counts := map[string]int{}
	active := map[string]int{}
	maxActive := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := r.URL.Query().Get("node_id")
		mu.Lock()
		times = append(times, time.Now())
		counts[node]++
		active[node]++
		maxActive = max(maxActive, active[node])
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
		mu.Lock()
		active[node]--
		mu.Unlock()
	}))
	defer srv.Close()
	d1, s1 := deliveryFixture(t, srv.URL, 1)
	d2, s2 := deliveryFixture(t, srv.URL, 2)
	if d1.budget != d2.budget {
		t.Fatal("panel budget not shared")
	}
	for _, d := range []*trafficDelivery{d1, d2} {
		d.policy.spacing = time.Millisecond
		d.policy.roundTime = time.Hour
	}
	for i := 0; i < 5; i++ {
		enqueueDelivery(t, s1, panel.UserTraffic{UID: 1, Upload: 1})
		enqueueDelivery(t, s2, panel.UserTraffic{UID: 1, Upload: 1})
	}
	go d1.run()
	go d2.run()
	wakeDone := make(chan struct{})
	defer close(wakeDone)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-wakeDone:
				return
			case <-ticker.C:
				d1.notify()
				d2.notify()
			}
		}
	}()

	awaitDelivered(t, s1, 4*time.Second)
	awaitDelivered(t, s2, 4*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(times) != 10 || counts["1"] != 5 || counts["2"] != 5 || maxActive != 1 {
		t.Fatal("serialization or progress failed")
	}
	if times[len(times)-1].Sub(times[0]) < 1100*time.Millisecond {
		t.Fatal("shared 8/s budget exceeded")
	}
}
func TestDeliveryRegularTickPreemptsCatchupWait(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	d.budget = rate.NewLimiter(1, 1)
	for i := 0; i < 3; i++ {
		enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 1})
	}
	go d.run()
	deadline := time.Now().Add(time.Second)
	for count.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no request")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	rows, err := d.report()
	if err != nil || len(rows) != 1 || time.Since(start) > 250*time.Millisecond {
		t.Fatal("regular report blocked by catchup budget")
	}
}
func TestDeliveryBlockedHTTPDoesNotHoldControlAndCloseRetainsBatch(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	root := filepath.Join(t.TempDir(), "restart-spool")
	d, s := deliveryFixture(t, srv.URL, 1, root)
	d.catchup = false
	enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 31})
	id := s.Pending().ID
	c := &Controller{apiClient: d.api, trafficSpool: s, trafficDelivery: d, reliableTrafficEnabled: true, nodeRemoved: true, server: &batchCore{counter: counter.NewTrafficCounter()}, Options: &conf.Options{}, info: &panel.NodeInfo{PushInterval: time.Second}}
	go d.run()
	done := make(chan struct{})
	go func() { c.reportUserTrafficTask(); close(done) }()
	<-entered
	got := make(chan struct{})
	go func() { c.operationMu.Lock(); c.operationMu.Unlock(); close(got) }()
	select {
	case <-got:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("HTTP holds operationMu")
	}
	start := time.Now()
	if e := c.Close(); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("close did not cancel HTTP")
	}
	<-done
	if s.Pending() == nil || s.Pending().ID != id {
		t.Fatal("cancel removed unresolved batch")
	}
	reopened, e := trafficbatch.Open(root, "1")
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if reopened.Pending() == nil || reopened.Pending().ID != id {
		t.Fatal("unconfirmed batch did not survive reopen")
	}
}
func TestDeliveryCatchupPreservesDeviceReportingThreshold(t *testing.T) {
	var onlineCalls atomic.Int32
	online := make(chan map[string]json.RawMessage, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/UniProxy/alive" {
			var data map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&data)
			onlineCalls.Add(1)
			online <- data
			fmt.Fprint(w, `{"data":true}`)
			return
		}
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
	}))
	defer srv.Close()
	limiter.Init()
	tag := "delivery-devices"
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "one", DeviceLimit: 1}, {Id: 2, Uuid: "two", DeviceLimit: 1}}, map[int]int{})
	defer limiter.DeleteLimiter(tag)
	for _, u := range []string{"one", "two"} {
		if _, blocked := l.CheckLimit(format.UserTag(tag, u), "192.0.2.1", true, true); blocked {
			t.Fatal("fixture rejected")
		}
	}
	d, s := deliveryFixture(t, srv.URL, 1)
	fastDelivery(d)
	d.catchup = false
	enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 2000})
	for i := 0; i < 6; i++ {
		enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 2000})
	}
	c := &Controller{apiClient: d.api, trafficSpool: s, trafficDelivery: d, reliableTrafficEnabled: true, limiter: l, server: &deviceWindowCore{rows: []panel.UserTraffic{{UID: 1, Upload: 10}, {UID: 2, Upload: 2000}}}, Options: &conf.Options{DeviceOnlineMinTraffic: 1}, info: &panel.NodeInfo{PushInterval: time.Second}}
	go d.run()
	if e := c.reportUserTrafficTask(); e != nil {
		t.Fatal(e)
	}
	data := <-online
	if _, ok := data["1"]; ok {
		t.Fatal("below-threshold device reported")
	}
	if _, ok := data["2"]; !ok {
		t.Fatal("eligible device missing")
	}
	if e := s.Seal(); e != nil {
		t.Fatal(e)
	}
	d.notify()
	awaitDelivered(t, s, 2*time.Second)
	if onlineCalls.Load() != 1 {
		t.Fatal("catchup duplicated online reporting")
	}
	for _, u := range []string{"one", "two"} {
		if _, blocked := l.CheckLimit(format.UserTag(tag, u), "192.0.2.2", true, true); !blocked {
			t.Fatal("catchup reset device admission")
		}
	}
}

type deviceWindowCore struct {
	vcore.Core
	rows []panel.UserTraffic
}

func (c *deviceWindowCore) PersistTrafficSnapshot(_ string, save func([]panel.UserTraffic) error) error {
	if err := save(c.rows); err != nil {
		return err
	}
	c.rows = nil
	return nil
}

func TestDeliveryDefaultPacing(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	for i := 0; i < 5; i++ {
		enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 1})
	}
	go d.run()
	awaitDelivered(t, s, 5*time.Second)
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) < 490*time.Millisecond {
			t.Fatal("per-node catchup limit exceeded")
		}
	}
}
func TestDeliveryAcknowledgementStorageFailurePauses(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		close(entered)
		<-release
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	fastDelivery(d)
	enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 1})
	id := s.Pending().ID
	go d.run()
	<-entered
	s.Close()
	close(release)
	for i := 0; i < 5; i++ {
		d.report()
		time.Sleep(10 * time.Millisecond)
	}
	if count.Load() != 1 || s.Pending().ID != id {
		t.Fatal("storage error continued sending or forgot unacknowledged head")
	}
}
