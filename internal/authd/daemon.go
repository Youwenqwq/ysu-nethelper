// Package authd 是 Daemon 模式的核心：按「Internet 探针 → portal 状态 →
// 重认证」的判定链自动保持校园网在线，含假死处理与指数退避。
//
// 判定链（每轮）：
//
//	Internet 探针通                    → ONLINE，慢速轮询
//	探针连续失败 N 次（防抖）           → 查 portal 状态
//	  portal 不可达                    → NO_LINK（不在校园网），短间隔重试
//	  portal 说 online（假死）          → 先 offline 再强制重认证
//	  portal 说 offline（真掉线）       → 直接重认证
//
// 重认证 = 确保 CAS TGC 有效（失效则用配置里的账密重新登录并持久化）
// 再走 CAS → ePortal 委托认证。认证成功但探针仍不通视为上游故障，
// 进入长退避，避免 logout/login 死循环。
package authd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"ysunethelper/internal/cas"
	"ysunethelper/internal/config"
	"ysunethelper/internal/eportal"
	"ysunethelper/internal/httpkit"
	"ysunethelper/internal/probe"
)

// State 是 Daemon 的观测状态。
type State string

const (
	StateInit    State = "INIT"
	StateOnline  State = "ONLINE"
	StateOffline State = "OFFLINE"
	StateNoLink  State = "NO_LINK"
	StateBackoff State = "BACKOFF"
)

// Daemon 编排探针、CAS 与 ePortal 客户端。
type Daemon struct {
	cfg    *config.Config
	cas    *cas.Client
	portal *eportal.Client
	prober *probe.Prober
	log    *slog.Logger

	state         State
	backoff       time.Duration
	postAuthFails int
}

// New 构造 Daemon。cfg 必须已 ApplyDefaults。
func New(cfg *config.Config, log *slog.Logger) *Daemon {
	timeout := cfg.HTTPTimeout.D()
	casClient := cas.New(timeout)
	if err := casClient.LoadCredential(cfg.CredentialPath); err != nil {
		log.Warn("加载 CAS 凭据失败，将重新登录", "path", cfg.CredentialPath, "err", err)
	}
	return &Daemon{
		cfg:     cfg,
		cas:     casClient,
		portal:  eportal.New(timeout),
		prober:  probe.New(cfg.Daemon.ProbeURLs, cfg.Daemon.ProbeTimeout.D()),
		log:     log,
		state:   StateInit,
		backoff: cfg.Daemon.BackoffInitial.D(),
	}
}

// NewClients 为 CLI 单发命令构造一套客户端（加载已持久化的 TGC）。
func NewClients(cfg *config.Config) (*cas.Client, *eportal.Client, error) {
	casClient := cas.New(cfg.HTTPTimeout.D())
	if err := casClient.LoadCredential(cfg.CredentialPath); err != nil {
		return nil, nil, fmt.Errorf("加载 CAS 凭据失败: %w", err)
	}
	return casClient, eportal.New(cfg.HTTPTimeout.D()), nil
}

// Authenticate 执行一次完整认证：确保 TGC 有效（失效则用配置里的账密重新登录并持久化）
// 再走 CAS → ePortal 委托认证。CLI 的 login 与 Daemon 的重认证共用此路径。
func Authenticate(ctx context.Context, cfg *config.Config, casClient *cas.Client, portal *eportal.Client) (*eportal.OnlineStatus, error) {
	ok, err := casClient.IsAuthenticated(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		if err := casClient.Login(ctx, cfg.Username, cfg.Password); err != nil {
			return nil, err
		}
		if err := casClient.SaveCredential(cfg.CredentialPath); err != nil {
			// 持久化失败不阻断本次认证，但下次还得重登
			return nil, fmt.Errorf("保存 CAS 凭据失败: %w", err)
		}
	}
	return portal.LoginViaCAS(ctx, casClient, cfg.Service)
}

// Run 运行 Daemon 主循环，直到 ctx 取消。
func (d *Daemon) Run(ctx context.Context) error {
	d.log.Info("daemon started",
		"service", d.cfg.Service,
		"probe_interval", d.cfg.Daemon.ProbeInterval.D().String(),
		"probe_confirm", d.cfg.Daemon.ProbeConfirm,
	)
	for {
		if ctx.Err() != nil {
			d.log.Info("daemon stopped")
			return nil
		}
		d.tick(ctx)
	}
}

