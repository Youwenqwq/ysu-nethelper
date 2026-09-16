package selfsvc

import "errors"

// 自助服务侧错误的可分类哨兵。
var (
	// ErrAuth 委托认证未完成（TGC 有效但回跳后未进入自助服务）。
	ErrAuth = errors.New("selfsvc: auth rejected")
	// ErrBusiness 接口返回业务拒绝（envelope code != 200 或 success=false）。
	ErrBusiness = errors.New("selfsvc: business error")
	// ErrProtocol 响应不符合预期结构。
	ErrProtocol = errors.New("selfsvc: protocol error")
)
