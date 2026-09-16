package authd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"ysunethelper/internal/cas"
	"ysunethelper/internal/config"
	"ysunethelper/internal/eportal"
	"ysunethelper/internal/logx"
	"ysunethelper/internal/probe"
)

// 用内存连接提供真实 HTTP 探针响应，所有等待均处于 synctest 的虚拟时钟内。
func observedProber(t *testing.T, requests chan<- time.Time, status string) *probe.Prober {
	t.Helper()
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				t.Error(err)
				return
			}
			req.Body.Close()
			requests <- time.Now()
			io.WriteString(server, "HTTP/1.1 "+status+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		}()
		return client, nil
	}
	http.DefaultTransport = transport
	p := probe.New([]string{"http://probe.test/generate_204"}, time.Second)
	http.DefaultTransport = original
	return p
}

// observedPortal 返回所有请求都落到内存假服务器的 portal 客户端。
// 响应对 portal 协议必然非法——只覆盖「INIT 首轮直查 portal 失败后
// 退避」的路径，不模拟任何成功的 portal 交互。
func observedPortal(t *testing.T, status string) *eportal.Client {
	t.Helper()
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			req.Body.Close()
			io.WriteString(server, "HTTP/1.1 "+status+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		}()
		return client, nil
	}
	http.DefaultTransport = transport
	c := eportal.New(time.Second)
	http.DefaultTransport = original
	return c
}

func TestDaemonNoAuthPeriod(t *testing.T) {
	for _, tt := range []struct {
		name            string
		initiallyPaused bool
		status          string
	}{
		{"start inside restricted period", true, "204 No Content"},
		{"enter during long polling interval", false, "204 No Content"},
		{"enter during offline confirmation", false, "200 OK"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				start := now.Add(time.Minute)
				end := now.Add(2 * time.Minute)
				if tt.initiallyPaused {
					start = now.Add(-time.Minute)
				}
				zone := time.FixedZone("Beijing", 8*60*60)
				cfg := &config.Config{Username: "user", Password: "password"}
				cfg.ApplyDefaults()
				cfg.Daemon.ProbeInterval = config.Duration(10 * time.Minute)
				cfg.Daemon.ProbeConfirmGap = config.Duration(10 * time.Minute)
				cfg.Daemon.NoAuthPeriod = config.NoAuthPeriod{
					Enabled: true, Weekdays: []int{int(start.In(zone).Weekday())},
					Start: start.In(zone).Format("15:04"), End: end.In(zone).Format("15:04"),
				}
				if err := cfg.Validate(); err != nil {
					t.Fatal(err)
				}
				requests := make(chan time.Time, 8)
				d := &Daemon{cfg: cfg, log: logx.New(io.Discard, logx.LevelInfo),
					prober: observedProber(t, requests, tt.status), portal: observedPortal(t, tt.status)}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				go func() {
					defer close(done)
					if err := d.Run(ctx); err != nil {
						t.Error(err)
					}
				}()
				synctest.Wait()
				if !tt.initiallyPaused {
					select {
					case at := <-requests:
						if !at.Equal(now) {
							t.Fatalf("initial probe at %s, want %s", at, now)
						}
					default:
						t.Fatal("no initial probe outside restricted period")
					}
				}
				time.Sleep(end.Sub(now) - time.Nanosecond)
				synctest.Wait()
				select {
				case at := <-requests:
					t.Fatalf("unexpected probe during restricted period at %s", at)
				default:
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				select {
				case at := <-requests:
					if !at.Equal(end) {
						t.Fatalf("resumed at %s, want %s", at, end)
					}
				default:
					t.Fatal("did not resume probing at end of restricted period")
				}
				cancel()
				<-done
			})
		})
	}
}

func TestDaemonNoAuthPeriodCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := &config.Config{}
		cfg.ApplyDefaults()
		cfg.Daemon.NoAuthPeriod = config.NoAuthPeriod{
			Enabled: true, Weekdays: []int{6}, Start: "07:00", End: "09:00",
		}
		d := &Daemon{cfg: cfg, log: logx.New(io.Discard, logx.LevelInfo)}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.Run(ctx)
		}()
		synctest.Wait()
		before := time.Now()
		cancel()
		<-done
		if !time.Now().Equal(before) {
			t.Fatal("cancellation waited for restricted period to end")
		}
	})
}

// 凭据类硬失败必须使 daemon 退出（ErrHardAuthFailure），不再退避重试。
func TestHandleAuthErrorHardFailure(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	d := &Daemon{cfg: cfg, log: logx.New(io.Discard, logx.LevelInfo)}
	for _, err := range []error{
		fmt.Errorf("%w: 您提供的用户名或者密码有误", cas.ErrLoginFailed),
		cas.ErrNeedCaptcha,
		cas.ErrMFARequired,
	} {
		got := d.handleAuthError(context.Background(), err)
		if !errors.Is(got, ErrHardAuthFailure) {
			t.Fatalf("%v: got %v, want ErrHardAuthFailure", err, got)
		}
		if !errors.Is(got, err) {
			t.Fatalf("%v: wrapped sentinel lost in %v", err, got)
		}
	}
}

// CAS 账密提交步骤的未识别失败累计到阈值触发熔断；其他步骤的失败不触发。
func TestHandleAuthErrorLoginFuse(t *testing.T) {
	newDaemon := func() *Daemon {
		cfg := &config.Config{}
		cfg.ApplyDefaults()
		cfg.Daemon.BackoffInitial = config.Duration(time.Millisecond)
		return &Daemon{cfg: cfg, log: logx.New(io.Discard, logx.LevelInfo), backoff: time.Millisecond}
	}
	loginErr := func() error { return &loginError{fmt.Errorf("%w: odd", cas.ErrProtocol)} }

	d := newDaemon()
	for i := 1; i < maxConsecutiveLoginFails; i++ {
		if err := d.handleAuthError(context.Background(), loginErr()); err != nil {
			t.Fatalf("attempt %d: got hard failure %v, want backoff", i, err)
		}
	}
	if err := d.handleAuthError(context.Background(), loginErr()); !errors.Is(err, ErrHardAuthFailure) {
		t.Fatalf("fuse did not trip at %d attempts, got %v", maxConsecutiveLoginFails, err)
	}

	d2 := newDaemon()
	for i := 0; i < maxConsecutiveLoginFails+2; i++ {
		if err := d2.handleAuthError(context.Background(),
			fmt.Errorf("%w: odd", eportal.ErrProtocol)); err != nil {
			t.Fatalf("non-login-step failure wrongly tripped fuse: %v", err)
		}
	}
}
