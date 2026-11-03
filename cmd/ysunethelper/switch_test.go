package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"ysunethelper/internal/eportal"
)

type menuClient struct {
	calls []string
	fail  bool
}

func (c *menuClient) Services(context.Context) (*eportal.OnlineStatus, []eportal.Service, error) {
	return &eportal.OnlineStatus{Online: true, Service: "中国联通"}, []eportal.Service{
		{Key: "校园网", Value: "campus-value"}, {Key: "中国联通", Value: "中国联通"},
	}, nil
}

func (c *menuClient) SwitchService(_ context.Context, service string) (*eportal.OnlineStatus, error) {
	c.calls = append(c.calls, service)
	if c.fail {
		return nil, errors.New("会话过期")
	}
	return &eportal.OnlineStatus{Online: true, Service: "校园网"}, nil
}

func TestServiceMenu(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		fail        bool
		calls       int
	}{
		{"quit", "q\n", false, 0},
		{"EOF", "", false, 0},
		{"invalid and refresh", "1\n2\ntelecom\nr\nq\n", false, 0},
		{"fixed campus number", "0\nq\n", false, 1},
		{"Chinese and English", "校园网\ncampus\nq\n", false, 2},
		{"persistent error", "0\nq\n", true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &menuClient{fail: tt.fail}
			var out bytes.Buffer
			err := runServiceMenu(context.Background(), client, bufio.NewReader(strings.NewReader(tt.input)), &out)
			if err != nil || len(client.calls) != tt.calls {
				t.Fatalf("calls = %v, error = %v", client.calls, err)
			}
			for _, call := range client.calls {
				if call != "campus-value" {
					t.Fatalf("submitted display name instead of server value: %q", call)
				}
			}
			if !strings.Contains(out.String(), "0. 校园网") || !strings.Contains(out.String(), "3. 中国联通") {
				t.Fatal("menu must use fixed carrier numbers even when some services are absent")
			}
			if strings.Contains(out.String(), "电信") || strings.Contains(out.String(), "移动") {
				t.Fatal("menu must only show services returned for this session")
			}
		})
	}
}

func TestSwitchArgs(t *testing.T) {
	for _, tt := range []struct {
		command string
		args    []string
		service string
		json    bool
		wantErr bool
	}{
		{"switch", nil, "", false, true},
		{"switch", []string{"unicom"}, "unicom", false, false},
		{"switch", []string{"3"}, "3", false, false},
		{"switch", []string{"联通"}, "联通", false, false},
		{"switch", []string{"--json", "campus"}, "campus", true, false},
		{"switch", []string{"--json"}, "", false, true},
		{"switch", []string{" "}, "", false, true},
		{"switch", []string{"campus", "unicom"}, "", false, true},
		{"tui", nil, "", false, false},
		{"tui", []string{"unicom"}, "", false, true},
	} {
		service, jsonOut, err := parseSwitchArgs(tt.command, tt.args)
		if (err != nil) != tt.wantErr || err == nil && (service != tt.service || jsonOut != tt.json) {
			t.Errorf("%s %v = %q, %v, %v", tt.command, tt.args, service, jsonOut, err)
		}
	}
}
