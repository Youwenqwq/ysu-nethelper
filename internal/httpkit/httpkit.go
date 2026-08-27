// Package httpkit 提供带完整元数据 cookie 存储、浏览器特征请求头和
// 手动跳转链跟随的 HTTP 客户端，供 CAS 与 ePortal 两个协议客户端共用。
//
// 不用 net/http/cookiejar 的原因：需要完整保留 cookie 的
// domain/path/secure/expires 用于凭据持久化（同名异 path 的 cookie 不能
// 互相覆盖），而 cookiejar 的 Cookies() 只回吐 name/value。
package httpkit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultUserAgent 与浏览器一致；实测 NAS 劫持页对非浏览器特征的请求直接丢包，
// 仅带 User-Agent 不够，Accept / Accept-Language 也必须补齐。
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3"

const (
	defaultAccept = "text/html,application/xhtml+xml,application/xml;q=0.9," +
		"image/avif,image/webp,*/*;q=0.8"
	defaultAcceptLang = "zh-CN,zh;q=0.9"
)

// CookieEntry 是单条 cookie 的可序列化表示，完整保留持久化所需元数据。
type CookieEntry struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Secure  bool   `json:"secure"`
	Expires *int64 `json:"expires"` // epoch 秒；nil 表示会话级
}

func (e *CookieEntry) expired(now time.Time) bool {
	return e.Expires != nil && *e.Expires <= now.Unix()
}

// CookieStore 是带完整元数据的 cookie 存储（RFC 6265 的简化实现，
// 足够覆盖本项目两个固定站点的需求）。
type CookieStore struct {
	mu      sync.Mutex
	cookies map[string]*CookieEntry // key: domain|path|name
}

func NewCookieStore() *CookieStore {
	return &CookieStore{cookies: make(map[string]*CookieEntry)}
}

func cookieKey(domain, path, name string) string {
	return domain + "|" + path + "|" + name
}

// Set 从响应中收割 Set-Cookie，按 RFC 6265 default-path 规则入库。
func (s *CookieStore) Set(reqURL *url.URL, cookies []*http.Cookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	host := strings.ToLower(reqURL.Hostname())
	for _, c := range cookies {
		domain := host
		if c.Domain != "" {
			d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
			// 仅接受当前主机自身或其后缀域
			if host != d && !strings.HasSuffix(host, "."+d) {
				continue
			}
			domain = d
		}
		path := c.Path
		if path == "" || !strings.HasPrefix(path, "/") {
			path = defaultPath(reqURL.Path)
		}
		key := cookieKey(domain, path, c.Name)
		if !c.Expires.IsZero() && time.Now().After(c.Expires) || c.MaxAge < 0 {
			delete(s.cookies, key)
			continue
		}
		entry := &CookieEntry{
			Name:   c.Name,
			Value:  c.Value,
			Domain: domain,
			Path:   path,
			Secure: c.Secure,
		}
		if !c.Expires.IsZero() {
			exp := c.Expires.Unix()
			entry.Expires = &exp
		} else if c.MaxAge > 0 {
			exp := time.Now().Add(time.Duration(c.MaxAge) * time.Second).Unix()
			entry.Expires = &exp
		}
		s.cookies[key] = entry
	}
}

// Get 返回应随请求发出的 Cookie 头值（空串表示无匹配）。
func (s *CookieStore) Get(reqURL *url.URL) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	host := strings.ToLower(reqURL.Hostname())
	path := reqURL.Path
	if path == "" {
		path = "/"
	}
	now := time.Now()
	var pairs []string
	for key, e := range s.cookies {
		if e.expired(now) {
			delete(s.cookies, key)
			continue
		}
		if !domainMatch(host, e.Domain) || !pathMatch(path, e.Path) {
			continue
		}
		if e.Secure && reqURL.Scheme != "https" {
			continue
		}
		pairs = append(pairs, e.Name+"="+e.Value)
	}
	return strings.Join(pairs, "; ")
}

// Snapshot 导出满足 filter 的 cookie 条目（用于持久化）。
func (s *CookieStore) Snapshot(filter func(domain string) bool) []CookieEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []CookieEntry
	for key, e := range s.cookies {
		if e.expired(now) {
			delete(s.cookies, key)
			continue
		}
		if filter == nil || filter(e.Domain) {
			out = append(out, *e)
		}
	}
	return out
}

// Install 把持久化的条目写回存储。
func (s *CookieStore) Install(entries []CookieEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range entries {
		e := entries[i]
		if e.Path == "" {
			e.Path = "/"
		}
		s.cookies[cookieKey(strings.ToLower(e.Domain), e.Path, e.Name)] = &e
	}
}

