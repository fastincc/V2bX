package xray

import (
	"context"
	"fmt"
	"strconv"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy"
)

func (c *Xray) GetUserManager(tag string) (proxy.UserManager, error) {
	handler, err := c.ihm.GetHandler(context.Background(), tag)
	if err != nil {
		return nil, fmt.Errorf("no such inbound tag: %s", err)
	}
	inboundInstance, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.GetInbound", tag)
	}
	userManager, ok := inboundInstance.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.UserManager", tag)
	}
	return userManager, nil
}

func (c *Xray) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	userManager, err := c.GetUserManager(tag)
	if err != nil {
		return fmt.Errorf("get user manager error: %s", err)
	}
	var user string
	c.users.mapLock.Lock()
	defer c.users.mapLock.Unlock()
	for i := range users {
		raw := format.UserTag(tag, users[i].Uuid)
		user = c.users.active[raw]
		if user == "" {
			continue
		}
		if userManager.GetUser(context.Background(), user) != nil {
			err = userManager.RemoveUser(context.Background(), user)
			if err != nil && userManager.GetUser(context.Background(), user) != nil {
				return err
			}
		}
		delete(c.users.active, raw)
		c.users.retired[user] = tag
		if v, ok := c.dispatcher.LinkManagers.Load(user); ok {
			v.(*dispatcher.LinkManager).CloseAll()
		}

	}
	return nil
}

func (x *Xray) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	defer x.trafficGate.Lock(tag)()
	trafficSlice := make([]panel.UserTraffic, 0)
	x.users.mapLock.Lock()
	defer x.users.mapLock.Unlock()
	if v, ok := x.dispatcher.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			email := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up, down, ready := traffic.Sample(func() int64 {
				if _, retired := x.users.retired[email]; retired {
					return 0
				}
				return x.reportMinTraffic(tag)
			}(), reset)
			if ready {
				if x.users.uidMap[email] == 0 {
					c.Delete(email)
					return true
				}
				trafficSlice = append(trafficSlice, panel.UserTraffic{
					UID:      x.users.uidMap[email],
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
		x.reapTrafficLocked(tag)
		if len(trafficSlice) == 0 {
			return nil, nil
		}
		return trafficSlice, nil
	}
	return nil, nil
}

func (c *Xray) AddUsers(p *vCore.AddUsersParams) (added int, err error) {
	c.users.mapLock.Lock()
	defer c.users.mapLock.Unlock()
	if c.users.active == nil {
		c.users.active = map[string]string{}
		c.users.retired = map[string]string{}
		c.users.node = map[string]string{}
	}
	var users []*protocol.User
	switch p.NodeInfo.Type {
	case "vmess":
		users = buildVmessUsers(p.Tag, p.Users)
	case "vless":
		users = buildVlessUsers(p.Tag, p.Users, p.VAllss.Flow)
	case "trojan":
		users = buildTrojanUsers(p.Tag, p.Users)
	case "shadowsocks":
		users = buildSSUsers(p.Tag,
			p.Users,
			p.Shadowsocks.Cipher,
			p.Shadowsocks.ServerKey)
	default:
		return 0, fmt.Errorf("unsupported node type: %s", p.NodeInfo.Type)
	}
	man, err := c.GetUserManager(p.Tag)
	if err != nil {
		return 0, fmt.Errorf("get user manager error: %s", err)
	}
	for i, u := range users {
		raw := format.UserTag(p.Tag, p.Users[i].Uuid)
		if _, exists := c.users.active[raw]; exists {
			continue
		}
		c.users.sequence++
		key := "accounting-" + strconv.FormatUint(c.users.sequence, 10)
		u.Email = key
		mUser, err := u.ToMemoryUser()
		if err != nil {
			return 0, err
		}
		c.dispatcher.LinkManagers.Store(key, dispatcher.NewLinkManager(raw))
		c.users.uidMap[key] = p.Users[i].Id
		c.users.node[key] = p.Tag
		err = man.AddUser(context.Background(), mUser)
		if err != nil {
			// A manager may have installed the user before returning an error. Keep a
			// cleanup handle until the controller's compensating deletion succeeds.
			if man.GetUser(context.Background(), key) != nil {
				c.users.active[raw] = key
			}
			c.users.retired[key] = p.Tag
			if v, ok := c.dispatcher.LinkManagers.Load(key); ok {
				v.(*dispatcher.LinkManager).CloseAll()
			}
			return added, err
		}
		c.users.active[raw] = key
		added++
	}
	return added, nil
}
