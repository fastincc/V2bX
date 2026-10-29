package node

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	vcore "github.com/InazumaV/V2bX/core"
	log "github.com/sirupsen/logrus"
	"path/filepath"
	"time"
)

func (c *Controller) openTrafficSpool() error {
	if !c.reliableTrafficEnabled {
		return nil
	}
	if _, ok := c.server.(vcore.TrafficSnapshotter); !ok {
		return fmt.Errorf("core does not support traffic batch snapshots")
	}
	if c.TrafficCheckpointSeconds < 0 || c.TrafficCheckpointSeconds > 60 {
		return fmt.Errorf("checkpoint seconds must be 1..60, or zero for default")
	}
	root := c.TrafficSpoolDirectory
	if root == "" {
		return fmt.Errorf("TrafficSpoolDirectory is required for batch mode")
	}
	binding := c.trafficBinding()
	maxBytes := c.TrafficSpoolMaxBytes
	if maxBytes == 0 {
		maxBytes = 64 << 20
	}
	maxBatches := c.TrafficSpoolMaxBatches
	if maxBatches == 0 {
		maxBatches = 4096
	}
	spool, err := trafficbatch.OpenWithLimits(filepath.Join(root, binding), binding, maxBytes, maxBatches)
	if err != nil {
		return err
	}
	c.trafficSpool = spool
	return nil
}
func (c *Controller) collectAndReportTraffic() ([]panel.UserTraffic, error) {
	if !c.reliableTrafficEnabled {
		rows, err := c.server.GetUserTrafficSlice(c.tag, true)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		err = c.apiClient.ReportUserTraffic(rows)
		if err == nil {
			if reclaimer, ok := c.server.(interface{ ReclaimTraffic(string) }); ok {
				reclaimer.ReclaimTraffic(c.tag)
			}
		}
		return rows, err
	}
	if c.trafficSpool == nil {
		return nil, fmt.Errorf("traffic spool not initialized")
	}
	storageErr := c.captureTraffic()
	if storageErr == nil {
		interval := time.Duration(0)
		if c.info != nil {
			interval = c.info.PushInterval
		}
		storageErr = c.trafficSpool.SealIfDue(interval)
	}
	batch := c.trafficSpool.Pending()
	if batch == nil {
		return nil, storageErr
	}
	if storageErr != nil {
		c.logTrafficStorageError(storageErr)
	}
	if err := c.apiClient.ReportTrafficBatch(batch.ID, batch.Traffic); err != nil {
		return batch.Traffic, err
	}
	return batch.Traffic, c.trafficSpool.Acknowledge(batch.ID)
}

func (c *Controller) captureTraffic() error {
	return c.server.(vcore.TrafficSnapshotter).PersistTrafficSnapshot(c.tag, c.trafficSpool.Save)
}
func (c *Controller) checkpointTrafficTask() error {
	c.trafficLifeMu.RLock()
	defer c.trafficLifeMu.RUnlock()
	if c.closed {
		return nil
	}
	if err := c.captureTraffic(); err != nil {
		c.logTrafficStorageError(err)
		return nil
	}
	interval := c.info.PushInterval
	if interval <= 0 {
		interval = time.Minute
	}
	if err := c.trafficSpool.SealIfDue(interval); err != nil {
		c.logTrafficStorageError(err)
	}
	return nil
}

func (c *Controller) logTrafficStorageError(err error) {
	log.WithFields(log.Fields{"tag": c.tag, "error": err}).Error("Traffic checkpoint unavailable; existing journal retained, unsaved counters remain in memory")
}
