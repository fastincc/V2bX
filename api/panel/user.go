package panel

import (
	"fmt"

	"github.com/goccy/go-json"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id          int    `json:"id"`
	Uuid        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

type UserListBody struct {
	//Msg  string `json:"msg"`
	Users []UserInfo `json:"users"`
}

type AliveMap struct {
	Alive map[int]int `json:"alive"`
}

// GetUserList will pull user from v2board
func (c *Client) GetUserList() ([]UserInfo, error) {
	const path = "/api/v1/server/UniProxy/user"
	r, err := c.client.R().
		SetHeader("If-None-Match", c.userEtag).
		ForceContentType("application/json").
		Get(path)
	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}
	if r != nil {
		defer r.RawResponse.Body.Close()
	} else {
		return nil, fmt.Errorf("received nil response")
	}

	if r.StatusCode() == 304 {
		return nil, nil
	} else {
		if err := json.Unmarshal(r.Body(), c.UserList); err != nil {
			return nil, fmt.Errorf("unmarshal user list error: %w", err)
		}
		c.userEtag = r.Header().Get("ETag")
	}
	return c.UserList.Users, nil
}

// GetUserAlive will fetch the alive IPs for users
func (c *Client) GetUserAlive() (map[int]int, error) {
	const path = "/api/v1/server/UniProxy/alivelist"
	r, err := c.client.R().
		ForceContentType("application/json").
		Get(path)
	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}

	if r != nil {
		defer r.RawResponse.Body.Close()
	} else {
		return nil, fmt.Errorf("received nil response")
	}

	if err := json.Unmarshal(r.Body(), c.AliveMap); err != nil {
		return nil, fmt.Errorf("unmarshal user alive list error: %s", err)
	}

	return c.AliveMap.Alive, nil
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

func (c *Client) ReportNodeOnlineUsers(data *map[int][]string, reportOnline *map[int]int) error {
	c.LastReportOnline = *reportOnline
	const path = "/api/v1/server/UniProxy/alive"
	r, err := c.client.R().
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	err = c.checkResponse(r, path, err)

	if err != nil {
		return nil
	}

	return nil
}
