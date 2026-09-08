package eportal

import "testing"

func TestAccountInfoContent(t *testing.T) {
	account := map[string]any{
		"accountInfo": []any{
			map[string]any{"title": "套餐&余额", "content": "0加油包"},
			map[string]any{"title": "剩余流量", "content": "28.1GB"},
		},
	}
	if got := accountInfoContent(account, "剩余流量"); got != "28.1GB" {
		t.Fatalf("accountInfoContent() = %q, want %q", got, "28.1GB")
	}
}
