// Package probe 实现 captive-portal 式 Internet 连通性探测。
//
// 判据必须是「HTTP 204 且无重定向」：未认证时校园网会把任意 HTTP 请求
// 劫持到认证页（200/302），只检查「能连上」会把劫持误认为已通。
package probe

import (
	"context"
	"net/http"
	"time"
)

// DefaultURLs 是校园网环境实测过的 generate_204 探针：离线时 NAS 丢包超时、在线时 204。
// 注意 wifi.vivo.com.cn 在 YSU 认证前是白名单（离线也通），不能用作探针。
var DefaultURLs = []string{
	"http://connect.rom.miui.com/generate_204",
	"http://www.gstatic.com/generate_204",
	"http://connectivitycheck.gstatic.com/generate_204",
}

// Prober 对一组 204 探针做连通性检查。
type Prober struct {
	urls []string
	hc   *http.Client
}

func New(urls []string, timeout time.Duration) *Prober {
	if len(urls) == 0 {
		urls = DefaultURLs
	}
	return &Prober{
		urls: urls,
		hc: &http.Client{
			Timeout: timeout,
			// 不跟随重定向：302 即劫持，判失败
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// URLResult 是单个探针的结果。
type URLResult struct {
	URL    string
	OK     bool
	Detail string // 状态码或错误摘要
}

// Check 逐个探测，返回每个探针的结果。任一 204 即整体在线。
func (p *Prober) Check(ctx context.Context) []URLResult {
	results := make([]URLResult, 0, len(p.urls))
	for _, u := range p.urls {
		results = append(results, p.checkOne(ctx, u))
	}
	return results
}

// Online 任一探针返回 204 即为在线。
func (p *Prober) Online(ctx context.Context) bool {
	for _, r := range p.Check(ctx) {
		if r.OK {
			return true
		}
	}
	return false
}

func (p *Prober) checkOne(ctx context.Context, rawURL string) URLResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return URLResult{URL: rawURL, Detail: err.Error()}
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return URLResult{URL: rawURL, Detail: err.Error()}
	}
	resp.Body.Close()
	ok := resp.StatusCode == http.StatusNoContent
	detail := resp.Status
	if ok {
		detail = "204"
	}
	return URLResult{URL: rawURL, OK: ok, Detail: detail}
}
