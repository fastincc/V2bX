package node

import (
	"crypto/sha256"
	"fmt"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
)

func (c *Controller) trafficBinding() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", c.apiClient.APIHost, c.apiClient.NodeType, c.apiClient.NodeId))))
}

func (c *Controller) configureTrafficMode() error {
	mode := c.TrafficReportingMode
	if mode == "" { // Programmatically constructed options also default to auto.
		mode = "auto"
		if c.Options.TrafficBatchMode {
			mode = "on"
		}
	}
	c.TrafficReportingMode = mode
	if mode != "auto" && mode != "on" && mode != "off" {
		return fmt.Errorf("invalid traffic mode")
	}
	if c.TrafficSpoolDirectory == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("cannot determine traffic directory; set TrafficSpoolDirectory")
		}
		c.TrafficSpoolDirectory = filepath.Join(root, "V2bX", "traffic")
	}
	// The store's durable v2 pending.json marker survives acknowledgement of the
	// last batch, so an empty queue must not be interpreted as permission to downgrade.
	if _, err := os.Lstat(filepath.Join(c.TrafficSpoolDirectory, "queue-v2")); err == nil {
		return fmt.Errorf("legacy queue migration requires an explicit drain")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect legacy traffic state")
	}
	dir := filepath.Join(c.TrafficSpoolDirectory, c.trafficBinding())
	pinned := false
	for _, name := range []string{"pending.json", "active.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			pinned = true
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect traffic mode state")
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "batch-*.json"))
	if err != nil {
		return fmt.Errorf("cannot inspect traffic queue")
	}
	pinned = pinned || len(files) != 0
	if mode == "off" {
		if pinned {
			return fmt.Errorf("reliable traffic state exists; explicit drain and migration required before disabling")
		}
		c.reliableTrafficEnabled = false
		return nil
	}
	available, err := c.apiClient.TrafficBatchAvailable()
	if err != nil {
		return fmt.Errorf("traffic mode negotiation failed: %w", err)
	}
	if !available && (mode == "on" || pinned) {
		return fmt.Errorf("panel reliable traffic unavailable; refusing forced-mode startup or downgrade")
	}
	c.reliableTrafficEnabled = available
	return nil
}

// Log only after successful startup, so a storage or authentication failure is
// never reported as an enabled, functioning reporter.
func (c *Controller) logTrafficMode() {
	fields := log.Fields{"tag": c.tag, "mode": c.TrafficReportingMode, "reliable_reporting": c.reliableTrafficEnabled}
	if c.reliableTrafficEnabled {
		log.WithFields(fields).Info("Reliable traffic reporting enabled")
	} else {
		reason := "explicitly disabled by configuration"
		if c.TrafficReportingMode == "auto" {
			reason = "panel explicitly disabled or endpoint unavailable (404/405)"
		}
		fields["reason"] = reason
		log.WithFields(fields).Info("Reliable traffic reporting disabled; using legacy reporting")
	}
}
