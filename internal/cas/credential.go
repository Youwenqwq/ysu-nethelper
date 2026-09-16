package cas

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ysunethelper/internal/httpkit"
)

// cookieDomain 是 CAS 网关 cookie 所在域；持久化时按该域过滤。
const cookieDomain = "cer.ysu.edu.cn"

// 路径过滤：仅保留 "/" 或 "/authserver..." 路径下的 cookie，
// 其余业务挂载点（如 /personalInfo）的 cookie 是 per-service 的，不属于 CAS 凭据。
func isCASCookiePath(p string) bool {
	return p == "" || p == "/" || len(p) >= len("/authserver") && p[:len("/authserver")] == "/authserver"
}

func isCASCookie(domain, path string) bool {
	return domain == cookieDomain && isCASCookiePath(path)
}

type credentialFile struct {
	// Username 记录 TGC 所属账号；旧版凭据文件没有该字段（为空），
	// 调用方在账号归属不明时应按各自策略处理（见 ensureCAS/Authenticate）。
	Username string                `json:"username,omitempty"`
	Cookies  []httpkit.CookieEntry `json:"cookies"`
}

// SaveCredential 把 session 中 CAS 网关域的 cookie 落盘（0600）。
func (c *Client) SaveCredential(path string) error {
	entries := c.hc.Cookies.Snapshot(func(domain string) bool {
		return domain == cookieDomain
	})
	// 路径过滤在 Snapshot 后做（Snapshot 只按域过滤）
	filtered := entries[:0]
	for _, e := range entries {
		if isCASCookiePath(e.Path) {
			filtered = append(filtered, e)
		}
	}
	data, err := json.MarshalIndent(credentialFile{Username: c.username, Cookies: filtered}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	// 防御已存在文件的旧权限
	_ = os.Chmod(path, 0o600)
	return nil
}

// LoadCredential 从凭据文件恢复 cookie 到 session。文件不存在返回 nil。
func (c *Client) LoadCredential(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var f credentialFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("invalid credential file %s: %w", path, err)
	}
	for i := range f.Cookies {
		if f.Cookies[i].Domain == "" {
			f.Cookies[i].Domain = cookieDomain
		}
	}
	c.hc.Cookies.Install(f.Cookies)
	c.username = f.Username
	return nil
}

// DropCredential 丢弃 session 中的 CAS 凭据（TGC 及账号归属）。
// 凭据属于其他账号需要换号重登时调用：带着旧 TGC 访问登录页会被
// CAS 直接放行（302 出票），拿不到登录表单，无法换号。
func (c *Client) DropCredential() {
	c.hc.Cookies.Clear(func(domain string) bool { return domain == cookieDomain })
	c.username = ""
}
