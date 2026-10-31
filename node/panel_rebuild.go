package node

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
)

// The controller's applied config remains authoritative until the new inbound
// and all its users are ready. Recovery is retried by the existing poll task,
// including when the panel is offline or has reverted to its previous ETag.
type nodeRebuildRecovery struct {
	info         *panel.NodeInfo
	users        []panel.UserInfo
	limiterUsers []panel.UserInfo
	resumeOnly   bool
	cleanup      bool
	// A failed batch may have partially revoked users. Quarantine every target
	// and retry idempotently before restoring any inbound or authorization.
	revocations []panel.UserInfo
}

func (c *Controller) runtimeNodeExists() bool {
	if s, ok := c.server.(interface{ HasNode(string) bool }); ok {
		return s.HasNode(c.tag)
	}
	return !c.nodeRemoved
}
func (c *Controller) reportingReady() {
	state := "disabled"
	if c.reliableTrafficEnabled {
		state = "enabled"
	}
	c.emitReportingStatus(state)
}
func (c *Controller) drainAndSave() error {
	if d, ok := c.server.(interface{ DrainTraffic(string) error }); ok {
		if err := d.DrainTraffic(c.tag); err != nil {
			return err
		}
	}
	if c.reliableTrafficEnabled {
		if err := c.captureTraffic(); err != nil {
			return err
		}
		if err := c.trafficSpool.Seal(); err != nil {
			return err
		}
	}
	return nil
}

// Call with operationMu and trafficLifeMu held; never start duplicate tasks.
func (c *Controller) recoverPanelRebuild() error {
	r := c.rebuildRecovery
	if r == nil {
		return nil
	}
	if len(r.revocations) > 0 {
		if c.runtimeNodeExists() {
			if err := c.server.DelUsers(r.revocations, c.tag, r.info); err != nil {
				return c.quiesceFailedRevocations(fmt.Errorf("retry rebuild revocations: %w", err))
			}
		}
		r.revocations = nil
	}
	if r.resumeOnly {
		if s, ok := c.server.(interface{ ResumeTraffic(string) error }); ok {
			if err := s.ResumeTraffic(c.tag); err != nil {
				return err
			}
		}
	} else {
		if r.cleanup {
			if c.runtimeNodeExists() {
				if err := c.drainAndSave(); err != nil {
					return fmt.Errorf("save partial node before recovery: %w", err)
				}
			}
			if err := c.server.DelNode(c.tag); err != nil {
				return fmt.Errorf("remove partial node: %w", err)
			}
			c.nodeRemoved = true
			r.cleanup = false
		}
		deleted, added := compareUserList(r.limiterUsers, r.users)
		c.limiter.UpdateUser(c.tag, added, deleted)
		c.limiter.UpdateDeviceLimits(c.tag, r.users)
		if err := c.limiter.UpdateRule(&r.info.Rules); err != nil {
			return err
		}
		r.limiterUsers = append([]panel.UserInfo(nil), r.users...)
		// Cleanup must be retried even if an implementation leaves partial state.
		r.cleanup = true
		if err := c.server.AddNode(c.tag, r.info, c.Options); err != nil {
			return fmt.Errorf("restore applied node: %w", err)
		}
		c.nodeRemoved = false
		if _, err := c.server.AddUsers(&vcore.AddUsersParams{Tag: c.tag, Users: r.users, NodeInfo: r.info}); err != nil {
			return fmt.Errorf("restore applied users: %w", err)
		}
	}
	c.rebuildRecovery = nil
	c.reportingReady()
	return nil
}

func (c *Controller) quiesceFailedRevocations(cause error) error {
	if d, ok := c.server.(interface{ DrainTraffic(string) error }); ok {
		return errors.Join(cause, d.DrainTraffic(c.tag))
	}
	return cause
}

