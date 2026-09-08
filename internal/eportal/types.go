package eportal

// OnlineStatus 是当前设备的校园网在线状态。
type OnlineStatus struct {
	Online           bool           `json:"online"`
	Username         string         `json:"username,omitempty"` // 在线时的准入账号（学工号）
	Name             string         `json:"name,omitempty"`     // 认证账户姓名
	Service          string         `json:"service,omitempty"`  // 网络服务名（如 "校园网"）
	UserIP           string         `json:"user_ip,omitempty"`
	UserMAC          string         `json:"user_mac,omitempty"`
	RemainingTraffic string         `json:"remaining_traffic,omitempty"` // 校园网套餐剩余流量（服务端格式）
	Message          string         `json:"message,omitempty"`           // 服务端原始结果信息
	Account          map[string]any `json:"account,omitempty"`           // getAccountInfo 原始 data，供详细输出
	Raw              map[string]any `json:"raw,omitempty"`               // portalOnlineUserInfo 原始字典，供详细输出
}
