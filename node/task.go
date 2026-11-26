package node

import (
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) startTasks(node *panel.NodeInfo) {
	if c.reliableTrafficEnabled {
		c.takeOnlineTraffic() // discard previous runtime device-window state, never billing data
		c.trafficDelivery = newTrafficDelivery(c.apiClient, c.trafficSpool, c.tag)
		go c.trafficDelivery.run()
		seconds := c.TrafficCheckpointSeconds
		if seconds == 0 {
			seconds = 1
		}
		c.lastTrafficSeal = time.Now()
		c.checkpointPeriodic = &task.Task{Interval: time.Duration(seconds) * time.Second, Execute: c.checkpointTrafficTask}
		_ = c.checkpointPeriodic.Start(false)
	}
	// fetch node info task
	c.nodeInfoMonitorPeriodic = &task.Task{
		Interval: node.PullInterval,
		Execute:  c.nodeInfoMonitor,
	}
	// fetch user list task
	c.userReportPeriodic = &task.Task{
		Interval: node.PushInterval,
		Execute:  c.reportUserTrafficTask,
	}
	log.WithField("tag", c.tag).Info("Start monitor node status")
	// delay to start nodeInfoMonitor
	_ = c.nodeInfoMonitorPeriodic.Start(false)
	log.WithField("tag", c.tag).Info("Start report node status")
	_ = c.userReportPeriodic.Start(false)
	if node.Security == panel.Tls {
		switch c.CertConfig.CertMode {
		case "none", "", "file", "self":
		default:
			c.renewCertPeriodic = &task.Task{
				Interval: time.Hour * 24,
				Execute:  c.renewCertTask,
			}
			log.WithField("tag", c.tag).Info("Start renew cert")
			// delay to start renewCert
			_ = c.renewCertPeriodic.Start(true)
		}
	}
	if c.LimitConfig.EnableDynamicSpeedLimit {
		log.Warn("Legacy EnableDynamicSpeedLimit is inactive; use panel dynamic_speed_limit v1 policies")
	}
}

func (c *Controller) nodeInfoMonitor() (err error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	if c.closed {
		return nil
	}
	if c.rebuildRecovery != nil {
		c.trafficLifeMu.Lock()
		err := c.recoverPanelRebuild()
		c.trafficLifeMu.Unlock()
		if err != nil {
			log.WithField("tag", c.tag).WithError(err).Error("Node recovery deferred")
		}
		return nil
	}
	if len(c.pendingUserCleanup) > 0 {
		if err := c.cleanupFailedUserAdd(); err != nil {
			log.WithField("tag", c.tag).WithError(err).Error("Partial user cleanup deferred")
			return nil
		}
	}
	// A failed admission snapshot must not block revocation, or enable new users.
	// get user alive
	newA, err := c.apiClient.GetUserAlive()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get alive list failed")
		c.syncOutageRevocations()
		return nil
	}
	// get node info
	newN, err := c.apiClient.GetNodeInfo()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get node info failed")
		c.syncOutageRevocations()
		return nil
	}
	// get user info
	newU, err := c.apiClient.GetUserList()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	if newN != nil {
		if err := c.rebuildFromPanel(newN, newU, newA); err != nil {
			log.WithField("tag", c.tag).WithError(err).Error("Node update failed; recovery state retained")
		}
		return nil
	}

	// update alive list
	if newA != nil {
		c.limiter.UpdateAliveSnapshot(newA, c.apiClient.AliveMap.Snapshot)
	}
	// node no changed, check users
	if newU == nil {
		return nil
	}
	if err := c.applyUserSnapshot(newU); err != nil {
		log.WithField("tag", c.tag).WithError(err).Error("User synchronization failed; response will be retried")
	}
	return nil
}

func (c *Controller) SpeedChecker() error {
	for u, t := range c.traffic {
		if t >= c.LimitConfig.DynamicSpeedLimitConfig.Traffic {
			err := c.limiter.UpdateDynamicSpeedLimit(c.tag, u,
				c.LimitConfig.DynamicSpeedLimitConfig.SpeedLimit,
				time.Now().Add(time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.ExpireTime)*time.Minute))
			log.WithField("err", err).Error("Update dynamic speed limit failed")
			delete(c.traffic, u)
		}
	}
	return nil
}

// Sync revocations against the running node while admission or config is unavailable.
func (c *Controller) syncOutageRevocations() {
	users, err := c.apiClient.GetUserList()
	if err == nil && users != nil {
		c.trafficLifeMu.Lock()
		err = c.applyRevocationsOnly(users)
		c.trafficLifeMu.Unlock()
	}
	if err != nil {
		log.WithField("tag", c.tag).WithError(err).Error("User revocation synchronization failed")
	}
}
