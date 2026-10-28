package panel

import (
	"encoding/json"
	"fmt"
)

// TrafficCapabilities is an authenticated, node-specific deployment contract.
// Retention zero means receipts are retained indefinitely, not disabled.
type TrafficCapabilities struct {
	Version                 int    `json:"version"`
	Enabled                 bool   `json:"enabled"`
	DurableAck              bool   `json:"durable_ack"`
	Deduplication           bool   `json:"deduplication"`
	ReceiptRetentionSeconds *int64 `json:"receipt_retention_seconds"`
}

func (c *Client) CheckTrafficCapabilities() error {
	const path = "/api/v1/server/UniProxy/traffic_capabilities"
	r, err := c.client.R().Get(path)
	if err = c.checkResponse(r, path, err); err != nil {
		return err
	}
	var response struct {
		Data TrafficCapabilities `json:"data"`
	}
	if r.StatusCode() != 200 || json.Unmarshal(r.Body(), &response) != nil {
		return fmt.Errorf("invalid traffic capability response")
	}
	v := response.Data
	if v.Version != 1 || !v.Enabled || !v.DurableAck || !v.Deduplication || v.ReceiptRetentionSeconds == nil || *v.ReceiptRetentionSeconds != 0 {
		return fmt.Errorf("panel must enable durable batch v1 with permanent deduplication receipts for this node")
	}
	return nil
}
