// Package eportal 实现锐捷 ePortal（auth1.ysu.edu.cn）校园网认证客户端。
//
// 只实现「CAS 委托认证」路径（与学校官方认证方式一致）：
// portal 流程会话 → cas-sso 登录页绑定 SESSION cookie → clientredirect
// 跳到 CAS 网关出票 → 带 ticket 回跳 → serviceSelection/serviceLogin 准入。
// 账密直登 portal（内嵌 cas-sso，强制图形验证码）的路径刻意不实现——
// Daemon 无人值守跑不通验证码，也没有必要。
//
// 协议细节移植自 ysu-sdk（Python 参考实现），关键坑见各函数注释。
package eportal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ysunethelper/internal/cas"
	"ysunethelper/internal/httpkit"
)

const (
	baseURL = "https://auth1.ysu.edu.cn"

	portalRedirectURL    = baseURL + "/eportal/redirect.jsp?mode=history"
	getOnlineUserInfoURL = baseURL + "/eportal/adaptor/getOnlineUserInfo"
	serviceSelectionURL  = baseURL + "/eportal/network/serviceSelection"
	serviceLoginURL      = baseURL + "/eportal/network/serviceLogin"
	userOnlineURL        = baseURL + "/eportal/network/userOnline"
	offlineURL           = baseURL + "/eportal/network/offline"

	casSSOLoginURL = baseURL + "/cas-sso/login"
	// 「统一身份认证」外部提供者入口（CAS 委托认证）
	clientRedirectURL = baseURL + "/cas-sso/clientredirect?client_name=sidadapter"
)

// ServiceAliases 服务英文别名 → 服务端服务名。
var ServiceAliases = map[string]string{
	"campus":  "校园网",
	"unicom":  "中国联通",
	"telecom": "中国电信",
	"mobile":  "中国移动",
}

// ResolveService 把别名解析为服务端服务名；未知名原样返回。
func ResolveService(s string) string {
	if v, ok := ServiceAliases[s]; ok {
		return v
	}
	return s
}

// Client 是 ePortal 门户客户端。
type Client struct {
	hc *httpkit.Client
}

// New 构造客户端；timeout 作用于每次 HTTP 请求。
func New(timeout time.Duration) *Client {
	return &Client{hc: httpkit.NewClient(timeout)}
}

// GetStatus 查询当前设备的在线状态（无需认证凭据）。
//
// getOnlineUserInfo 按 sessionId 出记录，无效 sessionId 会返回伪造的离线
// 记录，因此必须先经 portal 入口拿到真实流程会话。副作用：设备离线时每次
// 调用都会在 portal 侧创建一个流程会话（与浏览器打开认证页行为一致）。
func (c *Client) GetStatus(ctx context.Context) (*OnlineStatus, error) {
	info, err := c.fetchSessionInfo(ctx)
	if err != nil {
		return nil, err
	}
	return c.queryStatus(ctx, info["sessionId"])
}

