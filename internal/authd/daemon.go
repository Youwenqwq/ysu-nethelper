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
//	禁认证时段内暂停整条判定链，不主动登出；结束后重新探测。
//
// 重认证 = 确保 CAS TGC 有效（失效则用配置里的账密重新登录并持久化）
// 再走 CAS → ePortal 委托认证。认证成功但探针仍不通视为上游故障，
// 进入长退避，避免 logout/login 死循环。
//
// 凭据类硬失败（密码错误、强制验证码、二次认证）不再退避重试：用同一套
// 错误凭据反复提交只会累积服务端失败计数，把账号刷进验证码/临时冻结。
// 此类错误使 Run 返回 ErrHardAuthFailure，由 main 以 EX_CONFIG 退出，
// 等人工修正配置后重启。CAS 账密提交步骤的连续失败另有熔断兜底，
// 防止服务端变更响应结构导致凭据拒绝被误分类后无限重试。
package authd

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"ysunethelper/internal/cas"
	"ysunethelper/internal/config"
	"ysunethelper/internal/eportal"
	"ysunethelper/internal/httpkit"
	"ysunethelper/internal/logx"
	"ysunethelper/internal/probe"
)

// ErrHardAuthFailure 表示凭据类不可恢复失败： daemon 应退出等人工干预，
// 而不是用同一套凭据重试（会累积服务端失败计数，触发账号风控）。
// main 将其映射为 EX_CONFIG(78) 退出码，systemd/OpenRC 均不会自动重启。
var ErrHardAuthFailure = errors.New("authd: unrecoverable credential failure")

// maxConsecutiveLoginFails 是 CAS 账密提交步骤的连续失败熔断阈值。
// 兜底场景：服务端变更响应结构导致凭据拒绝被误分类（如 401 曾被当作
// 协议错误），达到阈值即按硬失败处理，避免无限刷服务端失败计数。
const maxConsecutiveLoginFails = 3

// loginError 标记失败发生在 CAS 账密提交步骤（区别于其后的 ePortal 准入）。
// 只有这一步的失败会消耗服务端的账号失败计数，熔断只对它计数。
type loginError struct{ err error }

func (e *loginError) Error() string { return "CAS 登录失败: " + e.err.Error() }
func (e *loginError) Unwrap() error { return e.err }

// State 是 Daemon 的观测状态。
type State string

const (
	StateInit       State = "INIT"
	StateOnline     State = "ONLINE"
	StateOffline    State = "OFFLINE"
	StateNoLink     State = "NO_LINK"
	StateBackoff    State = "BACKOFF"
	StateAuthPaused State = "AUTH_PAUSED"
)

// Daemon 编排探针、CAS 与 ePortal 客户端。
type Daemon struct {
	cfg    *config.Config
	cas    *cas.Client
	portal *eportal.Client
	prober *probe.Prober
	log    *logx.Logger

	state         State
	backoff       time.Duration
	postAuthFails int
	loginFails    int // 连续 CAS 账密提交失败计数（熔断用，认证成功时清零）
}

// New 构造 Daemon。cfg 必须已 ApplyDefaults。
func New(cfg *config.Config, log *logx.Logger) *Daemon {
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

// Authenticate 执行一次完整认证：确保 TGC 有效且属于配置账号（失效或归属
// 其他账号时用配置里的账密重新登录并持久化）再走 CAS → ePortal 委托认证。
// Daemon 的重认证走此路径；CLI 单发命令用 main 包的 ensureCAS。
func Authenticate(ctx context.Context, cfg *config.Config, casClient *cas.Client, portal *eportal.Client) (*eportal.OnlineStatus, error) {
	ok, err := casClient.IsAuthenticated(ctx)
	if err != nil {
		return nil, err
	}
	// 归属不明（旧版凭据文件）或归属其他账号时同样重登，理由见 ensureCAS。
	if ok && cfg.Username != "" && casClient.Username() != cfg.Username {
		casClient.DropCredential()
		ok = false
	}
	if !ok {
		if err := casClient.Login(ctx, cfg.Username, cfg.Password); err != nil {
			return nil, &loginError{err}
		}
		if err := casClient.SaveCredential(cfg.CredentialPath); err != nil {
			// 持久化失败不阻断本次认证，但下次还得重登
			return nil, fmt.Errorf("保存 CAS 凭据失败: %w", err)
		}
	}
	return portal.LoginViaCAS(ctx, casClient, cfg.Service)
}

// Run 运行 Daemon 主循环，直到 ctx 取消或发生凭据类硬失败（ErrHardAuthFailure）。
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
		if err := d.tick(ctx); err != nil {
			return err
		}
	}
}

