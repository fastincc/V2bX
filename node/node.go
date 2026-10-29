package node

import (
	"errors"
	"fmt"
	"os"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
)

var ErrRestoreIncomplete = errors.New("node restoration incomplete")

type Node struct {
	controllers []*Controller
}

func New() *Node {
	return &Node{}
}

func (n *Node) Start(nodes []conf.NodeConfig, core vCore.Core) error {
	nodes = (&conf.Conf{NodeConfig: nodes}).Clone().NodeConfig
	fmt.Fprintln(os.Stdout, "V2BX_REPORTING_RESET_V1")
	// Register every configured node first, including nodes whose startup may
	// never be reached after another node fails.
	for _, n := range nodes {
		writeReportingStatus(os.Stdout, n.ApiConfig.APIHost, n.ApiConfig.NodeType, n.ApiConfig.NodeID, "unknown", n.Options.TrafficReportingMode)
	}
	n.controllers = make([]*Controller, 0, len(nodes))
	for i := range nodes {
		p, err := panel.New(&nodes[i].ApiConfig)
		if err != nil {
			return err
		}
		// Register controller service
		controller := NewController(core, p, &nodes[i].Options)
		n.controllers = append(n.controllers, controller)
		err = controller.Start()
		if err != nil {
			return fmt.Errorf("start node controller [%s-%s-%d] error: %s",
				nodes[i].ApiConfig.APIHost,
				nodes[i].ApiConfig.NodeType,
				nodes[i].ApiConfig.NodeID,
				err)
		}
	}
	return nil
}

func (n *Node) Close() error {
	var result error
	var failed []*Controller
	for _, c := range n.controllers {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil {
			result = errors.Join(result, err)
			failed = append(failed, c)
		}
	}
	n.controllers = failed
	return result
}

// CloseForReload preserves the original controller set on any failure and
// restores every attempted close before returning an error.
func (n *Node) CloseForReload() error {
	for i, c := range n.controllers {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil {
			result := err
			for _, old := range n.controllers[:i+1] {
				if old != nil {
					if restoreErr := old.Restore(); restoreErr != nil {
						result = errors.Join(result, restoreErr, ErrRestoreIncomplete)
					}
				}
			}
			return result
		}
	}
	return nil
}
func (n *Node) Restore() error {
	var result error
	for _, c := range n.controllers {
		if c != nil {
			result = errors.Join(result, c.Restore())
		}
	}
	return result
}

func (n *Node) RestoreOn(core vCore.Core, configs []conf.NodeConfig) error {
	var result error
	for i, c := range n.controllers {
		if c == nil {
			continue
		}
		c.server = core
		c.nodeRemoved = true
		if i < len(configs) {
			saved := (&conf.Conf{NodeConfig: []conf.NodeConfig{configs[i]}}).Clone()
			c.Options = &saved.NodeConfig[0].Options
		}
		result = errors.Join(result, c.Restore())
	}
	return result
}