// tick 执行一轮判定链，并睡到下一轮。
func (d *Daemon) tick(ctx context.Context) {
	// 1. Internet 探针：通则在线，慢速轮询
	if d.prober.Online(ctx) {
		d.setState(StateOnline)
		d.backoff = d.cfg.Daemon.BackoffInitial.D()
		d.postAuthFails = 0
		d.sleep(ctx, d.cfg.Daemon.ProbeInterval.D())
		return
	}

	// 2. 防抖：连续确认 N 次都失败才动作
	if !d.confirmOffline(ctx) {
		return // 确认期间探针恢复，本轮结束（下一轮重新判定）
	}

	// 3. 查 portal 状态，区分假死/真掉线/不在校园网
	status, err := d.portal.GetStatus(ctx)
	if err != nil {
		if httpkit.IsNetworkError(err) {
			d.setState(StateNoLink)
			d.log.Warn("portal 不可达，可能不在校园网", "err", err)
			d.sleep(ctx, d.cfg.Daemon.NoLinkInterval.D())
			return
		}
		d.log.Error("查询 portal 状态失败", "err", err)
		d.sleepBackoff(ctx)
		return
	}
	d.setState(StateOffline)
	if status.Online {
		// 假死：portal 认为在线但 Internet 不通，先下线再强制重认证
		d.log.Warn("检测到假死：portal 在线但 Internet 不通，强制重认证",
			"username", status.Username, "user_ip", status.UserIP)
		if err := d.portal.Logout(ctx); err != nil {
			d.log.Warn("假死场景下线失败，继续尝试认证", "err", err)
		}
	} else {
		d.log.Info("portal 确认为离线，开始认证", "message", status.Message)
	}

	// 4. 认证
	_, err = Authenticate(ctx, d.cfg, d.cas, d.portal)
	if err != nil {
		d.handleAuthError(ctx, err)
		return
	}
	d.log.Info("认证流程完成，验证 Internet 连通性")

	// 5. 认证后验证：仍不通则是上游故障，长退避防 logout/login 死循环
	if d.prober.Online(ctx) {
		d.setState(StateOnline)
		d.backoff = d.cfg.Daemon.BackoffInitial.D()
		d.postAuthFails = 0
		d.log.Info("Internet 连通，恢复在线")
		d.sleep(ctx, d.cfg.Daemon.ProbeInterval.D())
		return
	}
	d.postAuthFails++
	if d.postAuthFails >= 2 {
		d.log.Error("重认证后 Internet 仍不通，疑似上游故障，进入长退避",
			"backoff", d.cfg.Daemon.BackoffMax.D().String())
		d.postAuthFails = 0
		d.sleep(ctx, d.cfg.Daemon.BackoffMax.D())
		return
	}
	d.log.Warn("认证成功但 Internet 未通，稍后重试")
	d.sleepBackoff(ctx)
}

// confirmOffline 连续确认探针失败；期间恢复则返回 false。
func (d *Daemon) confirmOffline(ctx context.Context) bool {
	confirm := d.cfg.Daemon.ProbeConfirm
	if confirm < 1 {
		confirm = 1
	}
	for i := 1; i < confirm; i++ {
		if !d.sleep(ctx, d.cfg.Daemon.ProbeConfirmGap.D()) {
			return false
		}
		if d.prober.Online(ctx) {
			d.log.Info("确认探测期间 Internet 恢复，取消重认证", "attempt", i)
			return false
		}
	}
	d.log.Warn("Internet 探针连续失败", "count", confirm)
	return true
}

// handleAuthError 按错误类别决定退避策略。
func (d *Daemon) handleAuthError(ctx context.Context, err error) {
	switch {
	case errors.Is(err, cas.ErrLoginFailed):
		d.log.Error("CAS 登录被拒绝：用户名或密码错误（如密码已改请更新配置），进入长退避",
			"err", err, "backoff", d.cfg.Daemon.BackoffMax.D().String())
		d.sleep(ctx, d.cfg.Daemon.BackoffMax.D())
	case errors.Is(err, cas.ErrNeedCaptcha), errors.Is(err, cas.ErrMFARequired):
		d.log.Error("CAS 要求验证码/二次认证（通常因频繁失败触发），请人工登录一次后重启 daemon",
			"err", err, "backoff", d.cfg.Daemon.BackoffMax.D().String())
		d.sleep(ctx, d.cfg.Daemon.BackoffMax.D())
	case errors.Is(err, cas.ErrIPBlocked):
		d.log.Error("IP 被认证网关冻结，进入长退避",
			"backoff", d.cfg.Daemon.BackoffMax.D().String())
		d.sleep(ctx, d.cfg.Daemon.BackoffMax.D())
	case httpkit.IsNetworkError(err):
		d.setState(StateNoLink)
		d.log.Warn("认证期间网络不可达", "err", err)
		d.sleep(ctx, d.cfg.Daemon.NoLinkInterval.D())
	default:
		d.log.Error("认证失败", "err", err, "next_backoff", d.backoff.String())
		d.sleepBackoff(ctx)
	}
}

// setState 记录状态迁移日志。
func (d *Daemon) setState(s State) {
	if d.state != s {
		d.log.Info("state transition", "from", d.state, "to", s)
		d.state = s
	}
}

// sleepBackoff 按当前退避睡眠并加倍（封顶 BackoffMax）。
func (d *Daemon) sleepBackoff(ctx context.Context) {
	d.setState(StateBackoff)
	d.sleep(ctx, d.backoff)
	d.backoff = min(d.backoff*2, d.cfg.Daemon.BackoffMax.D())
}

// sleep 带 ±20% 抖动睡眠；ctx 取消时返回 false。
func (d *Daemon) sleep(ctx context.Context, dur time.Duration) bool {
	jitter := time.Duration((rand.Float64()*0.4 - 0.2) * float64(dur))
	timer := time.NewTimer(dur + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