// tick 执行一轮判定链，并睡到下一轮。
// 返回非 nil 错误表示不可恢复的硬失败，Run 应退出。
func (d *Daemon) tick(ctx context.Context) error {
	if d.pauseAuthentication(ctx) {
		return nil
	}

	// 1. Internet 探针：通则在线，慢速轮询
	if d.prober.Online(ctx) {
		d.setState(StateOnline)
		d.backoff = d.cfg.Daemon.BackoffInitial.D()
		d.postAuthFails = 0
		d.sleep(ctx, d.cfg.Daemon.ProbeInterval.D())
		return nil
	}

	// 2. 防抖：连续确认 N 次都失败才动作
	if !d.confirmOffline(ctx) {
		return nil // 确认期间探针恢复，本轮结束（下一轮重新判定）
	}
	if d.pauseAuthentication(ctx) {
		return nil
	}

	// 3. 查 portal 状态，区分假死/真掉线/不在校园网
	status, err := d.portal.GetStatus(ctx)
	if err != nil {
		if httpkit.IsNetworkError(err) {
			d.setState(StateNoLink)
			d.log.Warn("portal 不可达，可能不在校园网", "err", err)
			d.sleep(ctx, d.cfg.Daemon.NoLinkInterval.D())
			return nil
		}
		d.log.Error("查询 portal 状态失败", "err", err)
		d.sleepBackoff(ctx)
		return nil
	}
	// 查询可能跨过时段边界，必须在主动下线前再次检查。
	if d.pauseAuthentication(ctx) {
		return nil
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
	if d.pauseAuthentication(ctx) {
		return nil
	}
	_, err = Authenticate(ctx, d.cfg, d.cas, d.portal)
	if err != nil {
		return d.handleAuthError(ctx, err)
	}
	d.loginFails = 0
	d.log.Info("认证流程完成，验证 Internet 连通性")

	// 5. 认证后验证：仍不通则是上游故障，长退避防 logout/login 死循环
	if d.pauseAuthentication(ctx) {
		return nil
	}
	if d.prober.Online(ctx) {
		d.setState(StateOnline)
		d.backoff = d.cfg.Daemon.BackoffInitial.D()
		d.postAuthFails = 0
		d.log.Info("Internet 连通，恢复在线")
		d.sleep(ctx, d.cfg.Daemon.ProbeInterval.D())
		return nil
	}
	d.postAuthFails++
	if d.postAuthFails >= 2 {
		d.log.Error("重认证后 Internet 仍不通，疑似上游故障，进入长退避",
			"backoff", d.cfg.Daemon.BackoffMax.D().String())
		d.postAuthFails = 0
		d.sleep(ctx, d.cfg.Daemon.BackoffMax.D())
		return nil
	}
	d.log.Warn("认证成功但 Internet 未通，稍后重试")
	d.sleepBackoff(ctx)
	return nil
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
		if d.pauseAuthentication(ctx) {
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
// 返回非 nil 错误（ErrHardAuthFailure）表示凭据类硬失败，daemon 应退出
// 等人工修正配置：用同一套错误凭据退避重试只会累积服务端失败计数，
// 把账号刷进强制验证码/临时冻结。
func (d *Daemon) handleAuthError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, cas.ErrLoginFailed):
		d.log.Error("CAS 拒绝登录：用户名或密码错误，停止自动重试。请修正配置中的账密后重启 daemon",
			"err", err)
		return fmt.Errorf("%w: %w", ErrHardAuthFailure, err)
	case errors.Is(err, cas.ErrNeedCaptcha), errors.Is(err, cas.ErrMFARequired):
		d.log.Error("CAS 要求验证码/二次认证（通常因多次失败触发风控），无法无人值守处理，停止自动重试。"+
			"请人工登录一次解除风控、确认配置账密后重启 daemon", "err", err)
		return fmt.Errorf("%w: %w", ErrHardAuthFailure, err)
	case errors.Is(err, cas.ErrIPBlocked):
		d.log.Error("IP 被认证网关冻结，进入长退避",
			"backoff", d.cfg.Daemon.BackoffMax.D().String())
		d.sleep(ctx, d.cfg.Daemon.BackoffMax.D())
	case httpkit.IsNetworkError(err):
		d.setState(StateNoLink)
		d.log.Warn("认证期间网络不可达", "err", err)
		d.sleep(ctx, d.cfg.Daemon.NoLinkInterval.D())
	default:
		// 未识别的失败：若发生在 CAS 账密提交步骤则计入熔断——可能是
		// 服务端变更导致的凭据拒绝误分类，刷满阈值即按硬失败退出。
		var le *loginError
		if errors.As(err, &le) {
			d.loginFails++
			if d.loginFails >= maxConsecutiveLoginFails {
				d.log.Error("CAS 登录连续失败且原因无法识别（疑似凭据被拒），触发熔断停止重试。"+
					"请检查配置账密后重启 daemon", "count", d.loginFails, "err", err)
				return fmt.Errorf("%w: %w", ErrHardAuthFailure, err)
			}
		}
		d.log.Error("认证失败", "err", err, "next_backoff", d.backoff.String())
		d.sleepBackoff(ctx)
	}
	return nil
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
	d.backoff = minDuration(d.backoff*2, d.cfg.Daemon.BackoffMax.D())
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// pauseAuthentication 在禁认证时段等待，不给固定结束时间添加抖动。
// 已经发出的请求不撤回；每个后续判定步骤开始前会重新检查。
func (d *Daemon) pauseAuthentication(ctx context.Context) bool {
	now := time.Now()
	start, end := d.cfg.Daemon.NoAuthPeriod.NextWindow(now)
	if start.IsZero() || now.Before(start) {
		if d.state == StateAuthPaused {
			d.log.Info("禁认证时段结束，恢复自动检测")
			d.setState(StateInit)
		}
		return false
	}
	if d.state != StateAuthPaused {
		d.setState(StateAuthPaused)
		d.log.Info("禁认证时段，暂停自动探测、登出和认证", "until", end.Format(time.RFC3339))
	}
	d.backoff = d.cfg.Daemon.BackoffInitial.D()
	d.postAuthFails = 0
	wait(ctx, end.Sub(now))
	return true
}

// sleep 带 ±20% 抖动睡眠；ctx 取消时返回 false。
func (d *Daemon) sleep(ctx context.Context, dur time.Duration) bool {
	jitter := time.Duration((rand.Float64()*0.4 - 0.2) * float64(dur))
	delay := dur + jitter
	now := time.Now()
	start, _ := d.cfg.Daemon.NoAuthPeriod.NextWindow(now)
	if !start.IsZero() {
		// 不让退避或探测间隔跨过禁认证时段；下一轮会按固定结束时间等待。
		delay = minDuration(delay, max(time.Duration(0), start.Sub(now)))
	}
	return wait(ctx, delay)
}

func wait(ctx context.Context, dur time.Duration) bool {
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
