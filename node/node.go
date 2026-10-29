package node

import (
	"errors"
	"fmt"
	"os"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
)

type Node struct {
	controllers []*Controller
}

func New() *Node {
	return &Node{}
}

func (n *Node) Start(nodes []conf.NodeConfig, core vCore.Core) error {
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
		err = controller.Start()
		if err != nil {
			return fmt.Errorf("start node controller [%s-%s-%d] error: %s",
				nodes[i].ApiConfig.APIHost,
				nodes[i].ApiConfig.NodeType,
				nodes[i].ApiConfig.NodeID,
				err)
		}
		n.controllers = append(n.controllers, controller)
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
