package node

import (
	"crypto/sha256"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	vcore "github.com/InazumaV/V2bX/core"
	"os"
	"path/filepath"
)

func (c *Controller) openTrafficSpool() error {
	if !c.TrafficBatchMode {
		if c.TrafficSpoolDirectory != "" {
			files, err := filepath.Glob(filepath.Join(c.TrafficSpoolDirectory, "*", "pending.json"))
			if err != nil || len(files) != 0 {
				return fmt.Errorf("cannot disable batches with pending traffic")
			}
			if _, err := os.Stat(filepath.Join(c.TrafficSpoolDirectory, "queue-v2")); err == nil {
				return fmt.Errorf("queue migration requires an explicit drain")
			}
		}
		return nil
	}
	if _, ok := c.server.(vcore.TrafficSnapshotter); !ok {
		return fmt.Errorf("core does not support traffic batch snapshots")
	}
	root := c.TrafficSpoolDirectory
	if root == "" {
		return fmt.Errorf("TrafficSpoolDirectory is required for batch mode")
	}
	binding := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", c.apiClient.APIHost, c.apiClient.NodeType, c.apiClient.NodeId))))
	spool, err := trafficbatch.Open(filepath.Join(root, binding), binding)
	if err != nil {
		return err
	}
	c.trafficSpool = spool
	return nil
}
func (c *Controller) collectAndReportTraffic() ([]panel.UserTraffic, error) {
	if !c.TrafficBatchMode {
		rows, err := c.server.GetUserTrafficSlice(c.tag, true)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return rows, c.apiClient.ReportUserTraffic(rows)
	}
	if c.trafficSpool == nil {
		return nil, fmt.Errorf("traffic spool not initialized")
	}
	if c.trafficSpool.Pending() == nil {
		err := c.server.(vcore.TrafficSnapshotter).PersistTrafficSnapshot(c.tag, c.trafficSpool.Save)
		if err != nil {
			return nil, err
		}
	}
	batch := c.trafficSpool.Pending()
	if batch == nil {
		return nil, nil
	}
	if err := c.apiClient.ReportTrafficBatch(batch.ID, batch.Traffic); err != nil {
		return batch.Traffic, err
	}
	return batch.Traffic, c.trafficSpool.Acknowledge(batch.ID)
}
