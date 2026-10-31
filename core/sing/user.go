package sing

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/core"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/anytls"
	"github.com/sagernet/sing-box/protocol/hysteria"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/tuic"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/vmess"
)

func (b *Sing) AddUsers(p *core.AddUsersParams) (added int, err error) {
	in, found := b.box.Inbound().Get(p.Tag)
	if !found {
		return 0, errors.New("the inbound not found")
	}
	// Validate the entire addition before touching the inbound or identity maps.
	// Keep the existing byte-prefix key derivation for all valid credentials.
	if p.NodeInfo.Type == "shadowsocks" {
		if err := validateSS2022Credentials(p.Shadowsocks.Cipher, p.Users); err != nil {
			return 0, err
		}
	}
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()

	switch p.NodeInfo.Type {
	case "vless":
		us := make([]option.VLESSUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.VLESSUser{
				Name: p.Users[i].Uuid,
				Flow: p.VAllss.Flow,
				UUID: p.Users[i].Uuid,
			}
		}
		err = in.(*vless.Inbound).AddUsers(us)
	case "vmess":
		us := make([]option.VMessUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.VMessUser{
				Name: p.Users[i].Uuid,
				UUID: p.Users[i].Uuid,
			}
		}
		err = in.(*vmess.Inbound).AddUsers(us)
	case "shadowsocks":
		us := make([]option.ShadowsocksUser, len(p.Users))
		for i := range p.Users {
			var password = p.Users[i].Uuid
			switch p.Shadowsocks.Cipher {
			case "2022-blake3-aes-128-gcm":
				password = base64.StdEncoding.EncodeToString([]byte(password[:16]))
			case "2022-blake3-aes-256-gcm":
				password = base64.StdEncoding.EncodeToString([]byte(password[:32]))
			}
			us[i] = option.ShadowsocksUser{
				Name:     p.Users[i].Uuid,
				Password: password,
			}
		}
		err = in.(*shadowsocks.MultiInbound).AddUsers(us)
	case "trojan":
		us := make([]option.TrojanUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.TrojanUser{
				Name:     p.Users[i].Uuid,
				Password: p.Users[i].Uuid,
			}
		}
		err = in.(*trojan.Inbound).AddUsers(us)
	case "tuic":
		us := make([]option.TUICUser, len(p.Users))
		id := make([]int, len(p.Users))
		for i := range p.Users {
			us[i] = option.TUICUser{
				Name:     p.Users[i].Uuid,
				UUID:     p.Users[i].Uuid,
				Password: p.Users[i].Uuid,
			}
			id[i] = p.Users[i].Id
		}
		err = in.(*tuic.Inbound).AddUsers(us, id)
	case "hysteria":
		us := make([]option.HysteriaUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.HysteriaUser{
				Name:       p.Users[i].Uuid,
				AuthString: p.Users[i].Uuid,
			}
		}
		err = in.(*hysteria.Inbound).AddUsers(us)
	case "hysteria2":
		us := make([]option.Hysteria2User, len(p.Users))
		id := make([]int, len(p.Users))
		for i := range p.Users {
			us[i] = option.Hysteria2User{
				Name:     p.Users[i].Uuid,
				Password: p.Users[i].Uuid,
			}
			id[i] = p.Users[i].Id
		}
		err = in.(*hysteria2.Inbound).AddUsers(us, id)
	case "anytls":
		us := make([]option.AnyTLSUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.AnyTLSUser{
				AuditID:  strconv.Itoa(p.Users[i].Id),
				Name:     p.Users[i].Uuid,
				Password: p.Users[i].Uuid,
			}
		}
		err = in.(*anytls.Inbound).AddUsers(us)
	}
	if err != nil {
		return 0, err
	}
	if b.users.active == nil {
		b.users.active = make(map[userKey]string)
	}
	if b.users.retired == nil {
		b.users.retired = make(map[userKey]bool)
	}
	for _, user := range p.Users {
		raw := userKey{p.Tag, user.Uuid}
		key, ok := b.users.active[raw]
		if ok && b.users.uidMap[userKey{p.Tag, key}] == user.Id {
			continue
		}
		if ok {
			b.users.retired[userKey{p.Tag, key}] = true
		}
		key = user.Uuid
		if _, exists := b.users.uidMap[userKey{p.Tag, key}]; exists {
			b.users.sequence++
			key = user.Uuid + "\x00" + strconv.FormatUint(b.users.sequence, 10)
		}
		b.users.active[raw] = key
		b.users.uidMap[userKey{p.Tag, key}] = user.Id
	}
	return len(p.Users), err
}

