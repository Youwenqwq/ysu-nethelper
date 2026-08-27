// Package cas 实现燕山大学统一身份认证（cer.ysu.edu.cn，金智 CAS）客户端。
// 只保留 Daemon 需要的路径：账密登录（一次）、TGC 有效性检测、
// service ticket 签发、TGC 凭据持久化。验证码与 MFA 不做自动处理，
// 撞到即报硬错误（校园网环境下正常账号不会触发）。
package cas

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ysunethelper/internal/httpkit"
)

const (
	baseURL     = "https://cer.ysu.edu.cn"
	loginURL    = baseURL + "/authserver/login"
	indexURL    = baseURL + "/authserver/index.do"
	referrerURL = baseURL + "/authserver/login"

	// 登录时给 CAS 的 stub service：用 cer 自身页面，避免拉进任何业务系统
	defaultLoginService = baseURL + "/personalInfo/personCenter/index.html"
)

var ticketRE = regexp.MustCompile(`[?&]ticket=([^&]+)`)

// Client 是 CAS 网关客户端。
type Client struct {
	hc *httpkit.Client
}

// New 构造客户端；timeout 作用于每次 HTTP 请求。
func New(timeout time.Duration) *Client {
	return &Client{hc: httpkit.NewClient(timeout)}
}

// IsAuthenticated 判断当前 session 是否仍持有有效 TGC。
// GET /authserver/index.do：302 到登录页 → false；200 或 302 到他处 → true。
// 网关不可达返回网络错误，不会把传输失败误报为未认证。
func (c *Client) IsAuthenticated(ctx context.Context) (bool, error) {
	resp, err := c.hc.Get(ctx, indexURL)
	if err != nil {
		return false, fmt.Errorf("CAS gateway unreachable: %w", err)
	}
	resp.Body.Close()
	if httpkit.IsRedirect(resp.StatusCode) {
		loc := resp.Header.Get("Location")
		return !strings.Contains(loc, "/authserver/login"), nil
	}
	return resp.StatusCode == http.StatusOK, nil
}

// Login 完成一次账密登录（第一重）。成功后 TGC 落在 session 上，
// 调用方随后可 SaveCredential 持久化。
func (c *Client) Login(ctx context.Context, username, password string) error {
	page, err := c.fetchLoginPage(ctx)
	if err != nil {
		return err
	}
	fields := ExtractHiddenFields(page, "userNameLogin")
	execution := fields["execution"]
	salt := fields["pwdEncryptSalt"]
	if execution == "" {
		return fmt.Errorf("%w: login page missing 'execution' field", ErrProtocol)
	}
	if salt == "" {
		return fmt.Errorf("%w: login page missing 'pwdEncryptSalt' field", ErrProtocol)
	}
	encrypted, err := EncryptPassword(password, salt, rand.Reader)
	if err != nil {
		return err
	}

	encodedService := url.QueryEscape(defaultLoginService)
	postURL := fmt.Sprintf("%s?service=%s&_=%d", loginURL, encodedService, time.Now().UnixMilli())
	form := url.Values{
		"username":  {username},
		"password":  {encrypted},
		"captcha":   {""},
		"_eventId":  {"submit"},
		"cllt":      {"userNameLogin"},
		"dllt":      {"generalLogin"},
		"lt":        {""},
		"execution": {execution},
	}
	resp, err := c.hc.PostForm(ctx, postURL, form, map[string]string{
		"Origin":  baseURL,
		"Referer": loginURL + "?service=" + encodedService,
	})
	if err != nil {
		return fmt.Errorf("CAS login submit failed: %w", err)
	}
	return c.classifyStep1Response(ctx, resp)
}

// GetServiceTicket 用 session 上的 TGC 为 serviceURL 签发一张 ST。
// TGC 失效（302 回登录页）时报 ErrNotAuthenticated，由调用方重新 Login。
func (c *Client) GetServiceTicket(ctx context.Context, serviceURL string) (string, error) {
	u := loginURL + "?service=" + url.QueryEscape(serviceURL)
	resp, err := c.hc.Get(ctx, u)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if !httpkit.IsRedirect(resp.StatusCode) {
		return "", fmt.Errorf("%w: expected redirect from CAS, got status %d", ErrProtocol, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "/authserver/login") {
		return "", ErrNotAuthenticated
	}
	m := ticketRE.FindStringSubmatch(loc)
	if m == nil {
		return "", fmt.Errorf("%w: no ST ticket in Location header: %q", ErrProtocol, loc)
	}
	ticket, err := url.QueryUnescape(m[1])
	if err != nil {
		return "", fmt.Errorf("%w: bad ticket encoding: %v", ErrProtocol, err)
	}
	return ticket, nil
}

// fetchLoginPage 拉取登录页 HTML（带 stub service 参数）。
func (c *Client) fetchLoginPage(ctx context.Context) (string, error) {
	u := loginURL + "?service=" + url.QueryEscape(defaultLoginService)
	res, err := c.hc.Follow(ctx, u, 10)
	if err != nil {
		return "", err
	}
	return res.Body, nil
}

// classifyStep1Response 把第一重提交的响应翻译成结构化结果。
func (c *Client) classifyStep1Response(ctx context.Context, resp *http.Response) error {
	if httpkit.IsRedirect(resp.StatusCode) {
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if strings.Contains(loc, "reAuthCheck") || strings.Contains(loc, "isMultifactor") {
			return ErrMFARequired
		}
		if strings.Contains(loc, defaultLoginService) || strings.Contains(loc, "ticket=") {
			// 跟随一次完成 service 端落地（TGC 已种下，落地失败不影响凭据）
			followCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, _ = c.hc.Follow(followCtx, loc, 10)
			cancel()
			return nil
		}
		// 其他重定向：跟随后再判断
		res, err := c.hc.Follow(ctx, loc, 10)
		if err != nil {
			return fmt.Errorf("%w: failed to follow redirect: %v", ErrProtocol, err)
		}
		if strings.Contains(res.FinalURL, defaultLoginService) || strings.Contains(res.FinalURL, "ticket=") {
			return nil
		}
		if IsReauthPage(res.Body) {
			return ErrMFARequired
		}
		return fmt.Errorf("%w: unrecognized redirect chain after first-factor: %s", ErrProtocol, res.FinalURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: unexpected status code from CAS: %d", ErrProtocol, resp.StatusCode)
	}
	text, err := httpkit.ReadString(resp)
	if err != nil {
		return err
	}
	switch {
	case IsIPFrozen(text):
		return ErrIPBlocked
	case IsReauthPage(text):
		return ErrMFARequired
	}
	if msg := ExtractErrorMessage(text); msg != "" {
		// 服务端常用同一 element 提示「需要验证码」与「用户名密码错」，按关键词区分
		if strings.Contains(msg, "验证码") || strings.Contains(strings.ToLower(msg), "captcha") {
			return fmt.Errorf("%w: %s", ErrNeedCaptcha, msg)
		}
		return fmt.Errorf("%w: %s", ErrLoginFailed, msg)
	}
	return fmt.Errorf("%w: first-factor failed (no error message extracted)", ErrLoginFailed)
}
