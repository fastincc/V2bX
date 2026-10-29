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

// TrafficBatchAvailable distinguishes explicit absence from an uncertain failure.
func (c *Client) TrafficBatchAvailable() (bool, error) {
	const path = "/api/v1/server/UniProxy/traffic_capabilities"
	r, err := c.client.R().Get(path)
	if err == nil && r != nil && (r.StatusCode() == 404 || r.StatusCode() == 405) {
		return false, nil
	}
	if err = c.checkResponse(r, path, err); err != nil {
		return false, err
	}
	var response struct {
		Data *struct {
			Version       *int   `json:"version"`
			Enabled       *bool  `json:"enabled"`
			DurableAck    *bool  `json:"durable_ack"`
			Deduplication *bool  `json:"deduplication"`
			Retention     *int64 `json:"receipt_retention_seconds"`
		} `json:"data"`
	}
	if r.StatusCode() != 200 || json.Unmarshal(r.Body(), &response) != nil || response.Data == nil {
		return false, fmt.Errorf("invalid traffic capability response")
	}
	v := response.Data
	if v.Version == nil || *v.Version != 1 || v.Enabled == nil || v.DurableAck == nil || v.Deduplication == nil || v.Retention == nil {
		return false, fmt.Errorf("invalid traffic capability contract")
	}
	if !*v.Enabled {
		return false, nil
	}
	if !*v.DurableAck || !*v.Deduplication || *v.Retention != 0 {
		return false, fmt.Errorf("panel must support durable batch v1 with permanent deduplication receipts")
	}
	return true, nil
}

func (c *Client) CheckTrafficCapabilities() error {
	available, err := c.TrafficBatchAvailable()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("panel has not enabled reliable traffic for this node")
	}
	return nil
}
