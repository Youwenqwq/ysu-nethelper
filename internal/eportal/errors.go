package eportal

import "errors"

// ePortal 侧错误的可分类哨兵。
var (
	// ErrAuth 认证被服务端拒绝（密码错误、账号锁定、准入失败等）。
	ErrAuth = errors.New("eportal: auth rejected")
	// ErrBusiness 准入流程接口返回业务拒绝（envelope code != 200）。
	ErrBusiness = errors.New("eportal: business error")
	// ErrProtocol 响应不符合预期结构。
	ErrProtocol = errors.New("eportal: protocol error")
)
