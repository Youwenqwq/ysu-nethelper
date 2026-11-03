package eportal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ysunethelper/internal/httpkit"
)

const setServiceURL = baseURL + "/eportal/network/setService"

const (
	switchVerifyTimeout = 20 * time.Second
	switchPollInterval  = time.Second
)

// 编号固定对应门户的常用服务顺序，不受服务列表过滤或排序变化影响。
var switchServiceNames = [...]string{"校园网", "中国电信", "中国移动", "中国联通"}

// ResolveSwitchService 接受固定编号、中文简称、服务全名和英文别名。
func ResolveSwitchService(service string) string {
	service = strings.TrimSpace(service)
	if len(service) == 1 && service[0] >= '0' && service[0] <= '3' {
		return switchServiceNames[service[0]-'0']
	}
	switch strings.ToLower(service) {
	case "校园", "campus":
		return "校园网"
	case "电信", "telecom":
		return "中国电信"
	case "移动", "mobile":
		return "中国移动"
	case "联通", "unicom":
		return "中国联通"
	}
	return service
}

// ServiceNumber 返回固定运营商编号；自定义服务用名称选择。
func ServiceNumber(service string) (int, bool) {
	service = ResolveSwitchService(service)
	for i, name := range switchServiceNames {
		if service == name {
			return i, true
		}
	}
	return 0, false
}

// Service 是网页切换菜单中的服务：Key 用于显示，Value 用于提交。
type Service struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Order *int   `json:"order,omitempty"`
}

// Services 返回当前在线状态和服务器允许选择的服务，无需 CAS 或密码。
func (c *Client) Services(ctx context.Context) (*OnlineStatus, []Service, error) {
	sessionID, status, err := c.onlineSession(ctx)
	if err != nil {
		return status, nil, err
	}
	services, err := c.servicesForSession(ctx, sessionID)
	return status, services, err
}

func (c *Client) onlineSession(ctx context.Context) (string, *OnlineStatus, error) {
	info, err := c.fetchSessionInfo(ctx)
	if err != nil {
		return "", nil, err
	}
	status, err := c.queryStatus(ctx, info["sessionId"])
	if err != nil {
		return "", nil, err
	}
	if !status.Online {
		return "", status, fmt.Errorf("%w: 当前设备未在线，请先使用 login 登录", ErrBusiness)
	}
	return info["sessionId"], status, nil
}

func (c *Client) servicesForSession(ctx context.Context, sessionID string) ([]Service, error) {
	// 网页切换弹窗使用 selectType: 1，区别于首次登录的服务选择。
	data, err := c.postJSON(ctx, serviceSelectionURL, map[string]any{
		"sessionId": sessionID, "selectType": 1,
	})
	if err != nil {
		return nil, err
	}
	var services []Service
	if err := json.Unmarshal(data, &services); err != nil {
		return nil, fmt.Errorf("%w: bad serviceSelection data: %v", ErrProtocol, err)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("%w: 当前会话没有可切换的服务", ErrBusiness)
	}
	for i := range services {
		if strings.TrimSpace(services[i].Value) == "" {
			return nil, fmt.Errorf("%w: serviceSelection contains an empty service value", ErrProtocol)
		}
		if services[i].Key == "" {
			services[i].Key = services[i].Value
		}
	}
	sort.SliceStable(services, func(i, j int) bool {
		if services[i].Order == nil {
			return false
		}
		return services[j].Order == nil || *services[i].Order < *services[j].Order
	})
	return services, nil
}

// IsCurrentService 同时识别接口的内部服务名和展示名。
func IsCurrentService(status *OnlineStatus, service string) bool {
	return status != nil && status.Online && service != "" &&
		(ResolveService(status.Service) == ResolveService(service) || stringField(status.Raw, "service") == service)
}

