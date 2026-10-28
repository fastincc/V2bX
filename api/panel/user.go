package panel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/vmihailenco/msgpack/v5"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id          int    `json:"id" msgpack:"id"`
	Uuid        string `json:"uuid" msgpack:"uuid"`
	SpeedLimit  int    `json:"speed_limit" msgpack:"speed_limit"`
	DeviceLimit int    `json:"device_limit" msgpack:"device_limit"`
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
	const path = "/api/v1/server/UniProxy/user"
	r, err := c.client.R().
		SetHeader("If-None-Match", c.userEtag).
		SetHeader("X-Response-Format", "msgpack").
		SetDoNotParseResponse(true).
		Get(path)
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
	userlist := &UserListBody{}
	if strings.Contains(r.Header().Get("Content-Type"), "application/x-msgpack") {
		decoder := msgpack.NewDecoder(r.RawResponse.Body)
		if err := decoder.Decode(userlist); err != nil {
			return nil, fmt.Errorf("decode user list error")
		}
	} else {
		dec := jsontext.NewDecoder(r.RawResponse.Body)
		for {
			tok, err := dec.ReadToken()
			if err != nil {
				return nil, fmt.Errorf("decode user list error")
			}
			if tok.Kind() == '"' && tok.String() == "users" {
				break
			}
		}
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, fmt.Errorf("decode user list error")
		}
		if tok.Kind() != '[' {
			return nil, fmt.Errorf(`decode user list error: expected "users" array`)
		}
		for dec.PeekKind() != ']' {
			val, err := dec.ReadValue()
			if err != nil {
				return nil, fmt.Errorf("decode user list error: read user object")
			}
			var u UserInfo
			if err := json.Unmarshal(val, &u); err != nil {
				return nil, fmt.Errorf("decode user list error: unmarshal user error")
			}
			userlist.Users = append(userlist.Users, u)
		}
	}
	c.userEtag = r.Header().Get("ETag")
	return userlist.Users, nil
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
		data[userTraffic[i].UID] = []int64{userTraffic[i].Upload, userTraffic[i].Download}
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
