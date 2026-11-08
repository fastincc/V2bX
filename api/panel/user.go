package panel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/InazumaV/V2bX/common/dynamic"
	"strconv"
	"strings"

	"encoding/json/v2"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id                int             `json:"id" msgpack:"id"`
	Uuid              string          `json:"uuid" msgpack:"uuid"`
	SpeedLimit        int             `json:"speed_limit" msgpack:"speed_limit"`
	DeviceLimit       int             `json:"device_limit" msgpack:"device_limit"`
	DynamicSpeedLimit *dynamic.Policy `json:"dynamic_speed_limit,omitempty" msgpack:"dynamic_speed_limit,omitempty"`
}

type UserListBody struct {
	Users []UserInfo `json:"users" msgpack:"users"`
}

type AliveMap struct {
	Alive    map[int]int     `json:"alive"`
	Snapshot *DeviceSnapshot `json:"device_snapshot"`
}

type DeviceSnapshot struct {
	Version int                    `json:"version"`
	TTL     int                    `json:"ttl"`
	IPs     map[int]map[string]int `json:"ips"`
}

// GetUserList will pull user from v2board
func (c *Client) GetUserList() ([]UserInfo, error) {
	c.pendingUserETag = ""
	c.userResponsePending = false
	const path = "/api/v1/server/UniProxy/user"
	request := c.client.R().
		SetHeader("If-None-Match", c.userEtag).
		SetHeader("X-Response-Format", "msgpack").
		SetHeader(dynamic.CapabilityHeader, "1").
		SetDoNotParseResponse(true)
	if c.visionSplicePrototype {
		request.SetHeader("X-V2bX-Vision-Splice-Prototype", "1")
	} else {
		request.SetHeader("X-V2bX-Dynamic-Speed-Limit-Exclude", "xray-vless-vision")
	}
	r, err := request.Get(path)
	if r == nil || r.RawResponse == nil {
		return nil, fmt.Errorf("received nil response or raw response")
	}
	defer r.RawResponse.Body.Close()

	if r.StatusCode() == 304 {
		return nil, nil
	}

	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}
	if r.StatusCode() != 200 {
		return nil, fmt.Errorf("unexpected user list HTTP status %d", r.StatusCode())
	}
	var users []UserInfo
	if strings.Contains(r.Header().Get("Content-Type"), "application/x-msgpack") {
		users, err = decodeUserListMessagePack(r.RawResponse.Body)
	} else {
		users, err = decodeUserListJSON(r.RawResponse.Body)
	}
	if err != nil {
		// A malformed response is never a revocation snapshot. Retry a full
		// response, even if the panel reused an ETag for the invalid body.
		c.InvalidateUserCache()
		return nil, err
	}
	c.pendingUserETag = r.Header().Get("ETag")
	c.userResponsePending = true
	return users, nil
}

// GetUserAlive will fetch the alive_ip count for users
func (c *Client) GetUserAlive() (map[int]int, error) {
	const path = "/api/v1/server/UniProxy/alivelist"
	r, err := c.client.R().SetHeader("X-Device-IP-Snapshot", "1").ForceContentType("application/json").Get(path)
	// Do not include request URLs or response bodies: node credentials are in the URL.
	if err != nil || r == nil {
		return nil, fmt.Errorf("online state request failed")
	}
	if r.StatusCode() != 200 {
		return nil, fmt.Errorf("online state status %d", r.StatusCode())
	}
	next := &AliveMap{}
	if err := json.Unmarshal(r.Body(), next); err != nil || next.Alive == nil {
		return nil, fmt.Errorf("invalid online state response")
	}
	if next.Snapshot != nil && (next.Snapshot.Version != 1 || next.Snapshot.TTL < 1 || next.Snapshot.TTL > 86400 || next.Snapshot.IPs == nil) {
		return nil, fmt.Errorf("invalid online IP snapshot")
	}
	c.AliveMap = next
	return next.Alive, nil
}

type UserTraffic struct {
	UID      int
	Upload   int64
	Download int64
}

// ReportUserTraffic reports the user traffic
func (c *Client) ReportUserTraffic(userTraffic []UserTraffic) error {
	data := make(map[int][]int64, len(userTraffic))
	for i := range userTraffic {
		row := data[userTraffic[i].UID]
		if row == nil {
			row = make([]int64, 2)
		}
		row[0] += userTraffic[i].Upload
		row[1] += userTraffic[i].Download
		data[userTraffic[i].UID] = row
	}
	const path = "/api/v1/server/UniProxy/push"
	r, err := c.client.R().
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	err = c.checkResponse(r, path, err)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) ReportNodeOnlineUsers(data *map[int][]string, events ...DeviceEvent) error {
	payload := make(map[string]any, len(*data)+1)
	for uid, ips := range *data {
		payload[strconv.Itoa(uid)] = ips
	}
	if len(events) > 0 {
		var batch [16]byte
		if _, err := rand.Read(batch[:]); err != nil {
			return fmt.Errorf("device event batch id unavailable")
		}
		payload["_device_events"] = map[string]any{"batch_id": hex.EncodeToString(batch[:]), "events": events}
	}

	const path = "/api/v1/server/UniProxy/alive"
	r, err := c.client.R().
		SetBody(payload).
		ForceContentType("application/json").
		Post(path)
	err = c.checkResponse(r, path, err)

	if err != nil {
		return err
	}

	return nil
}

// InvalidateUserCache retries a response that was not fully applied.
// CommitUserList acknowledges application, not just a successful HTTP decode.
func (c *Client) CommitUserList() {
	if c.userResponsePending {
		c.userEtag = c.pendingUserETag
		c.userResponsePending = false
		c.pendingUserETag = ""
	}
}
func (c *Client) InvalidateUserCache() {
	c.userEtag = ""
	c.pendingUserETag = ""
	c.userResponsePending = false
}

// ConfigureVisionSplicePrototype is startup-only; it advertises the experimental
// ability separately so a panel can continue excluding it unless opted in.
func (c *Client) ConfigureVisionSplicePrototype(enabled bool) {
	if c.visionSplicePrototype != enabled {
		c.InvalidateUserCache()
	}
	c.visionSplicePrototype = enabled
}
