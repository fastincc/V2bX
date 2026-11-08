package limiter

import (
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
)

type dynamicEntry struct {
	control     *dynamic.Control
	uid         int
	lastWarning time.Time // accessed only while stateMu is held
	unsupported bool
}
type dynamicPanel struct {
	closed   bool // protected by Limiter.stateMu
	controls sync.Map
	active   sync.Map
	wake     chan struct{}
	done     chan struct{}
	start    sync.Once
	stop     sync.Once
	wg       sync.WaitGroup
}

func (l *Limiter) addDynamicUser(tag string, u panel.UserInfo) {
	key := format.UserTag(tag, u.Uuid)
	e := &dynamicEntry{control: dynamic.New(determineSpeedLimit(l.SpeedLimit, u.SpeedLimit)), uid: u.Id}
	if old, ok := l.dynamic.controls.Swap(key, e); ok {
		old.(*dynamicEntry).control.Close()
	}
}
func (l *Limiter) removeDynamicUser(key string) {
	l.dynamic.active.Delete(key)
	if e, ok := l.dynamic.controls.LoadAndDelete(key); ok {
		e.(*dynamicEntry).control.Close()
	}
}
func (l *Limiter) DynamicControl(key string) *dynamic.Control {
	if v, ok := l.dynamic.controls.Load(key); ok {
		return v.(*dynamicEntry).control
	}
	return nil
}

// Called separately from identity changes so a policy update cannot revoke a
// long-lived stream or clear its device lease/accounting generation.
func (l *Limiter) UpdateDynamicPolicies(tag string, users []panel.UserInfo) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.dynamic.closed {
		return
	}
	for _, u := range users {
		key := format.UserTag(tag, u.Uuid)
		v, ok := l.dynamic.controls.Load(key)
		if !ok {
			continue
		}
		e := v.(*dynamicEntry)
		if e.unsupported {
			if u.DynamicSpeedLimit != nil && u.DynamicSpeedLimit.Enabled && time.Since(e.lastWarning) > time.Minute {
				e.lastWarning = time.Now()
				dynamicDiagnostics.emit(dynamicDiagnostic{at: e.lastWarning, uid: u.Id, kind: dynamicUnsupported})
			}
			continue
		}
		if err := e.control.Apply(u.DynamicSpeedLimit, time.Now()); err != nil {
			if time.Since(e.lastWarning) > time.Minute {
				e.lastWarning = time.Now()
				dynamicDiagnostics.emit(dynamicDiagnostic{at: e.lastWarning, uid: u.Id, kind: dynamicRejected, reason: err.Error()})
			}
			continue
		}
		if e.control.Status().Enabled {
			l.dynamic.active.Store(key, e)
		} else {
			l.dynamic.active.Delete(key)
		}
	}
	has := false
	l.dynamic.active.Range(func(_, _ any) bool { has = true; return false })
	if has {
		l.dynamic.start.Do(func() { l.dynamic.wg.Add(1); go l.runDynamicPolicies() })
	}
	if l.dynamic.wake != nil {
		select {
		case l.dynamic.wake <- struct{}{}:
		default:
		}
	}
}

// Vision's raw splice copy cannot be paced by a buf.Writer wrapper. Preserve
// that existing fast path instead of silently throttling every unlimited user.
// The panel must exclude this path; a misconfiguration is also rejected here.
func (l *Limiter) RejectDynamicPolicyForRawCopy(key string) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if v, ok := l.dynamic.controls.Load(key); ok {
		e := v.(*dynamicEntry)
		if !e.unsupported {
			e.unsupported = true
			wasEnabled := e.control.Status().Enabled
			e.control.Apply(nil, time.Now())
			l.dynamic.active.Delete(key)
			if wasEnabled {
				dynamicDiagnostics.emit(dynamicDiagnostic{at: time.Now(), uid: e.uid, kind: dynamicUnsupported})
			}
		}
	}
}

// Only after the old node has drained: a transport change away from Vision
// must not leave a permanent unsupported flag on otherwise unchanged users.
func (l *Limiter) ResetUnsupportedDynamicPolicies(tag string, users []panel.UserInfo) {
	l.stateMu.Lock()
	if !l.dynamic.closed {
		for _, u := range users {
			key := format.UserTag(tag, u.Uuid)
			if v, ok := l.dynamic.controls.Load(key); ok && v.(*dynamicEntry).unsupported {
				l.removeDynamicUser(key)
				l.addDynamicUser(tag, u)
			}
		}
	}
	l.stateMu.Unlock()
	l.UpdateDynamicPolicies(tag, users)
}
func (l *Limiter) TickDynamicPolicies(now time.Time) {
	l.dynamic.active.Range(func(_, v any) bool {
		e := v.(*dynamicEntry)
		if transition := e.control.Tick(now); transition != "" {
			dynamicDiagnostics.emit(dynamicDiagnostic{at: now, uid: e.uid, kind: dynamicChanged, state: transition})
		}
		return true
	})
}
func (l *Limiter) runDynamicPolicies() {
	defer l.dynamic.wg.Done()
	active := func() bool {
		has := false
		l.dynamic.active.Range(func(_, _ any) bool { has = true; return false })
		return has
	}
	for {
		if !active() {
			select {
			case <-l.dynamic.done:
				return
			case <-l.dynamic.wake:
			}
			continue
		}
		ticker := time.NewTicker(time.Second)
		for active() {
			select {
			case <-l.dynamic.done:
				ticker.Stop()
				return
			case <-l.dynamic.wake:
			case now := <-ticker.C:
				l.TickDynamicPolicies(now)
			}
		}
		ticker.Stop()
	}
}
func (l *Limiter) closeDynamicPolicies() {
	l.dynamic.stop.Do(func() {
		l.stateMu.Lock()
		l.dynamic.closed = true
		if l.dynamic.done != nil {
			close(l.dynamic.done)
		}
		l.stateMu.Unlock()
		l.dynamic.controls.Range(func(_, v any) bool { v.(*dynamicEntry).control.Close(); return true })
		l.dynamic.wg.Wait()
	})
}
