package node

import (
	"errors"
	"fmt"
	"strings"

	"github.com/InazumaV/V2bX/api/panel"
	vcore "github.com/InazumaV/V2bX/core"
	log "github.com/sirupsen/logrus"
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

// Called with operationMu and trafficLifeMu held. A 304 supplies no snapshot;
// an explicit empty list revokes all identities. Policy-only changes wait for a
// healthy poll so a speed adjustment cannot disconnect an authorized user.
func (c *Controller) applyRevocationsOnly(users []panel.UserInfo) error {
	if users == nil {
		return nil
	}
	c.apiClient.InvalidateUserCache() // Only removals will be applied from this response.
	owners := make(map[string]int, len(users))
	for _, u := range users {
		if u.Id <= 0 || strings.TrimSpace(u.Uuid) == "" || owners[u.Uuid] != 0 {
			return errors.New("invalid or ambiguous identity in user snapshot")
		}
		owners[u.Uuid] = u.Id
	}

	var removed []panel.UserInfo
	for _, u := range c.userList {
		owner, exists := owners[u.Uuid]
		if !exists || owner != u.Id {
			removed = append(removed, u)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	if err := c.revokeOrQuarantine(removed); err != nil {
		return err
	}
	log.WithField("tag", c.tag).Infof("Revoked %d users while panel state is unavailable", len(removed))
	return nil
}

// A core can revoke part of a batch before returning an error. Quarantine all
// requested identities and recreate only the survivors on the next poll.
func (c *Controller) revokeOrQuarantine(deleted []panel.UserInfo) error {
	if len(deleted) == 0 {
		return nil
	}
	err := c.server.DelUsers(deleted, c.tag, c.info)
	c.forgetAppliedUsers(deleted)
	if err == nil {
		return nil
	}
	c.rebuildRecovery = &nodeRebuildRecovery{
		info: c.info, users: append([]panel.UserInfo(nil), c.userList...),
		limiterUsers: append([]panel.UserInfo(nil), c.userList...),
		cleanup:      true, revocations: deleted,
	}
	c.emitReportingStatus("unknown")
	return c.quiesceFailedRevocations(fmt.Errorf("revoke users: %w", err))
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
	c.limiter.UpdateDynamicPolicies(c.tag, users)
	c.userList = append([]panel.UserInfo(nil), users...)
	c.apiClient.CommitUserList()
	return nil
}
