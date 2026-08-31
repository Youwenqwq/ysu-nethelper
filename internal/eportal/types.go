package eportal

// OnlineStatus 是当前设备的校园网在线状态。
type OnlineStatus struct {
	Online   bool           `json:"online"`
	Username string         `json:"username,omitempty"` // 在线时的准入账号（学工号）
	Name     string         `json:"name,omitempty"`     // 认证账户姓名
	Service  string         `json:"service,omitempty"`  // 网络服务名（如 "校园网"）
	UserIP   string         `json:"user_ip,omitempty"`
	UserMAC  string         `json:"user_mac,omitempty"`
	Message  string         `json:"message,omitempty"` // 服务端原始结果信息
	Raw      map[string]any `json:"raw,omitempty"`     // portalOnlineUserInfo 原始字典，供调试
}