// defaultPath 实现 RFC 6265 §5.1.4 的 default-path 计算。
func defaultPath(reqPath string) string {
	if reqPath == "" || !strings.HasPrefix(reqPath, "/") ||
		strings.Count(reqPath, "/") == 1 {
		return "/"
	}
	return reqPath[:strings.LastIndex(reqPath, "/")]
}

func domainMatch(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func pathMatch(reqPath, cookiePath string) bool {
	if reqPath == cookiePath {
		return true
	}
	if strings.HasPrefix(reqPath, cookiePath) {
		return strings.HasSuffix(cookiePath, "/") ||
			reqPath[len(cookiePath)] == '/'
	}
	return false
}

// NetworkError 表示传输层失败（对端不可达、超时、被重置等）。
// 协议客户端用它区分「网络不通」与「业务拒绝」。
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("network error for %s: %v", e.URL, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// IsNetworkError 报告错误链中是否存在传输层失败。
func IsNetworkError(err error) bool {
	for err != nil {
		if _, ok := err.(*NetworkError); ok {
			return true
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
			continue
		}
		break
	}
	return false
}

// Client 是不自动跟随重定向、带浏览器特征头和元数据 cookie 存储的
// HTTP 客户端。所有跳转由调用方通过 Follow 显式控制。
type Client struct {
	hc      *http.Client
	Cookies *CookieStore
	Timeout time.Duration
}

func NewClient(timeout time.Duration) *Client {
	return &Client{
		hc: &http.Client{
			// 永不自动跟随：302 语义（POST 转 GET、占位 IP 劫持、JS 跳转）
			// 都需要调用方显式处理
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Cookies: NewCookieStore(),
		Timeout: timeout,
	}
}

// Do 注入 cookie 与浏览器头后发出请求；传输失败包装为 *NetworkError。
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if h := c.Cookies.Get(req.URL); h != "" {
		req.Header.Set("Cookie", h)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", DefaultUserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", defaultAccept)
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", defaultAcceptLang)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &NetworkError{URL: req.URL.String(), Err: err}
	}
	c.Cookies.Set(req.URL, resp.Cookies())
	return resp, nil
}

// Get 发出一次不跟随重定向的 GET。
func (c *Client) Get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// PostForm 发出一次不跟随重定向的表单 POST。
func (c *Client) PostForm(ctx context.Context, rawURL string, form url.Values, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.Do(req)
}

// PostJSON 发出一次不跟随重定向的 JSON POST。
func (c *Client) PostJSON(ctx context.Context, rawURL string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

// ReadString 读取并关闭响应体。
func ReadString(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

var jsRedirectRE = regexp.MustCompile(`location\.href\s*=\s*'([^']+)'`)

// ExtractJSRedirect 从 location.href='...' 形式的 JS 跳转页提取目标 URL。
func ExtractJSRedirect(html string) string {
	m := jsRedirectRE.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	return m[1]
}

// IsRedirect 报告状态码是否为 3xx 重定向。
func IsRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// FollowResult 是一次跳转链跟随的结果。
type FollowResult struct {
	Response *http.Response // 最终的非跳转响应
	Body     string         // 最终响应体
	FinalURL string         // 落地 URL
	Visited  []string       // 依序访问过的 URL（含起点）
}

// Follow 从 rawURL 出发手动跟随 30x 与 JS 跳转，最多 maxHops 跳。
// 返回最终落地响应。跳转链超长视为协议异常。
func (c *Client) Follow(ctx context.Context, rawURL string, maxHops int) (*FollowResult, error) {
	res := &FollowResult{}
	current := rawURL
	for hop := 0; ; hop++ {
		if hop >= maxHops {
			return nil, fmt.Errorf("redirect chain too long (>%d hops, last: %s)", maxHops, current)
		}
		res.Visited = append(res.Visited, current)
		resp, err := c.Get(ctx, current)
		if err != nil {
			return nil, err
		}
		if IsRedirect(resp.StatusCode) {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			if loc == "" {
				return nil, fmt.Errorf("redirect without Location from %s", current)
			}
			next, err := resolveURL(current, loc)
			if err != nil {
				return nil, err
			}
			current = next
			continue
		}
		body, err := ReadString(resp)
		if err != nil {
			return nil, &NetworkError{URL: current, Err: err}
		}
		if target := ExtractJSRedirect(body); target != "" {
			next, err := resolveURL(current, target)
			if err != nil {
				return nil, err
			}
			current = next
			continue
		}
		res.Response = resp
		res.Body = body
		res.FinalURL = current
		return res, nil
	}
}

func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("bad base URL %q: %w", base, err)
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("bad redirect target %q: %w", ref, err)
	}
	return b.ResolveReference(r).String(), nil
}
