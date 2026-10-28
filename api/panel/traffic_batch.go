package panel

import (
	"encoding/json/v2"
	"fmt"
)

// ReportTrafficBatch requires the panel to persist a deduplicated receipt before
// returning the matching batch_id. Never downgrade this request to legacy push.
func (c *Client) ReportTrafficBatch(id string, rows []UserTraffic) error {
	data := make(map[int][]int64, len(rows))
	for _, r := range rows {
		if _, ok := data[r.UID]; ok {
			return fmt.Errorf("duplicate batch user")
		}
		data[r.UID] = []int64{r.Upload, r.Download}
	}
	const path = "/api/v1/server/UniProxy/push"
	response, err := c.client.R().SetHeader("X-Traffic-Batch-ID", id).SetBody(data).Post(path)
	if err = c.checkResponse(response, path, err); err != nil {
		return err
	}
	var ack struct {
		Data bool   `json:"data"`
		ID   string `json:"batch_id"`
	}
	if response.StatusCode() != 200 || json.Unmarshal(response.Body(), &ack) != nil || !ack.Data || ack.ID != id {
		return fmt.Errorf("traffic batch not durably acknowledged")
	}
	return nil
}