func (b *Sing) GetUserTraffic(tag, uuid string, reset bool) (up int64, down int64) {
	defer b.trafficGate.Lock(tag)()
	b.users.mapLock.RLock()
	defer b.users.mapLock.RUnlock()
	if v, ok := b.hookServer.counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		if value, ok := c.Counters.Load(uuid); ok {
			up, down, _ = value.(*counter.TrafficStorage).Sample(-1, reset)
		}
		return
	}
	return 0, 0
}

func (b *Sing) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	defer b.trafficGate.Lock(tag)()
	trafficSlice := make([]panel.UserTraffic, 0)
	indexes := make(map[int]int)
	hook := b.hookServer
	b.users.mapLock.RLock()
	defer b.users.mapLock.RUnlock()
	if v, ok := hook.counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			uuid := key.(string)
			traffic := value.(*counter.TrafficStorage)
			minimum := b.reportMinTraffic(tag)
			if b.users.retired[userKey{tag, uuid}] {
				minimum = 0
			}
			up, down, ready := traffic.Sample(minimum, reset)
			if ready {
				if b.users.uidMap[userKey{tag, uuid}] == 0 {
					c.Delete(uuid)
					return true
				}
				uid := b.users.uidMap[userKey{tag, uuid}]
				if i, ok := indexes[uid]; ok {
					trafficSlice[i].Upload += up
					trafficSlice[i].Download += down
				} else {
					indexes[uid] = len(trafficSlice)
					trafficSlice = append(trafficSlice, panel.UserTraffic{UID: uid, Upload: up, Download: down})
				}
			}
			return true
		})
		if len(trafficSlice) == 0 {
			return nil, nil
		}
		return trafficSlice, nil
	}
	return nil, nil
}

type UserDeleter interface {
	DelUsers(uuid []string) error
}

func (b *Sing) DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error {
	var del UserDeleter
	if i, found := b.box.Inbound().Get(tag); found {
		switch info.Type {
		case "vmess":
			del = i.(*vmess.Inbound)
		case "vless":
			del = i.(*vless.Inbound)
		case "shadowsocks":
			del = i.(*shadowsocks.MultiInbound)
		case "trojan":
			del = i.(*trojan.Inbound)
		case "tuic":
			del = i.(*tuic.Inbound)
		case "hysteria":
			del = i.(*hysteria.Inbound)
		case "hysteria2":
			del = i.(*hysteria2.Inbound)
		case "anytls":
			del = i.(*anytls.Inbound)
		}
	} else {
		return errors.New("the inbound not found")
	}
	uuids := make([]string, len(users))
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	// Revocation belongs to the inbound. Keep the accounting identity and
	// counter for the lifetime of this core: closing a multiplexed transport
	// asynchronously can still account its final bytes after DelUsers returns.
	// These entries never authorize a connection; they only identify old traffic.
	for i := range users {
		uuids[i] = users[i].Uuid
	}

	err := del.DelUsers(uuids)
	if err != nil {
		return err
	}
	if b.users.retired == nil {
		b.users.retired = make(map[userKey]bool)
	}
	for _, user := range users {
		raw := userKey{tag, user.Uuid}
		key, ok := b.users.active[raw]
		if !ok {
			key = user.Uuid
		}
		b.users.retired[userKey{tag, key}] = true
		delete(b.users.active, raw)
	}
	return nil
}

func validateSS2022Credentials(cipher string, users []panel.UserInfo) error {
	required := 0
	switch cipher {
	case "2022-blake3-aes-128-gcm":
		required = 16
	case "2022-blake3-aes-256-gcm":
		required = 32
	default:
		return nil
	}
	for _, user := range users {
		if len(user.Uuid) < required {
			return fmt.Errorf("SS2022 user %d: credential has %d bytes; %s requires at least %d", user.Id, len(user.Uuid), cipher, required)
		}
	}
	return nil
}
