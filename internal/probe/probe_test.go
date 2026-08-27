package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProber(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	hijack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟 NAS 劫持：返回 200 认证页
		w.Write([]byte("<html>auth</html>"))
	}))
	defer hijack.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", ok.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()

	p := New([]string{hijack.URL, redirect.URL}, 2*time.Second)
	if p.Online(context.Background()) {
		t.Error("hijack/redirect responses must not count as online")
	}
	p2 := New([]string{hijack.URL, ok.URL}, 2*time.Second)
	if !p2.Online(context.Background()) {
		t.Error("a single 204 should count as online")
	}
}
