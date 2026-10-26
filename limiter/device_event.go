package limiter

import (
	"github.com/InazumaV/V2bX/api/panel"
	"strconv"
	"time"
)

// Called with stateMu held. Keep memory and report size bounded under a flood.
func (l *Limiter) recordDeviceEvent(uid int, ip string, allowed bool, limit int) {
	decision := "denied"
	if allowed {
		decision = "allowed"
	}
	key := strconv.Itoa(uid) + "/" + ip + "/" + decision
	if l.deviceEvents == nil {
		l.deviceEvents = make(map[string]panel.DeviceEvent)
	}
	row, exists := l.deviceEvents[key]
	if !exists && len(l.deviceEvents) >= 2048 {
		return
	}
	row.UserID = uid
	row.IP = ip
	row.Decision = decision
	row.Limit = limit
	row.LastSeen = time.Now().Unix()
	if row.Count < 1000000 {
		row.Count++
	}
	l.deviceEvents[key] = row
}
func (l *Limiter) GetDeviceEvents() []panel.DeviceEvent {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	rows := make([]panel.DeviceEvent, 0, len(l.deviceEvents))
	for _, row := range l.deviceEvents {
		rows = append(rows, row)
	}
	l.deviceEvents = nil
	return rows
}
