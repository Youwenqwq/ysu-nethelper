package cas

import "errors"

// CAS 侧错误的可分类哨兵。Daemon 据此决定退避策略：
// ErrLoginFailed/ErrNeedCaptcha/ErrMFARequired 属于硬失败（等人工干预），
// ErrIPBlocked 需要长退避，httpkit.IsNetworkError 为真时只是链路问题。
var (
	// ErrLoginFailed 用户名或密码错误（或服务端明确的凭据拒绝）。
	ErrLoginFailed = errors.New("cas: login failed")
	// ErrNeedCaptcha 服务端要求图形验证码（本项目不处理，人工登录一次后通常消失）。
	ErrNeedCaptcha = errors.New("cas: server requires captcha")
	// ErrMFARequired 服务端要求二次认证（本项目不处理）。
	ErrMFARequired = errors.New("cas: server requires MFA")
	// ErrIPBlocked 当前 IP 被认证网关冻结。
	ErrIPBlocked = errors.New("cas: IP blocked by gateway")
	// ErrNotAuthenticated 未持有有效 TGC。
	ErrNotAuthenticated = errors.New("cas: not authenticated")
	// ErrProtocol 响应不符合预期结构。
	ErrProtocol = errors.New("cas: protocol error")
)
