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
	// Revocations already applied must survive rollback. Commit each successful
	// removal independently so a later removal failure cannot reactivate it.
	deleted, _ := compareUserList(c.userList, users)
	for _, u := range deleted {
		if e := c.server.DelUsers([]panel.UserInfo{u}, c.tag, c.info); e != nil {
			return fmt.Errorf("revoke before rebuild: %w", e)
		}
		c.limiter.UpdateUser(c.tag, nil, []panel.UserInfo{u})
		kept := make([]panel.UserInfo, 0, len(c.userList))
		for _, old := range c.userList {
			if old.Uuid != u.Uuid {
				kept = append(kept, old)
			}
		}
		c.userList = kept
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
