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
	operationMu               sync.Mutex
	trafficLifeMu             sync.RWMutex
	checkpointPeriodic        *task.Task
	lastTrafficSeal           time.Time
	closed                    bool
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
		server:    server,
		Options:   config,
		apiClient: api,
	}
	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() (err error) {
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
		if err != nil && c.trafficSpool != nil {
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
	err = c.server.AddNode(c.tag, node, c.Options)
	if err != nil {
		return fmt.Errorf("add new node error: %s", err)
	}
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
	c.startTasks(node)
	c.logTrafficMode()
	return nil
}

// Close implement the Close() function of the service interface
func (c *Controller) Close() error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.trafficLifeMu.Lock()
	defer c.trafficLifeMu.Unlock()
	c.closed = true
	if c.checkpointPeriodic != nil {
		c.checkpointPeriodic.Close()
	}
	if c.TrafficBatchMode && c.trafficSpool != nil {
		if drainer, ok := c.server.(interface{ DrainTraffic(string) error }); ok {
			if err := drainer.DrainTraffic(c.tag); err != nil {
				return err
			}
		}
		if err := c.captureTraffic(); err != nil {
			c.closed = false
			return fmt.Errorf("cannot close with unsaved traffic: %w", err)
		}
		if err := c.trafficSpool.Seal(); err != nil {
			c.closed = false
			return fmt.Errorf("cannot seal closing traffic: %w", err)
		}
	}
	if c.trafficSpool != nil {
		defer c.trafficSpool.Close()
	}
	limiter.DeleteLimiter(c.tag)
	if c.nodeInfoMonitorPeriodic != nil {
		c.nodeInfoMonitorPeriodic.Close()
	}
	if c.userReportPeriodic != nil {
		c.userReportPeriodic.Close()
	}
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
	}
	if c.dynamicSpeedLimitPeriodic != nil {
		c.dynamicSpeedLimitPeriodic.Close()
	}
	if c.onlineIpReportPeriodic != nil {
		c.onlineIpReportPeriodic.Close()
	}
	err := c.server.DelNode(c.tag)
	if err != nil {
		return fmt.Errorf("del node error: %s", err)
	}
	return nil
}

func (c *Controller) buildNodeTag(node *panel.NodeInfo) string {
	return fmt.Sprintf("[%s]-%s:%d", c.apiClient.APIHost, node.Type, node.Id)
}
