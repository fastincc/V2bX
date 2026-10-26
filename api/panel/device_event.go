package panel

// DeviceEvent is a bounded aggregate of authenticated device-admission checks.
// Allowed means admission passed, not that the destination connected successfully.
type DeviceEvent struct {
	UserID   int    `json:"user_id"`
	IP       string `json:"ip"`
	Decision string `json:"decision"`
	Count    int    `json:"count"`
	LastSeen int64  `json:"last_seen"`
	Limit    int    `json:"limit"`
}
