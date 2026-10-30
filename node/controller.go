package node

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
)

type Controller struct {
	pendingUserCleanup []panel.UserInfo
	rebuildRecovery    *nodeRebuildRecovery
	// Negotiated runtime state must survive core-specific Options reparsing.
	reliableTrafficEnabled    bool
	operationMu               sync.Mutex
	reportMu                  sync.Mutex
	onlineTrafficMu           sync.Mutex
	onlineTraffic             map[int]int64
	trafficDelivery           *trafficDelivery
	trafficLifeMu             sync.RWMutex
	checkpointPeriodic        *task.Task
	lastTrafficSeal           time.Time
	closed                    bool
	nodeRemoved               bool
	trafficSpool              *trafficbatch.Store
	server                    vCore.Core
	apiClient                 *panel.Client
	tag                       string
	limiter                   *limiter.Limiter
	traffic                   map[string]int64
	userList                  []panel.UserInfo
	aliveMap                  map[int]int
	info                      *panel.NodeInfo
	nodeInfoMonitorPeriodic   *task.Task
	userReportPeriodic        *task.Task
	renewCertPeriodic         *task.Task
	dynamicSpeedLimitPeriodic *task.Task
	onlineIpReportPeriodic    *task.Task
	*conf.Options
}

// NewController return a Node controller with default parameters.
func NewController(server vCore.Core, api *panel.Client, config *conf.Options) *Controller {
	controller := &Controller{
		server:      server,
		nodeRemoved: true,
		Options:     config,
		apiClient:   api,
	}
	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() (err error) {
	c.emitReportingStatus("unknown")
	// First fetch Node Info
	node, err := c.apiClient.GetNodeInfo()
	if err != nil {
		return fmt.Errorf("get node info error: %s", err)
	}
	if err = c.configureTrafficMode(); err != nil {
		log.WithFields(log.Fields{"tag": c.Name, "error": err}).Error("Reliable traffic startup negotiation failed")
		return err
	}
	// Prepare and durably pin reliable mode before opening any listener.
	if err = c.openTrafficSpool(); err != nil {
		log.WithFields(log.Fields{"tag": c.Name, "error": err}).Error("Reliable traffic storage initialization failed")
		return err
	}
	defer func() {
		if err != nil && c.nodeRemoved && c.trafficSpool != nil {
			_ = c.trafficSpool.Close()
			c.trafficSpool = nil
		}
	}()
	// Update user
	c.userList, err = c.apiClient.GetUserList()
	if err != nil {
		return fmt.Errorf("get user list error: %s", err)
	}
	if len(c.userList) == 0 {
		return errors.New("add users error: not have any user")
	}
	c.aliveMap, err = c.apiClient.GetUserAlive()
	if err != nil {
		return fmt.Errorf("failed to get user alive list: %s", err)
	}
	if len(c.Options.Name) == 0 {
		c.tag = c.buildNodeTag(node)
	} else {
		c.tag = c.Options.Name
	}

	// add limiter
	l := limiter.AddLimiter(c.tag, &c.LimitConfig, c.userList, c.aliveMap)
	l.UpdateAliveSnapshot(c.aliveMap, c.apiClient.AliveMap.Snapshot)
	// add rule limiter
	if err = l.UpdateRule(&node.Rules); err != nil {
		return fmt.Errorf("update rule error: %s", err)
	}
	c.limiter = l
	if node.Security == panel.Tls {
		err = c.requestCert()
		if err != nil {
			return fmt.Errorf("request cert error: %s", err)
		}
	}
	// Add new tag
	c.info = node
	err = c.server.AddNode(c.tag, node, c.Options)
	if err != nil {
		return fmt.Errorf("add new node error: %s", err)
	}
	c.nodeRemoved = false
	added, err := c.server.AddUsers(&vCore.AddUsersParams{
		Tag:      c.tag,
		Users:    c.userList,
		NodeInfo: node,
	})
	if err != nil {
		return fmt.Errorf("add users error: %s", err)
	}
	log.WithField("tag", c.tag).Infof("Added %d new users", added)
	c.info = node
	c.apiClient.CommitNodeInfo(node)
	c.apiClient.CommitUserList()
	c.startTasks(node)
	c.logTrafficMode()
	state := "disabled"
	if c.reliableTrafficEnabled {
		state = "enabled"
	}
	c.emitReportingStatus(state)
	return nil
}

// Close implement the Close() function of the service interface
func (c *Controller) tasks() []*task.Task {
	return []*task.Task{c.checkpointPeriodic, c.nodeInfoMonitorPeriodic, c.userReportPeriodic, c.renewCertPeriodic, c.dynamicSpeedLimitPeriodic, c.onlineIpReportPeriodic}
}
func (c *Controller) Close() error {
	c.emitReportingStatus("unknown")
	c.operationMu.Lock()
	c.trafficLifeMu.Lock()
	c.closed = true
	c.trafficLifeMu.Unlock()
	delivery := c.trafficDelivery
	if delivery != nil {
		delivery.cancel()
	}
	for _, t := range c.tasks() {
		if t != nil {
			t.Close()
		}
	}
	c.operationMu.Unlock()
	// No controller locks may be held while callbacks finish.
	for _, t := range c.tasks() {
		if t != nil {
			t.Wait()
		}
	}
	if delivery != nil {
		<-delivery.done
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.trafficLifeMu.Lock()
	defer c.trafficLifeMu.Unlock()
	if c.nodeRemoved {
		limiter.DeleteLimiter(c.tag)
		if c.trafficSpool != nil {
			err := c.trafficSpool.Close()
			c.trafficSpool = nil
			return err
		}
		return nil
	}
	if c.reliableTrafficEnabled && c.trafficSpool != nil {
		if d, ok := c.server.(interface{ DrainTraffic(string) error }); ok {
			if err := d.DrainTraffic(c.tag); err != nil {
				return err
			}
		}
		if err := c.captureTraffic(); err != nil {
			return fmt.Errorf("cannot close with unsaved traffic: %w", err)
		}
		if err := c.trafficSpool.Seal(); err != nil {
			return fmt.Errorf("cannot seal closing traffic: %w", err)
		}
	}
	if err := c.server.DelNode(c.tag); err != nil {
		return fmt.Errorf("delete node failed: %w", err)
	}
	c.nodeRemoved = true
	limiter.DeleteLimiter(c.tag)
	if c.trafficSpool != nil {
		err := c.trafficSpool.Close()
		c.trafficSpool = nil
		return err
	}
	return nil
}

// Restore resumes a failed close or recreates a fully closed inbound using the
// last applied settings. It never refetches possibly-invalid panel configuration.
func (c *Controller) Restore() error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.trafficLifeMu.Lock()
	defer c.trafficLifeMu.Unlock()
	if c.nodeRemoved {
		if err := c.openTrafficSpool(); err != nil {
			return err
		}
		limiter.RestoreLimiter(c.tag, c.limiter)
		if err := c.server.AddNode(c.tag, c.info, c.Options); err != nil {
			return err
		}
		c.nodeRemoved = false
		if _, err := c.server.AddUsers(&vCore.AddUsersParams{Tag: c.tag, Users: c.userList, NodeInfo: c.info}); err != nil {
			return err
		}
		c.nodeRemoved = false
	} else if r, ok := c.server.(interface{ ResumeTraffic(string) error }); ok {
		if err := r.ResumeTraffic(c.tag); err != nil {
			return err
		}
	}
	c.closed = false
	c.startTasks(c.info)
	state := "disabled"
	if c.reliableTrafficEnabled {
		state = "enabled"
	}
	c.emitReportingStatus(state)
	return nil
}

func (c *Controller) buildNodeTag(node *panel.NodeInfo) string {
	return fmt.Sprintf("[%s]-%s:%d", c.apiClient.APIHost, node.Type, node.Id)
}
