package panel

import (
	"errors"
	"fmt"
	"github.com/go-resty/resty/v2"
	"net"
)

// Raw request debugging is deliberately disabled: URLs and payloads contain secrets.
func (c *Client) Debug() { c.client.SetDebug(false) }

type discardRequestLogger struct{}

func (discardRequestLogger) Errorf(string, ...interface{}) {}
func (discardRequestLogger) Warnf(string, ...interface{})  {}
func (discardRequestLogger) Debugf(string, ...interface{}) {}

func (c *Client) checkResponse(res *resty.Response, path string, err error) error {
	if err != nil {
		kind := "transport failure"
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			kind = "timeout"
		}
		// path is an internal constant. Never format or wrap the underlying error.
		return fmt.Errorf("panel %s: %s", path, kind)
	}
	if res == nil {
		return fmt.Errorf("panel %s: missing response", path)
	}
	if res.StatusCode() >= 400 {
		return fmt.Errorf("panel %s: HTTP %d", path, res.StatusCode())
	}
	return nil
}
