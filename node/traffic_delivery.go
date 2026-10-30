package node

import (
	"context"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

// Limits apply only to extra catch-up attempts; regular reporting has priority.
// Each attempt includes exactly one HTTP request (no hidden transport retries).
type deliveryPolicy struct {
	spacing, roundTime, yield, backoff, maxBackoff time.Duration
	roundBatches                                   int
}

var defaultDeliveryPolicy = deliveryPolicy{500 * time.Millisecond, 2 * time.Second, 250 * time.Millisecond, 2 * time.Second, time.Minute, 8}

type panelDeliveryBudget struct {
	limiter *rate.Limiter
	refs    int
}

var deliveryBudgets = struct {
	sync.Mutex
	panels map[string]*panelDeliveryBudget
}{panels: make(map[string]*panelDeliveryBudget)}

func acquireDeliveryBudget(host string) (*rate.Limiter, func()) {
	key := strings.ToLower(host)
	if u, err := url.Parse(host); err == nil && u.Hostname() != "" {
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		key = strings.ToLower(u.Scheme+"://"+u.Hostname()) + ":" + port
	}
	deliveryBudgets.Lock()
	budget := deliveryBudgets.panels[key]
	if budget == nil {
		budget = &panelDeliveryBudget{limiter: rate.NewLimiter(8, 1)}
		deliveryBudgets.panels[key] = budget
	}
	budget.refs++
	deliveryBudgets.Unlock()
	return budget.limiter, func() {
		deliveryBudgets.Lock()
		budget.refs--
		if budget.refs == 0 {
			delete(deliveryBudgets.panels, key)
		}
		deliveryBudgets.Unlock()
	}
}

type deliveryResult struct {
	rows []panel.UserTraffic
	err  error
}
type deliveryRequest struct{ result chan deliveryResult }
type trafficDelivery struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	regular       chan deliveryRequest
	wake          chan struct{}
	spool         *trafficbatch.Store
	api           *panel.Client
	tag           string
	policy        deliveryPolicy
	budget        *rate.Limiter
	releaseBudget func()
	// The fields below belong exclusively to run's goroutine.
	catchup, paused                          bool
	retryAt, extraAt, roundStart, drainStart time.Time
	failures, roundCount, drained            int
}

func newTrafficDelivery(api *panel.Client, spool *trafficbatch.Store, tag string) *trafficDelivery {
	budget, release := acquireDeliveryBudget(api.APIHost)
	ctx, cancel := context.WithCancel(context.Background())
	return &trafficDelivery{ctx: ctx, cancel: cancel, done: make(chan struct{}), regular: make(chan deliveryRequest, 1), wake: make(chan struct{}, 1), spool: spool, api: api, tag: tag, policy: defaultDeliveryPolicy, budget: budget, releaseBudget: release, catchup: true}
}
func (d *trafficDelivery) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *trafficDelivery) report() ([]panel.UserTraffic, error) {
	req := deliveryRequest{result: make(chan deliveryResult, 1)}
	select {
	case d.regular <- req:
	case <-d.ctx.Done():
		return nil, nil
	}
	select {
	case result := <-req.result:
		return result.rows, result.err
	case <-d.ctx.Done():
		return nil, nil
	}
}
func (d *trafficDelivery) run() {
	defer close(d.done)
	defer d.releaseBudget()
	for {
		if d.ctx.Err() != nil {
			return
		}
		// A regular tick never sits behind a burst of old batches.
		select {
		case req := <-d.regular:
			req.result <- d.attempt(false)
			continue
		default:
		}
		n, _ := d.spool.Counts()
		if n == 0 {
			d.catchup = false
			if !d.drainStart.IsZero() {
				log.WithFields(log.Fields{"tag": d.tag, "batches": d.drained, "elapsed": time.Since(d.drainStart)}).Info("Traffic backlog drained")
				d.drainStart = time.Time{}
				d.drained = 0
			}
		}
		var timer *time.Timer
		var tick <-chan time.Time
		var reservation *rate.Reservation
		if d.catchup && !d.paused && n > 0 {
			now := time.Now()
			next := d.extraAt
			if d.retryAt.After(next) {
				next = d.retryAt
			}
			delay := next.Sub(now)
			if delay <= 0 {
				reservation = d.budget.ReserveN(now, 1)
				delay = reservation.DelayFrom(now)
			}
			timer = time.NewTimer(max(delay, 0))
			tick = timer.C
		}
	waitForDelivery:
		select {
		case <-d.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			if reservation != nil {
				reservation.Cancel()
			}
			return
		case <-d.wake:
			// A checkpoint must not repeatedly cancel an existing panel reservation;
			// doing so could starve a busy node behind other nodes' reservations.
			if d.catchup {
				goto waitForDelivery
			}
			d.catchup = true
		case req := <-d.regular:
			if timer != nil {
				timer.Stop()
			}
			if reservation != nil {
				reservation.Cancel()
			}
			req.result <- d.attempt(false)
		case <-tick:
			if reservation == nil {
				continue
			}
			select {
			case req := <-d.regular:
				reservation.Cancel()
				req.result <- d.attempt(false)
			default:
				d.attempt(true)
			}
		}
	}
}
func (d *trafficDelivery) attempt(extra bool) deliveryResult {
	batch := d.spool.Pending()
	if batch == nil {
		return deliveryResult{}
	}
	result := deliveryResult{rows: batch.Traffic}
	if d.paused || d.ctx.Err() != nil || time.Now().Before(d.retryAt) {
		return result
	}
	d.catchup = true
	now := time.Now()
	if extra {
		n, _ := d.spool.Counts()
		if n > 1 && d.drainStart.IsZero() {
			d.drainStart = now
			d.drained = 0
			log.WithFields(log.Fields{"tag": d.tag, "pending_batches": n}).Info("Traffic backlog catch-up started")
		}
		if d.roundStart.IsZero() {
			d.roundStart = now
		}
		d.roundCount++
	}
	err := d.api.ReportTrafficBatchContext(d.ctx, batch.ID, batch.Traffic)
	if d.ctx.Err() != nil {
		return result
	} // keep ambiguous acknowledgement for replay
	d.extraAt = time.Now().Add(d.policy.spacing)
	if extra && (d.roundCount >= d.policy.roundBatches || time.Since(d.roundStart) >= d.policy.roundTime) {
		d.extraAt = d.extraAt.Add(d.policy.yield + time.Duration(rand.Float64()*float64(d.policy.yield)))
		d.roundStart = time.Time{}
		d.roundCount = 0
	}
	if err != nil {
		delay := d.policy.backoff
		for i := 0; i < d.failures && delay < d.policy.maxBackoff; i++ {
			delay = min(delay*2, d.policy.maxBackoff)
		}
		d.failures = min(d.failures+1, 32)
		delay = min(delay+time.Duration(rand.Float64()*float64(delay)/4), d.policy.maxBackoff)
		delay = max(delay, panel.TrafficRetryAfter(err))
		d.retryAt = time.Now().Add(delay)
		log.WithFields(log.Fields{"tag": d.tag, "retry_in": delay, "error": err}).Info("Reliable traffic attempt deferred; batch retained")
		result.err = err
		return result
	}
	if err = d.spool.Acknowledge(batch.ID); err != nil {
		d.paused = true
		log.WithFields(log.Fields{"tag": d.tag, "error": err}).Error("Reliable traffic acknowledgement storage failed; sender paused until restart")
		result.err = err
		return result
	}
	d.failures = 0
	d.retryAt = time.Time{}
	if !d.drainStart.IsZero() {
		d.drained++
	}
	return result
}