// LoginViaCAS 通过统一身份认证（CAS 委托）完成校园网认证。
// casClient 必须已持有有效 TGC；TGC 失效报 cas.ErrNotAuthenticated，
// 由调用方先行重新 CAS 登录。已在线时直接返回当前状态。
func (c *Client) LoginViaCAS(ctx context.Context, casClient *cas.Client, service string) (*OnlineStatus, error) {
	service = ResolveService(service)
	info, err := c.fetchSessionInfo(ctx)
	if err != nil {
		return nil, err
	}
	sessionID := info["sessionId"]
	status, err := c.queryStatus(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if status.Online {
		return status, nil
	}

	// GET 登录页：把流程会话绑定到 SESSION cookie（委托认证依赖此绑定）
	if _, err := c.hc.Follow(ctx, c.casSSOLoginURL(info), 10); err != nil {
		return nil, err
	}

	// clientredirect → CAS 登录 URL（service 参数即回跳地址）
	resp, err := c.hc.Get(ctx, clientRedirectURL)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	if !strings.Contains(location, "/authserver/login") {
		return nil, fmt.Errorf("%w: clientredirect did not point to CAS login: %q", ErrProtocol, location)
	}
	serviceURL := parseQueryParam(location, "service")
	if serviceURL == "" {
		return nil, fmt.Errorf("%w: CAS login URL missing service param: %q", ErrProtocol, location)
	}

	// 用 CAS 侧的 TGC 出票；ticket 消费与流程推进落在本 session 上
	ticket, err := casClient.GetServiceTicket(ctx, serviceURL)
	if err != nil {
		return nil, err
	}
	sep := "?"
	if strings.Contains(serviceURL, "?") {
		sep = "&"
	}
	// 回跳后页面可能用 JS 跳转继续流程（auth-success 等），Follow 一并处理
	if _, err := c.hc.Follow(ctx, serviceURL+sep+"ticket="+url.QueryEscape(ticket), 10); err != nil {
		return nil, err
	}

	return c.finishAdmission(ctx, sessionID, service)
}

// Logout 登出当前设备；已离线时为 no-op。
func (c *Client) Logout(ctx context.Context) error {
	info, err := c.fetchSessionInfo(ctx)
	if err != nil {
		return err
	}
	status, err := c.queryStatus(ctx, info["sessionId"])
	if err != nil {
		return err
	}
	if !status.Online {
		return nil
	}
	_, err = c.postJSON(ctx, offlineURL, map[string]string{"sessionId": info["sessionId"]})
	return err
}

// finishAdmission 是 cas-sso 认证完成后的准入收尾。
//
// 认证成功后流程通常停在服务选择节点：先查 userOnline，未在线则走
// serviceSelection → serviceLogin 完成准入，最后复查 userOnline。
func (c *Client) finishAdmission(ctx context.Context, sessionID, service string) (*OnlineStatus, error) {
	online, err := c.checkUserOnline(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !online {
		if _, err := c.postJSON(ctx, serviceSelectionURL, map[string]string{"sessionId": sessionID}); err != nil {
			return nil, err
		}
		data, err := c.postJSON(ctx, serviceLoginURL, map[string]string{
			"sessionId": sessionID,
			"service":   service,
		})
		if err != nil {
			return nil, err
		}
		var lr struct {
			AuthResult  string `json:"authResult"`
			AuthMessage string `json:"authMessage"`
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &lr); err != nil {
				return nil, fmt.Errorf("%w: bad serviceLogin data: %v", ErrProtocol, err)
			}
			switch lr.AuthResult {
			case "", "success":
			case "fail":
				return nil, fmt.Errorf("%w: 准入失败: %s", ErrAuth, orDefault(lr.AuthMessage, "未知原因"))
			default:
				return nil, fmt.Errorf("%w: unexpected authResult from serviceLogin: %q", ErrProtocol, lr.AuthResult)
			}
		}
		online, err = c.checkUserOnline(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if !online {
			return nil, fmt.Errorf("%w: 登录校验失败: 认证后用户不在线", ErrAuth)
		}
	}
	return c.queryStatus(ctx, sessionID)
}

// checkUserOnline 查 userOnline 接口，返回是否在线。
func (c *Client) checkUserOnline(ctx context.Context, sessionID string) (bool, error) {
	data, err := c.postJSON(ctx, userOnlineURL, map[string]string{"sessionId": sessionID})
	if err != nil {
		return false, err
	}
	var u struct {
		Online bool `json:"online"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &u); err != nil {
			return false, fmt.Errorf("%w: bad userOnline data: %v", ErrProtocol, err)
		}
	}
	return u.Online, nil
}

// fetchSessionInfo 跟随 portal 跳转链，解析 portal-main 落地 URL 上的会话参数。
//
// 已知异常：设备存在未完成的认证流程时，redirect.jsp 会 302 到占位 IP
// （如 http://124.124.124.124），依赖 NAS 劫持该请求重新带回 portal 页面；
// NAS 不应答（限速惩罚等）时该请求超时，这里识别此情形给出可读错误。
func (c *Client) fetchSessionInfo(ctx context.Context) (map[string]string, error) {
	res, err := c.hc.Follow(ctx, portalRedirectURL, 10)
	if err != nil {
		if httpkit.IsNetworkError(err) && strings.Contains(err.Error(), "124.124.124.124") {
			return nil, fmt.Errorf("portal redirect chain hit placeholder IP "+
				"124.124.124.124: 设备存在未完成的认证流程且 NAS 未应答劫持请求——"+
				"稍后在浏览器访问任意 HTTP 页面触发认证页，或等流程会话过期后重试: %w", err)
		}
		return nil, err
	}
	if !strings.Contains(res.FinalURL, "portal-main") {
		return nil, fmt.Errorf("%w: portal redirect did not land on portal-main: %s", ErrProtocol, res.FinalURL)
	}
	u, err := url.Parse(res.FinalURL)
	if err != nil {
		return nil, fmt.Errorf("%w: bad portal-main URL: %v", ErrProtocol, err)
	}
	params := make(map[string]string)
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			params[k] = vs[0]
		}
	}
	if params["sessionId"] == "" {
		return nil, fmt.Errorf("%w: portal-main URL missing sessionId: %s", ErrProtocol, res.FinalURL)
	}
	return params, nil
}

// queryStatus 用真实流程 sessionId 查询在线状态。
func (c *Client) queryStatus(ctx context.Context, sessionID string) (*OnlineStatus, error) {
	u := fmt.Sprintf("%s?sessionId=%s&_=%d", getOnlineUserInfoURL,
		url.QueryEscape(sessionID), time.Now().UnixMilli())
	data, err := c.getJSON(ctx, u)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Info map[string]any `json:"portalOnlineUserInfo"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: bad getOnlineUserInfo data: %v", ErrProtocol, err)
	}
	info := payload.Info
	if info == nil {
		return nil, fmt.Errorf("%w: getOnlineUserInfo missing portalOnlineUserInfo", ErrProtocol)
	}
	str := func(k string) string {
		if v, ok := info[k].(string); ok {
			return v
		}
		return ""
	}
	// 离线时 result="fail" 且 redirectUrl 非空；在线时 result="success"
	online := str("result") == "success" || str("userName") != ""
	service := str("realServiceName")
	if service == "" {
		service = str("service")
	}
	return &OnlineStatus{
		Online:   online,
		Username: str("userName"),
		Service:  service,
		UserIP:   str("userIp"),
		UserMAC:  str("userMac"),
		Message:  str("message"),
		Raw:      info,
	}, nil
}

// casSSOLoginURL 构造携带流程会话参数的 cas-sso 登录页 URL。
func (c *Client) casSSOLoginURL(info map[string]string) string {
	q := url.Values{
		"flowSessionId": {info["sessionId"]},
		"customPageId":  {info["customPageId"]},
		"preview":       {"false"},
		"appType":       {"normal"},
		"language":      {"zh-CN"},
		"mode":          {info["mode"]},
		"timer":         {fmt.Sprintf("%d", time.Now().UnixMilli())},
		"nasIp":         {info["nasIp"]},
		"userIp":        {info["userIp"]},
		"ssid":          {info["ssid"]},
	}
	return casSSOLoginURL + "?" + q.Encode()
}

// ── JSON envelope 解包 ────────────────────────────────────────────────

// envelope 是 ePortal 接口的统一包装：{"code":200,"message":...,"data":...}。
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e envelope) ok() bool { return e.Code == http.StatusOK }

func (c *Client) unwrap(resp *http.Response, reqURL string) (json.RawMessage, error) {
	text, err := httpkit.ReadString(resp)
	if err != nil {
		return nil, &httpkit.NetworkError{URL: reqURL, Err: err}
	}
	var env envelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		head := text
		if len(head) > 200 {
			head = head[:200]
		}
		return nil, fmt.Errorf("%w: non-JSON response from %s: %q", ErrProtocol, reqURL, head)
	}
	if !env.ok() {
		return nil, fmt.Errorf("%w: code=%d message=%s url=%s", ErrBusiness, env.Code, env.Message, reqURL)
	}
	return env.Data, nil
}

func (c *Client) getJSON(ctx context.Context, rawURL string) (json.RawMessage, error) {
	resp, err := c.hc.Get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return c.unwrap(resp, rawURL)
}

func (c *Client) postJSON(ctx context.Context, rawURL string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.PostJSON(ctx, rawURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	return c.unwrap(resp, rawURL)
}

// parseQueryParam 从 URL 的 query 中取单个参数。
func parseQueryParam(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
