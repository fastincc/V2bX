package node

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	vcore "github.com/InazumaV/V2bX/core"
)

func (c *Controller) cleanupFailedUserAdd() error {
	if len(c.pendingUserCleanup) == 0 {
		return nil
	}
	if err := c.server.DelUsers(c.pendingUserCleanup, c.tag, c.info); err != nil {
		return fmt.Errorf("remove partially added users: %w", err)
	}
	c.limiter.UpdateUser(c.tag, nil, c.pendingUserCleanup)
	c.pendingUserCleanup = nil
	return nil
}
func (c *Controller) forgetAppliedUsers(users []panel.UserInfo) {
	removed := make(map[string]bool, len(users))
	for _, u := range users {
		removed[u.Uuid] = true
	}
	kept := make([]panel.UserInfo, 0, len(c.userList))
	for _, u := range c.userList {
		if !removed[u.Uuid] {
			kept = append(kept, u)
		}
	}
	c.userList = kept
	c.limiter.UpdateUser(c.tag, nil, users)
	for _, u := range users {
		delete(c.traffic, u.Uuid)
	}
}

// Keep batch operations on the healthy path: some inbounds rebuild their user
// table per call. Per-user calls would make a large update quadratic.
func (c *Controller) applyUserSnapshot(users []panel.UserInfo) (err error) {
	defer func() {
		if err != nil {
			c.apiClient.InvalidateUserCache()
		}
	}()
	if err = c.cleanupFailedUserAdd(); err != nil {
		return err
	}
	deleted, added := compareUserList(c.userList, users)
	if len(deleted) > 0 {
		if err = c.server.DelUsers(deleted, c.tag, c.info); err != nil {
			return fmt.Errorf("delete users: %w", err)
		}
		// Never roll back a completed revocation if additions fail later.
		c.forgetAppliedUsers(deleted)
	}
	if len(added) > 0 {
		// New sessions must see their limits as soon as the core admits them.
		c.limiter.UpdateUser(c.tag, added, nil)
		if _, err = c.server.AddUsers(&vcore.AddUsersParams{Tag: c.tag, NodeInfo: c.info, Users: added}); err != nil {
			c.pendingUserCleanup = append([]panel.UserInfo(nil), added...)
			return errors.Join(fmt.Errorf("add users: %w", err), c.cleanupFailedUserAdd())
		}
	}
	c.limiter.UpdateDeviceLimits(c.tag, users)
	c.userList = append([]panel.UserInfo(nil), users...)
	c.apiClient.CommitUserList()
	return nil
}
