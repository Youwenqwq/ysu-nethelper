// Package selfsvc 实现 auth1 自助服务系统（/self）的免浏览器访问：
// 查询账号在线设备、下线指定设备。
//
// 自助服务前端是 OAuth2 客户端（/self → /oauth2/authorization/custom-self
// → cas-sso/oauth2.0/authorize → cas-sso/login?service=…）。认证沿用与
// eportal 相同的 CAS 委托认证（client_name=sidadapter，TGC 出票回跳），
// 票根消费后 cas-sso 会话已认证，重新进入 /self 入口即可走完
// authorize → callbackAuthorize → code 交换，拿到自助服务会话。
//
// 关键坑：同一主机上 / 与 /cas-sso/ 两个应用各自下发同名 SESSION cookie，
// 请求必须按 path 从长到短排列（httpkit 已处理），否则跨请求会话丢失，
// 回调链会在 authorize/login 之间死循环。
package selfsvc

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

	// 任一 /self 下受保护页面都会触发同样的 OAuth2 跳转链
	entryURL = baseURL + "/self/my-devices?i18n_lang=zh"
	// 「统一身份认证」外部提供者入口（CAS 委托认证，与 eportal 同一 client）
	clientRedirectURL = baseURL + "/cas-sso/clientredirect?client_name=sidadapter"

	devicesURL = baseURL + "/sam/api/userself/devices"
	kickURL    = baseURL + "/sam/api/userself/devices/kick-offline/batch"
)

// Client 是自助服务客户端。
type Client struct {
	hc *httpkit.Client
}

// New 构造客户端；timeout 作用于每次 HTTP 请求。
func New(timeout time.Duration) *Client {
	return &Client{hc: httpkit.NewClient(timeout)}
}

// Login 通过 CAS 委托认证建立自助服务会话。casClient 必须已持有有效 TGC；
// TGC 失效报 cas.ErrNotAuthenticated，由调用方先行重新 CAS 登录。
func (c *Client) Login(ctx context.Context, casClient *cas.Client) error {
	// 进入受保护页，跟随 OAuth2 跳转链到 cas-sso 登录页
	// （service 参数在此绑定进 cas-sso 会话）
	res, err := c.hc.Follow(ctx, entryURL, 10)
	if err != nil {
		return err
	}
	if strings.Contains(res.FinalURL, "/self/") {
		return nil // 已有会话（复用 Client 时）
	}
	if !strings.Contains(res.FinalURL, "/cas-sso/login") {
		return fmt.Errorf("%w: self-service entry did not land on cas-sso login: %s", ErrProtocol, res.FinalURL)
	}

	// clientredirect → CAS 登录 URL（service 参数即回跳地址）
	resp, err := c.hc.Get(ctx, clientRedirectURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	if !strings.Contains(location, "/authserver/login") {
		return fmt.Errorf("%w: clientredirect did not point to CAS login: %q", ErrProtocol, location)
	}
	serviceURL := parseQueryParam(location, "service")
	if serviceURL == "" {
		return fmt.Errorf("%w: CAS login URL missing service param: %q", ErrProtocol, location)
	}

	// 用 CAS 侧的 TGC 出票；票根消费后 cas-sso 会话即已认证，
	// 跟随跳转链走完 authorize → callbackAuthorize → code 交换
	ticket, err := casClient.GetServiceTicket(ctx, serviceURL)
	if err != nil {
		return err
	}
	sep := "?"
	if strings.Contains(serviceURL, "?") {
		sep = "&"
	}
	res, err = c.hc.Follow(ctx, serviceURL+sep+"ticket="+url.QueryEscape(ticket), 15)
	if err != nil {
		return err
	}
	if !strings.Contains(res.FinalURL, "/self/") {
		// 票根未被接受时回落到 cas-sso 登录页
		return fmt.Errorf("%w: 委托认证未完成，回跳后未进入自助服务（落在 %s）", ErrAuth, res.FinalURL)
	}
	return nil
}

// Device 是一台在线设备。
type Device struct {
	UUID           string `json:"uuid"` // onlineUserUuid，下线凭据
	Name           string `json:"name"` // 设备名（用户可改）
	IP             string `json:"ip,omitempty"`
	MAC            string `json:"mac,omitempty"`
	Type           string `json:"type,omitempty"`      // 设备类型（如"电脑"）
	AuthType       string `json:"auth_type,omitempty"` // 接入方式
	OnlineDuration string `json:"online_duration,omitempty"`
	Current        bool   `json:"current"` // 是否当前设备
}

// DeviceList 是设备列表查询结果。
type DeviceList struct {
	Online []Device `json:"online"`
	// Raw 为接口原始 data（含 offlineDevices 等），供详细输出
	Raw map[string]any `json:"raw,omitempty"`
}

// ListDevices 查询账号的在线设备。须先 Login。
func (c *Client) ListDevices(ctx context.Context) (*DeviceList, error) {
	data, err := c.postJSON(ctx, devicesURL, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		OnlineDevices []struct {
			OnlineUserUUID string `json:"onlineUserUuid"`
			DeviceName     string `json:"deviceName"`
			UserIPv4       string `json:"userIpv4"`
			UserMAC        string `json:"userMac"`
			DeviceType     string `json:"deviceType"`
			AuthType       string `json:"authType"`
			OnlineDuration string `json:"onlineDuration"`
			CurrentDevice  bool   `json:"currentDevice"`
		} `json:"onlineDevices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: bad devices data: %v", ErrProtocol, err)
	}
	list := &DeviceList{}
	for _, d := range payload.OnlineDevices {
		list.Online = append(list.Online, Device{
			UUID:           d.OnlineUserUUID,
			Name:           d.DeviceName,
			IP:             d.UserIPv4,
			MAC:            d.UserMAC,
			Type:           d.DeviceType,
			AuthType:       d.AuthType,
			OnlineDuration: d.OnlineDuration,
			Current:        d.CurrentDevice,
		})
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		list.Raw = raw
	}
	return list, nil
}

// KickOffline 按 onlineUserUuid 下线设备（可一次多台）。
// 下线不存在的 UUID 时服务端以非 200 code 报错。
func (c *Client) KickOffline(ctx context.Context, uuids []string) error {
	if len(uuids) == 0 {
		return fmt.Errorf("%w: empty device list", ErrProtocol)
	}
	_, err := c.postJSON(ctx, kickURL, map[string][]string{"onlineUserUuids": uuids})
	return err
}

// ── JSON envelope 解包 ────────────────────────────────────────────────

// envelope 是自助服务接口的统一包装：{"code":200,"message":"OK","data":...,"success":true}。
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Success bool            `json:"success"`
}

func (e envelope) ok() bool { return e.Code == http.StatusOK && e.Success }

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

func (c *Client) postJSON(ctx context.Context, rawURL string, payload any) (json.RawMessage, error) {
	var body string
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = string(b)
	} else {
		body = "null"
	}
	resp, err := c.hc.PostJSON(ctx, rawURL, strings.NewReader(body))
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
