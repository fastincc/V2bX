package panel

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ReportTrafficBatch requires the panel to persist a deduplicated receipt before
// returning the matching batch_id. Never downgrade this request to legacy push.
func (c *Client) ReportTrafficBatch(id string, rows []UserTraffic) error {
	return c.ReportTrafficBatchContext(context.Background(), id, rows)
}

type trafficRetryError struct {
	error
	after time.Duration
}

// TrafficRetryAfter exposes only a delay, never response bodies or credentials.
func TrafficRetryAfter(err error) time.Duration {
	var retry *trafficRetryError
	if errors.As(err, &retry) {
		return retry.after
	}
	return 0
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

// One HTTP attempt only. The persistent sender schedules any replay with the
// same ID and payload; cancellation does not acknowledge or remove the batch.
func (c *Client) ReportTrafficBatchContext(ctx context.Context, id string, rows []UserTraffic) error {
	data := make(map[int][]int64, len(rows))
	for _, r := range rows {
		if _, ok := data[r.UID]; ok {
			return fmt.Errorf("duplicate batch user")
		}
		data[r.UID] = []int64{r.Upload, r.Download}
	}
	const path = "/api/v1/server/UniProxy/push"
	response, err := c.trafficClient.R().SetContext(ctx).SetHeader("X-Traffic-Batch-ID", id).SetBody(data).Post(path)
	if err = c.checkResponse(response, path, err); err != nil {
		if response != nil && (response.StatusCode() == http.StatusTooManyRequests || response.StatusCode() == http.StatusServiceUnavailable) {
			return &trafficRetryError{err, retryAfter(response.Header().Get("Retry-After"), time.Now())}
		}
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