// SwitchService 移植网页 setService 流程。服务器使用当前在线会话完成切换，
// 客户端不读取密码、不调用 CAS，也不自行 logout/login 或失败后盲目重试。
func (c *Client) SwitchService(ctx context.Context, service string) (*OnlineStatus, error) {
	service = ResolveSwitchService(service)
	if service == "" {
		return nil, fmt.Errorf("%w: 目标运营商不能为空", ErrBusiness)
	}
	sessionID, status, err := c.onlineSession(ctx)
	if err != nil {
		return nil, err
	}
	if IsCurrentService(status, service) {
		return status, nil
	}
	services, err := c.servicesForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var target *Service
	for i := range services {
		if services[i].Value == service || services[i].Key == service {
			target = &services[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: 当前会话未提供服务 %q；使用 tui 查看可选运营商", ErrBusiness, service)
	}
	if IsCurrentService(status, target.Value) {
		return status, nil
	}
	data, err := c.postJSON(ctx, setServiceURL, map[string]any{
		"sessionId": sessionID, "serviceName": target.Value,
		"rememberService": false, "browserFgr": nil,
	})
	if err != nil {
		return nil, fmt.Errorf("切换请求未确认，请用 status 检查当前状态: %w", err)
	}
	var result struct {
		SwitchResult string `json:"switchResult"`
		FailType     string `json:"failType"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%w: bad setService data: %v", ErrProtocol, err)
	}
	if result.SwitchResult != "success" {
		if result.SwitchResult == "" && result.FailType == "" {
			return nil, fmt.Errorf("%w: setService missing switchResult", ErrProtocol)
		}
		hint := "请用 status 检查当前状态"
		if result.FailType == "loginFail" {
			hint = "原服务可能已下线，请用 status 检查；需要恢复时执行 login"
		}
		return nil, fmt.Errorf("%w: 切换失败（%s）：%s；%s", ErrBusiness,
			orDefault(result.FailType, result.SwitchResult), orDefault(result.Message, "服务端未说明原因"), hint)
	}
	// setService 成功表示请求已被接受。NAS 下线旧服务、接入运营商的过程中，
	// 门户可能短暂返回离线或旧服务；立即调用 onlineSession 会把过渡态当成失败。
	// 只轮询状态，不重复 setService，也不通过 logout/login 干扰正在进行的切换。
	verifyCtx, cancel := context.WithTimeout(ctx, switchVerifyTimeout)
	defer cancel()
	return c.waitForService(verifyCtx, *target, switchPollInterval)
}

// waitForService 等待目标服务上线。每轮刷新门户流程会话，避免切换后使用失效
// sessionId 导致伪离线。ctx 限制整个验证窗口，网络中断也不会让等待无限延长。
func (c *Client) waitForService(ctx context.Context, target Service, interval time.Duration) (*OnlineStatus, error) {
	var lastStatus *OnlineStatus
	var lastErr error
	failure := func(err error) error {
		detail := "尚未读到在线状态"
		if lastErr != nil {
			detail = lastErr.Error()
		} else if lastStatus != nil {
			if lastStatus.Online {
				detail = "当前运营商仍为 " + orDefault(lastStatus.Service, "未知")
			} else {
				detail = "当前设备离线"
				if lastStatus.Message != "" {
					detail += "：" + lastStatus.Message
				}
			}
		}
		return fmt.Errorf("服务端已接受切换，但等待 %s 上线未完成（%s）；请用 status 复查: %w", target.Key, detail, err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, failure(err)
		}
		// 与网页成功后刷新页面相同，重新获取真实流程会话。
		info, err := c.fetchSessionInfo(ctx)
		if err == nil {
			lastStatus, err = c.queryStatus(ctx, info["sessionId"])
		}
		lastErr = err
		if err == nil {
			if IsCurrentService(lastStatus, target.Value) || IsCurrentService(lastStatus, target.Key) {
				return lastStatus, nil
			}
		} else if !httpkit.IsNetworkError(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// 明确的业务拒绝或响应结构错误不属于切换过渡态。
			return nil, failure(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, failure(ctx.Err())
		case <-timer.C:
		}
	}
}
