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
	Cookies []httpkit.CookieEntry `json:"cookies"`
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
	data, err := json.MarshalIndent(credentialFile{Cookies: filtered}, "", "  ")
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
	return nil
}
