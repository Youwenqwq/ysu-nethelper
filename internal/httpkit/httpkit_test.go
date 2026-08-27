package httpkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCookieStorePathScoping(t *testing.T) {
	s := NewCookieStore()
	base := mustURL(t, "https://example.com/eportal/index.jsp")

	// 无 Path 属性 → default-path 为请求路径的目录部分（/eportal）
	s.Set(base, []*http.Cookie{{Name: "JSESSIONID", Value: "a"}})
	if got := s.Get(mustURL(t, "https://example.com/eportal/network/userOnline")); got != "JSESSIONID=a" {
		t.Errorf("same-dir request should carry cookie, got %q", got)
	}
	if got := s.Get(mustURL(t, "https://example.com/cas-sso/login")); got != "" {
		t.Errorf("different path should not carry cookie, got %q", got)
	}

	// 显式 Path=/ 全站可见
	s.Set(base, []*http.Cookie{{Name: "SESSION", Value: "b", Path: "/"}})
	got := s.Get(mustURL(t, "https://example.com/cas-sso/login"))
	if got != "SESSION=b" {
		t.Errorf("root-path cookie should be sent site-wide, got %q", got)
	}
}

func TestCookieStoreExpiryAndSecure(t *testing.T) {
	s := NewCookieStore()
	u := mustURL(t, "https://example.com/")
	past := time.Now().Add(-time.Hour)
	s.Set(u, []*http.Cookie{{Name: "dead", Value: "x", Path: "/", Expires: past}})
	if got := s.Get(u); got != "" {
		t.Errorf("expired cookie should be dropped, got %q", got)
	}
	s.Set(u, []*http.Cookie{{Name: "sec", Value: "y", Path: "/", Secure: true}})
	if got := s.Get(mustURL(t, "http://example.com/")); got != "" {
		t.Errorf("secure cookie should not be sent over http, got %q", got)
	}
}

func TestCookieSnapshotInstallRoundTrip(t *testing.T) {
	s := NewCookieStore()
	u := mustURL(t, "https://cer.ysu.edu.cn/authserver/login")
	exp := time.Now().Add(time.Hour).Unix()
	s.Set(u, []*http.Cookie{{Name: "TGC", Value: "v", Path: "/authserver", Secure: true, Expires: time.Unix(exp, 0)}})

	entries := s.Snapshot(nil)
	if len(entries) != 1 || entries[0].Path != "/authserver" || entries[0].Expires == nil {
		t.Fatalf("snapshot = %+v", entries)
	}
	s2 := NewCookieStore()
	s2.Install(entries)
	if got := s2.Get(mustURL(t, "https://cer.ysu.edu.cn/authserver/index.do")); got != "TGC=v" {
		t.Errorf("round-trip lost cookie, got %q", got)
	}
	if got := s2.Get(mustURL(t, "https://cer.ysu.edu.cn/personalInfo/")); got != "" {
		t.Errorf("path scoping lost after round-trip, got %q", got)
	}
}

func TestFollowJSRedirect(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", "/mid")
			w.WriteHeader(http.StatusFound)
		case "/mid":
			w.Write([]byte(`<script>location.href='/end?ok=1'</script>`))
		case "/end":
			w.Write([]byte("final"))
		}
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	res, err := c.Follow(context.Background(), srv.URL+"/start", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalURL != srv.URL+"/end?ok=1" || res.Body != "final" {
		t.Errorf("final = %q body = %q", res.FinalURL, res.Body)
	}
	if len(res.Visited) != 3 {
		t.Errorf("visited = %v", res.Visited)
	}
}

func TestIsNetworkError(t *testing.T) {
	c := NewClient(50 * time.Millisecond)
	// 127.0.0.1:1 必定连接拒绝
	_, err := c.Get(context.Background(), "http://127.0.0.1:1/")
	if err == nil || !IsNetworkError(err) {
		t.Fatalf("expected NetworkError, got %v", err)
	}
}