func (c *Controller) rebuildFromPanel(next *panel.NodeInfo, users []panel.UserInfo, alive map[int]int) (err error) {
	c.trafficLifeMu.Lock()
	defer c.trafficLifeMu.Unlock()
	// A response may have been fetched before this operation failed. Do not let
	// an unchanged user ETag suppress reapplication on the following poll.
	defer func() {
		if err != nil {
			c.apiClient.InvalidateUserCache()
		}
	}()
	if e := new(limiter.Limiter).UpdateRule(&next.Rules); e != nil {
		return e
	}
	if next.Security == panel.Tls {
		if c.CertConfig == nil {
			return fmt.Errorf("TLS certificate config missing")
		}
		if e := c.requestCert(); e != nil {
			return e
		}
	}
	if users == nil {
		users = append([]panel.UserInfo(nil), c.userList...)
	}
	// Rebuild each inbound's authentication table once, not once per user. A
	// successful batch is fully applied; on error its partial result is unknown.
	// Quarantine the entire requested set rather than guessing which users remain.
	deleted, _ := compareUserList(c.userList, users)
	if len(deleted) > 0 {
		e := c.server.DelUsers(deleted, c.tag, c.info)
		c.forgetAppliedUsers(deleted)
		if e != nil {
			c.rebuildRecovery = &nodeRebuildRecovery{
				info: c.info, users: append([]panel.UserInfo(nil), c.userList...),
				limiterUsers: append([]panel.UserInfo(nil), c.userList...),
				cleanup:      true, revocations: append([]panel.UserInfo(nil), deleted...),
			}
			// Recreate only the surviving users after cleanup. ResumeTraffic alone
			// could revive a paused identity captured before its deletion completed.
			c.emitReportingStatus("unknown")
			return c.quiesceFailedRevocations(fmt.Errorf("revoke before rebuild: %w", e))
		}
	}
	r := &nodeRebuildRecovery{info: c.info, users: append([]panel.UserInfo(nil), c.userList...), limiterUsers: append([]panel.UserInfo(nil), c.userList...), resumeOnly: true}
	c.rebuildRecovery = r
	c.emitReportingStatus("unknown")
	fail := func(cause error) error { return errors.Join(cause, c.recoverPanelRebuild()) }
	if e := c.drainAndSave(); e != nil {
		return fail(fmt.Errorf("drain/checkpoint before rebuild: %w", e))
	}
	r.resumeOnly = false
	r.cleanup = true
	if e := c.server.DelNode(c.tag); e != nil {
		return fail(fmt.Errorf("delete old node: %w", e))
	}
	c.nodeRemoved = true
	removed, added := compareUserList(r.limiterUsers, users)
	c.limiter.UpdateUser(c.tag, added, removed)
	c.limiter.UpdateDeviceLimits(c.tag, users)
	r.limiterUsers = append([]panel.UserInfo(nil), users...)
	if e := c.limiter.UpdateRule(&next.Rules); e != nil {
		return fail(e)
	}
	if alive != nil {
		c.limiter.UpdateAliveSnapshot(alive, c.apiClient.AliveMap.Snapshot)
	}
	if e := c.server.AddNode(c.tag, next, c.Options); e != nil {
		return fail(fmt.Errorf("create candidate node: %w", e))
	}
	c.nodeRemoved = false
	if _, e := c.server.AddUsers(&vcore.AddUsersParams{Tag: c.tag, Users: users, NodeInfo: next}); e != nil {
		return fail(fmt.Errorf("install candidate users: %w", e))
	}
	c.info = next
	c.userList = users
	c.traffic = make(map[string]int64)
	c.rebuildRecovery = nil
	c.apiClient.CommitNodeInfo(next)
	c.apiClient.CommitUserList()
	if t := c.nodeInfoMonitorPeriodic; t != nil && next.PullInterval > 0 && t.Interval != next.PullInterval {
		t.SetInterval(next.PullInterval)
	}
	if t := c.userReportPeriodic; t != nil && next.PushInterval > 0 && t.Interval != next.PushInterval {
		t.SetInterval(next.PushInterval)
	}
	c.reportingReady()
	return nil
}
