package limiter

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	log "github.com/sirupsen/logrus"
)

// Run in an isolated test process: a blocked standard logger must not change
// the logger or leave a blocked background task in unrelated package tests.
// This regression uses only the public pre-fix limiter API so it can run
// unchanged against the release baseline as a negative control.
func TestDynamicDiagnosticsCannotBlockExpiry(t *testing.T) {
	const childMode = "V2BX_TEST_DYNAMIC_LOG_BLOCK"
	if mode := os.Getenv(childMode); mode != "" {
		testDynamicBlockedLogLifecycle(t, mode)
		return
	}
	for _, mode := range []string{"writer", "hook"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDynamicDiagnosticsCannotBlockExpiry$", "-test.v", "-test.timeout=20s")
			cmd.Env = append(os.Environ(), childMode+"="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("blocked %s regression: %v\n%s", mode, err, out)
			}
			t.Logf("%s", out)
		})
	}
}

type dynamicBlockingLog struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *dynamicBlockingLog) block() {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
}
func (b *dynamicBlockingLog) Write(p []byte) (int, error) { b.block(); return len(p), nil }
func (b *dynamicBlockingLog) Fire(*log.Entry) error       { b.block(); return nil }
func (b *dynamicBlockingLog) Levels() []log.Level         { return log.AllLevels }

func testDynamicBlockedLogLifecycle(t *testing.T, mode string) {
	t.Helper()
	Init()
	blocked := &dynamicBlockingLog{entered: make(chan struct{}), release: make(chan struct{})}
	// Release the arbitrary writer before closing limiters, even on a failure
	// against the old implementation, so the negative control terminates cleanly.
	defer func() { close(blocked.release); Init() }()
	log.SetLevel(log.InfoLevel)
	log.SetOutput(io.Discard)
	switch mode {
	case "writer":
		log.SetOutput(blocked)
	case "hook":
		log.AddHook(blocked)
	default:
		t.Fatal("unknown block mode")
	}
	policy := dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 1}
	users := []panel.UserInfo{
		{Id: 1, Uuid: "synthetic-dynamic-one", DeviceLimit: 1, DynamicSpeedLimit: &policy},
		{Id: 2, Uuid: "synthetic-dynamic-two", DeviceLimit: 1, DynamicSpeedLimit: &policy},
		{Id: 3, Uuid: "synthetic-dynamic-fixed", DeviceLimit: 1, SpeedLimit: 2, DynamicSpeedLimit: &policy},
	}
	const tag = "dynamic-blocked-log"
	l := AddLimiter(tag, &conf.LimitConfig{}, users, nil)
	keys := make([]string, len(users))
	controls := make([]*dynamic.Control, len(users))
	for i, u := range users {
		keys[i] = format.UserTag(tag, u.Uuid)
		controls[i] = l.DynamicControl(keys[i])
		if _, reject := l.CheckLimit(keys[i], "192.0.2.10", true, true); reject {
			t.Fatal("initial device admission failed")
		}
	}
	controls[0].Observe(1)
	select {
	case <-blocked.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the real policy transition did not enter the blocking log sink")
	}
	if !controls[0].ActiveLimit() {
		t.Fatal("first policy was not triggered before logging")
	}
	until := controls[0].Status().Until
	type waitResult struct {
		err error
		at  time.Time
	}
	waiter := make(chan waitResult, 1)
	go func() {
		err := controls[0].Wait(1<<30, nil)
		waiter <- waitResult{err: err, at: time.Now()}
	}()
	controls[1].Observe(1)
	controls[2].Observe(1)
	deadline := time.Now().Add(2500 * time.Millisecond)
	for !(controls[1].ActiveLimit() && controls[2].ActiveLimit()) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !controls[1].ActiveLimit() || !controls[2].ActiveLimit() {
		t.Fatal("blocked diagnostic prevented another user from triggering")
	}
	var expiryDelay time.Duration
	select {
	case result := <-waiter:
		expiryDelay = result.at.Sub(until)
		if result.err != nil {
			t.Fatal("expiry waiter did not resume normally", result.err)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("blocked diagnostic kept the expired quota active")
	}
	deadline = time.Now().Add(2500 * time.Millisecond)
	for (controls[1].ActiveLimit() || controls[2].ActiveLimit()) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for i, c := range controls {
		if c.ActiveLimit() || !c.Status().Enabled {
			t.Fatalf("user %d did not expire while preserving its rule", i)
		}
		if l.DynamicControl(keys[i]) != c {
			t.Fatal("expiry replaced the authorization generation")
		}
		bucket, reject := l.CheckLimit(keys[i], "192.0.2.10", true, true)
		if reject || (i < 2 && bucket != nil) || (i == 2 && (bucket == nil || bucket.Rate() != 250000)) {
			t.Fatal("expiry changed device admission or the original fixed limit")
		}
		if _, reject := l.CheckLimit(keys[i], "192.0.2.11", true, true); !reject {
			t.Fatal("expiry cleared the occupied device slot")
		}
	}
	// Exercise both warning branches while the sink remains blocked: malformed
	// revisions and unsupported Vision policies must not hold stateMu for IO.
	beforeRebuild := runtime.NumGoroutine()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		bad := users[0]
		badPolicy := policy
		badPolicy.Revision = 0
		bad.DynamicSpeedLimit = &badPolicy
		l.UpdateDynamicPolicies(tag, []panel.UserInfo{bad})
		l.RejectDynamicPolicyForRawCopy(keys[1])
		l.UpdateDynamicPolicies(tag, []panel.UserInfo{users[1]})
		// More than the diagnostic capacity: reloading nodes cannot accumulate
		// a worker per node or make any lifecycle method wait for the sink.
		for i := 0; i < 300; i++ {
			r := AddLimiter("dynamic-log-rebuild", &conf.LimitConfig{}, users[:1], nil)
			r.RejectDynamicPolicyForRawCopy(format.UserTag("dynamic-log-rebuild", users[0].Uuid))
			DeleteLimiter("dynamic-log-rebuild")
		}
		l.UpdateUser(tag, nil, users)
		DeleteLimiter(tag)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked diagnostic stalled rule update, revoke, or node close/rebuild")
	}
	for _, key := range keys {
		if l.DynamicControl(key) != nil {
			t.Fatal("revoked user's dynamic control survived")
		}
	}
	deadline = time.Now().Add(time.Second)
	for runtime.NumGoroutine() > beforeRebuild+4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	afterRebuild := runtime.NumGoroutine()
	if afterRebuild > beforeRebuild+4 {
		t.Fatalf("repeated rebuild retained goroutines: before=%d after=%d", beforeRebuild, afterRebuild)
	}
	t.Logf("mode=%s expiry_observed_after_deadline=%s users=3 rebuilds=300 goroutines_before=%d goroutines_after=%d device_slots_preserved=true fixed_limit_preserved=true", mode, expiryDelay, beforeRebuild, afterRebuild)
}
