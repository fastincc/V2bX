package node

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
)

// Dedicated stdout protocol for service managers. Never include host, tag,
// credentials or user information. Unlike application logs this is not filtered
// by Log.Level or redirected by Log.Output.
func writeReportingStatus(w io.Writer, host, protocol string, nodeID int, state, mode string) {
	switch state {
	case "enabled", "disabled":
	default:
		state = "unknown"
	}
	switch mode {
	case "auto", "on", "off":
	default:
		mode = "unknown"
	}
	kind := strings.ToLower(protocol)
	if kind == "v2ray" {
		kind = "vmess"
	}
	switch kind {
	case "anytls", "trojan", "vmess", "vless", "shadowsocks", "hysteria", "hysteria2", "tuic":
	default:
		kind = "unknown"
	}
	binding := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", host, kind, nodeID))))
	fmt.Fprintf(w, "V2BX_REPORTING_STATUS_V1 binding=%s node_id=%d node_type=%s state=%s mode=%s\n", binding, nodeID, kind, state, mode)
}

func (c *Controller) emitReportingStatus(state string) {
	if c.apiClient == nil {
		return
	}
	writeReportingStatus(os.Stdout, c.apiClient.APIHost, c.apiClient.NodeType, c.apiClient.NodeId, state, c.TrafficReportingMode)
}
