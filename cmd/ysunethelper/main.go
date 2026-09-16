// ysunethelper：燕山大学校园网助手 CLI。
//
// 用法：
//
//	ysunethelper [-config path] status [-v] [-test] [--json]
//	                                      查询 portal 在线状态；-v 显示完整信息，
//	                                      -test 额外检测 Internet 连通性，
//	                                      --json 以 JSON 输出
//	ysunethelper [-config path] login [-u username] [-p password] [-s service] [--json]
//	                                      认证上线（CAS → ePortal；TGC 失效且无
//	                                      配置账密时交互式询问）
//	ysunethelper [-config path] logout [--json]
//	                                      登出下线
//	ysunethelper [-config path] devices [--json]
//	                                      查询账号当前在线设备（自助服务）
//	ysunethelper [-config path] kick [--json] <序号|UUID>...
//	                                      下线指定在线设备（序号见 devices 输出）
//	ysunethelper [-config path] daemon    Daemon 模式：自动保持在线（前台运行，
//	                                      由 systemd/OpenRC 托管）
//	ysunethelper version                  显示版本号
//
// daemon 默认解析：-config 指定 > 当前目录 ./ysunethelper.json >
// ~/.config/ysunethelper/config.json > /etc/ysunethelper/config.json。
// status/login/logout/devices/kick 的隐式解析不读取系统级配置；显式 -config 仍然生效。
// daemon 首次运行且未找到配置时，自动在当前目录生成 ysunethelper.json
// 模板（0600）后退出。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"ysunethelper/internal/authd"
	"ysunethelper/internal/cas"
	"ysunethelper/internal/config"
	"ysunethelper/internal/eportal"
	"ysunethelper/internal/logx"
	"ysunethelper/internal/probe"
	"ysunethelper/internal/prompt"
	"ysunethelper/internal/selfsvc"
)

const configExitCode = 78 // EX_CONFIG

func main() {
	fs := flag.NewFlagSet("ysunethelper", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径（默认路径依命令而异；daemon 包含 /etc/ysunethelper/config.json）")
	verbose := fs.Bool("v", false, "详细输出；daemon 下启用 debug 级日志")
	showVersion := fs.Bool("version", false, "显示版本号并退出")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "燕山大学校园网认证助手")
		fmt.Fprintln(os.Stderr, "\n用法:")
		fmt.Fprintln(os.Stderr, "  ysunethelper [全局选项] <命令> [命令选项]")
		fmt.Fprintln(os.Stderr, "\n命令:")
		fmt.Fprintln(os.Stderr, "  status [-v] [-test] [--json]              查询 Portal 在线状态和账户信息")
		fmt.Fprintln(os.Stderr, "    -v 显示完整账户及 Portal 原始字段；-test 额外执行 Internet 连通性检测")
		fmt.Fprintln(os.Stderr, "  login [-u 用户名] [-p 密码] [-s 运营商] [--json]")
		fmt.Fprintln(os.Stderr, "                                              登录校园网")
		fmt.Fprintln(os.Stderr, "  logout [--json]                             登出当前设备")
		fmt.Fprintln(os.Stderr, "  devices [--json]                            查询账号当前在线设备")
		fmt.Fprintln(os.Stderr, "  kick [--json] <序号|UUID>...                 下线指定在线设备")
		fmt.Fprintln(os.Stderr, "  daemon                                      前台运行在线守护进程")
		fmt.Fprintln(os.Stderr, "  version                                     显示版本号")
		fmt.Fprintln(os.Stderr, "\n全局选项（必须放在命令之前）:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\n使用 ysunethelper <命令> -h 查看命令选项。")
	}
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Printf("ysunethelper %s\n", versionString())
		os.Exit(0)
	}
	args := fs.Args()
	if len(args) < 1 {
		fs.Usage()
		printSystemConfigHint()
		os.Exit(2)
	}
	cmd := args[0]
	cmdArgs := args[1:]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "version":
		ensureNoCommandArgs(cmd, cmdArgs)
		fmt.Printf("ysunethelper %s\n", versionString())
	case "status":
		statusVerbose, testConnectivity, jsonOut := parseStatusArgs(cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdStatus(ctx, cfg, *verbose || statusVerbose, testConnectivity, jsonOut)
	case "logout":
		jsonOut := parseLogoutArgs(cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdLogout(ctx, cfg, jsonOut)
	case "login":
		username, password, service, jsonOut := parseLoginArgs(cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdLogin(ctx, cfg, username, password, service, jsonOut)
	case "devices":
		jsonOut := parseDevicesArgs(cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdDevices(ctx, cfg, jsonOut)
	case "kick":
		targets, jsonOut := parseKickArgs(cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdKick(ctx, cfg, targets, jsonOut)
	case "daemon":
		ensureNoCommandArgs(cmd, cmdArgs)
		cfg, err := config.Load(*configPath)
		if err != nil && config.IsNotExist(err) {
			// daemon 首次运行：在当前目录（或 -config 指定处）生成模板后退出
			target := config.ResolvePath(*configPath)
			if *configPath == "" {
				target = config.CWDConfigFilename
			}
			if werr := config.WriteTemplate(target); werr != nil {
				fatal("生成配置模板失败: %v", werr)
			}
			fmt.Printf("未找到配置文件，已生成模板 %s（权限 0600）。\n请编辑该文件填写 username/password，并确认 service；service 默认为“校园网”。\n完成后重新运行 daemon。\n", target)
			os.Exit(0)
		}
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		if err := cfg.Validate(); err != nil {
			incompleteConfig(cfg.Path(), err)
		}
		cmdDaemon(ctx, cfg, *verbose)
	default:
		fs.Usage()
		os.Exit(2)
	}
}

// parseStatusArgs 解析 status 的显示、输出格式和连通性检测选项。
func parseStatusArgs(args []string) (verbose, testConnectivity, jsonOut bool) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&verbose, "v", false, "显示完整 portal 和账户信息")
	fs.BoolVar(&verbose, "verbose", false, "显示完整 portal 和账户信息")
	fs.BoolVar(&testConnectivity, "test", false, "额外检测 Internet 连通性")
	fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: ysunethelper status [-v] [-test] [--json]")
		fmt.Fprintln(os.Stderr, "\n查询当前设备的 Portal 在线状态和账户信息。")
		fmt.Fprintln(os.Stderr, "默认只显示姓名、学号、IP、MAC（如有）、运营商和校园网剩余流量；不执行 Internet 连通性检测。")
		fmt.Fprintln(os.Stderr, "\n选项:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	return verbose, testConnectivity, jsonOut
}

// parseLoginArgs 解析 login 子命令的参数。它们仅影响本次登录，不会改写配置文件。
func parseLoginArgs(args []string) (username, password, service string, jsonOut bool) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&username, "u", "", "统一身份认证用户名")
	fs.StringVar(&username, "username", "", "统一身份认证用户名")
	fs.StringVar(&password, "p", "", "统一身份认证密码")
	fs.StringVar(&password, "password", "", "统一身份认证密码")
	fs.StringVar(&service, "s", "", "网络服务名（campus/unicom/telecom/mobile 或服务全名）")
	fs.StringVar(&service, "service", "", "网络服务名（campus/unicom/telecom/mobile 或服务全名）")
	fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出登录结果")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: ysunethelper login [-u 用户名] [-p 密码] [-s 运营商] [--json]")
		fmt.Fprintln(os.Stderr, "\n通过统一身份认证登录校园网。本次传入的参数不会写入配置文件。")
		fmt.Fprintln(os.Stderr, "\n选项:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	return username, password, service, jsonOut
}

// parseLogoutArgs 解析 logout 的输出格式选项，并校验不带位置参数。
func parseLogoutArgs(args []string) (jsonOut bool) {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出登出结果")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: ysunethelper logout [--json]")
		fmt.Fprintln(os.Stderr, "\n登出当前设备；设备已经离线时不执行额外操作。")
		fmt.Fprintln(os.Stderr, "\n选项:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	return jsonOut
}

// parseDevicesArgs 解析 devices 的输出格式选项，并校验不带位置参数。
func parseDevicesArgs(args []string) (jsonOut bool) {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出接口原始数据")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: ysunethelper devices [--json]")
		fmt.Fprintln(os.Stderr, "\n通过自助服务查询账号当前在线设备。--json 输出接口原始数据。")
		fmt.Fprintln(os.Stderr, "\n选项:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	return jsonOut
}

// parseKickArgs 解析 kick 的目标列表（序号或 onlineUserUuid）和输出格式选项。
func parseKickArgs(args []string) (targets []string, jsonOut bool) {
	fs := flag.NewFlagSet("kick", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出下线结果")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: ysunethelper kick [--json] <序号|UUID>...")
		fmt.Fprintln(os.Stderr, "\n下线账号的指定在线设备。目标为 devices 输出中的序号（1 起）或 UUID。")
		fmt.Fprintln(os.Stderr, "\n选项:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	return fs.Args(), jsonOut
}

func ensureNoCommandArgs(cmd string, args []string) {
	if len(args) == 0 {
		return
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		if cmd == "daemon" {
			fmt.Fprintln(os.Stderr, "用法: ysunethelper [-v] daemon")
			fmt.Fprintln(os.Stderr, "\n前台运行在线守护进程；使用命令前的全局 -v 输出 debug 级日志。")
		}
		if cmd == "version" {
			fmt.Fprintln(os.Stderr, "用法: ysunethelper version")
			fmt.Fprintln(os.Stderr, "\n显示版本号。")
		}
		os.Exit(0)
	}
	fatal("%s 不接受额外参数: %v", cmd, args)
}

func cmdStatus(ctx context.Context, cfg *config.Config, verbose, testConnectivity, jsonOut bool) {
	_, portalClient, err := authd.NewClients(cfg)
	if err != nil {
		fatal("%v", err)
	}
	st, err := portalClient.GetStatus(ctx)
	if err != nil {
		fatal("查询 portal 状态失败: %v", err)
	}
	if jsonOut {
		var value any = struct {
			Online           bool   `json:"online"`
			Name             string `json:"name,omitempty"`
			Username         string `json:"username,omitempty"`
			UserIP           string `json:"user_ip,omitempty"`
			UserMAC          string `json:"user_mac,omitempty"`
			Service          string `json:"service,omitempty"`
			RemainingTraffic string `json:"remaining_traffic,omitempty"`
		}{
			Online:           st.Online,
			Name:             st.Name,
			Username:         st.Username,
			UserIP:           st.UserIP,
			UserMAC:          st.UserMAC,
			Service:          st.Service,
			RemainingTraffic: st.RemainingTraffic,
		}
		if verbose {
			value = st
		}
		out, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(out))
	} else {
		printStatusHuman(st, verbose)
	}

	if !testConnectivity {
		return
	}
	p := probe.New(cfg.Daemon.ProbeURLs, cfg.Daemon.ProbeTimeout.D())
	for _, r := range p.Check(ctx) {
		mark := "FAIL"
		if r.OK {
			mark = "OK  "
		}
		fmt.Printf("internet probe %s %s (%s)\n", mark, r.URL, r.Detail)
	}
	if st.Online {
		fmt.Println("status: ONLINE (portal)")
	} else {
		fmt.Println("status: OFFLINE (portal)")
	}
}

// printStatusHuman 以人类友好的逐行格式输出在线状态；
// verbose 时追加服务端消息、账户信息和 Portal 原始字段。
func printStatusHuman(st *eportal.OnlineStatus, verbose bool) {
	state := "离线"
	if st.Online {
		state = "在线"
	}
	fmt.Printf("在线状态: %s\n", state)
	if st.Name != "" {
		fmt.Printf("姓名: %s\n", st.Name)
	}
	if st.Username != "" {
		fmt.Printf("学工号: %s\n", st.Username)
	}
	if st.UserIP != "" {
		fmt.Printf("IP: %s\n", st.UserIP)
	}
	if st.UserMAC != "" {
		fmt.Printf("MAC: %s\n", st.UserMAC)
	}
	if st.Service != "" {
		fmt.Printf("运营商: %s\n", st.Service)
	}
	if st.RemainingTraffic != "" {
		fmt.Printf("剩余流量: %s\n", st.RemainingTraffic)
	}
	if !verbose {
		return
	}
	if st.Message != "" {
		fmt.Printf("服务端消息: %s\n", st.Message)
	}
	if len(st.Account) > 0 {
		out, _ := json.MarshalIndent(st.Account, "", "  ")
		fmt.Printf("账户信息: %s\n", out)
	}
	if len(st.Raw) > 0 {
		out, _ := json.MarshalIndent(st.Raw, "", "  ")
		fmt.Printf("认证门户原始字段: %s\n", out)
	}
}

func cmdLogin(ctx context.Context, cfg *config.Config, username, password, service string, jsonOut bool) {
	if username != "" {
		cfg.Username = username
	}
	if password != "" {
		cfg.Password = password
	}
	if service != "" {
		cfg.Service = service
	}
	casClient, portalClient, err := authd.NewClients(cfg)
	if err != nil {
		fatal("%v", err)
	}
	if err := ensureCAS(ctx, cfg, casClient); err != nil {
		fatal("CAS 登录失败: %v", err)
	}
	st, err := portalClient.LoginViaCAS(ctx, casClient, cfg.Service)
	if err != nil {
		fatal("认证失败: %v", err)
	}
	if jsonOut {
		out, _ := json.MarshalIndent(struct {
			OK       bool   `json:"ok"`
			Username string `json:"username,omitempty"`
			Name     string `json:"name,omitempty"`
			Service  string `json:"service,omitempty"`
			UserIP   string `json:"user_ip,omitempty"`
			UserMAC  string `json:"user_mac,omitempty"`
		}{
			OK:       true,
			Username: st.Username,
			Name:     st.Name,
			Service:  st.Service,
			UserIP:   st.UserIP,
			UserMAC:  st.UserMAC,
		}, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Printf("登录成功。用户: %s / 服务: %s / IP: %s\n", st.Username, st.Service, st.UserIP)
}

// ensureCAS 保证 casClient 持有属于 cfg.Username 的有效 TGC：
// 失效或归属其他账号时优先用配置账密重新登录并持久化；账密缺失则交互式询问。
func ensureCAS(ctx context.Context, cfg *config.Config, casClient *cas.Client) error {
	ok, err := casClient.IsAuthenticated(ctx)
	if err != nil {
		return fmt.Errorf("CAS 网关不可达: %w", err)
	}
	// TGC 有效但属于另一个账号（切换过配置/-u 的典型情形），或凭据文件
	// 是旧版、没有记录账号归属：继续使用会以旧账号身份完成 portal 准入，
	// 运营商绑定校验全部打在旧账号上，报出误导性的「未绑定运营商」。
	// 两种情况都丢弃凭据重新登录（旧版文件只发生一次，重登后即带归属）。
	if ok && cfg.Username != "" && casClient.Username() != cfg.Username {
		if owner := casClient.Username(); owner != "" {
			fmt.Fprintf(os.Stderr, "ysunethelper: 缓存的 CAS 凭据属于 %s，与当前账号 %s 不符，将以当前账号重新登录\n",
				owner, cfg.Username)
		} else {
			fmt.Fprintln(os.Stderr, "ysunethelper: 缓存的 CAS 凭据缺少账号归属信息（旧版凭据文件），将以当前账号重新登录一次")
		}
		casClient.DropCredential()
		ok = false
	}
	if ok {
		return nil
	}
	if cfg.Username == "" || cfg.Password == "" {
		u, p, err := prompt.Credentials(ctx, os.Stdin)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "ysunethelper: 已取消")
				os.Exit(130)
			}
			return err
		}
		cfg.Username, cfg.Password = u, p
	}
	if err := casClient.Login(ctx, cfg.Username, cfg.Password); err != nil {
		return err
	}
	if err := casClient.SaveCredential(cfg.CredentialPath); err != nil {
		// 持久化失败不阻断本次使用，但下次还得重登
		return fmt.Errorf("保存 CAS 凭据失败: %w", err)
	}
	return nil
}

// newSelfsvcClient 建立已认证的自助服务客户端。
func newSelfsvcClient(ctx context.Context, cfg *config.Config) (*selfsvc.Client, error) {
	casClient, _, err := authd.NewClients(cfg)
	if err != nil {
		return nil, err
	}
	if err := ensureCAS(ctx, cfg, casClient); err != nil {
		return nil, fmt.Errorf("CAS 登录失败: %w", err)
	}
	sc := selfsvc.New(cfg.HTTPTimeout.D())
	if err := sc.Login(ctx, casClient); err != nil {
		return nil, err
	}
	return sc, nil
}

func cmdDevices(ctx context.Context, cfg *config.Config, jsonOut bool) {
	sc, err := newSelfsvcClient(ctx, cfg)
	if err != nil {
		fatal("自助服务认证失败: %v", err)
	}
	list, err := sc.ListDevices(ctx)
	if err != nil {
		fatal("查询在线设备失败: %v", err)
	}
	if jsonOut {
		out, _ := json.MarshalIndent(list, "", "  ")
		fmt.Println(string(out))
		return
	}
	if len(list.Online) == 0 {
		fmt.Println("当前没有在线设备")
		return
	}
	fmt.Printf("在线设备 %d 台:\n", len(list.Online))
	for i, d := range list.Online {
		var b strings.Builder
		fmt.Fprintf(&b, "  [%d] %s", i+1, orEmpty(d.Name, "(未命名)"))
		if d.Current {
			b.WriteString("（当前设备）")
		}
		if d.IP != "" {
			fmt.Fprintf(&b, "  ip=%s", d.IP)
		}
		if d.MAC != "" {
			fmt.Fprintf(&b, "  mac=%s", d.MAC)
		}
		if d.Type != "" {
			fmt.Fprintf(&b, "  type=%s", d.Type)
		}
		if d.OnlineDuration != "" {
			fmt.Fprintf(&b, "  在线时长=%s", d.OnlineDuration)
		}
		fmt.Println(b.String())
		fmt.Printf("      uuid=%s\n", d.UUID)
	}
}

func cmdKick(ctx context.Context, cfg *config.Config, targets []string, jsonOut bool) {
	sc, err := newSelfsvcClient(ctx, cfg)
	if err != nil {
		fatal("自助服务认证失败: %v", err)
	}
	list, err := sc.ListDevices(ctx)
	if err != nil {
		fatal("查询在线设备失败: %v", err)
	}
	byUUID := make(map[string]selfsvc.Device, len(list.Online))
	for _, d := range list.Online {
		byUUID[d.UUID] = d
	}
	var kicked []selfsvc.Device
	var uuids []string
	var descs []string
	for _, target := range targets {
		d, err := resolveKickTarget(target, list.Online, byUUID)
		if err != nil {
			fatal("%v", err)
		}
		kicked = append(kicked, d)
		uuids = append(uuids, d.UUID)
		descs = append(descs, fmt.Sprintf("%s(%s)", orEmpty(d.Name, "未命名"), d.UUID))
	}
	if err := sc.KickOffline(ctx, uuids); err != nil {
		fatal("下线失败: %v", err)
	}
	if jsonOut {
		out, _ := json.MarshalIndent(struct {
			OK     bool             `json:"ok"`
			Kicked []selfsvc.Device `json:"kicked"`
		}{OK: true, Kicked: kicked}, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Printf("kick ok: %s\n", strings.Join(descs, ", "))
}

// resolveKickTarget 把 kick 参数解析为设备：32 位十六进制按 UUID，
// 纯数字按 devices 列表序号（1 起）。
func resolveKickTarget(target string, online []selfsvc.Device, byUUID map[string]selfsvc.Device) (selfsvc.Device, error) {
	if isUUID(target) {
		d, ok := byUUID[target]
		if !ok {
			return selfsvc.Device{}, fmt.Errorf("UUID %s 不在在线设备列表中", target)
		}
		return d, nil
	}
	if n, err := strconv.Atoi(target); err == nil {
		if n < 1 || n > len(online) {
			return selfsvc.Device{}, fmt.Errorf("序号 %d 超出范围（当前在线 %d 台）", n, len(online))
		}
		return online[n-1], nil
	}
	return selfsvc.Device{}, fmt.Errorf("无法识别的目标 %q：请用 devices 输出中的序号或 UUID", target)
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func isUUID(s string) bool { return uuidRE.MatchString(s) }

func orEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func cmdLogout(ctx context.Context, cfg *config.Config, jsonOut bool) {
	_, portalClient, err := authd.NewClients(cfg)
	if err != nil {
		fatal("%v", err)
	}
	if err := portalClient.Logout(ctx); err != nil {
		fatal("登出失败: %v", err)
	}
	if jsonOut {
		out, _ := json.MarshalIndent(struct {
			OK bool `json:"ok"`
		}{OK: true}, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Println("下线成功")
}

func cmdDaemon(ctx context.Context, cfg *config.Config, verbose bool) {
	level := logx.LevelInfo
	if verbose {
		level = logx.LevelDebug
	}
	log := logx.New(os.Stdout, level)
	d := authd.New(cfg, log)
	if err := d.Run(ctx); err != nil {
		if errors.Is(err, authd.ErrHardAuthFailure) {
			// 与配置不完整共用 EX_CONFIG(78)：systemd 单元的
			// RestartPreventExitStatus=78 与 OpenRC（无 respawn）
			// 都不会自动重启，等人工修正配置后手动拉起。
			fmt.Fprintf(os.Stderr, "ysunethelper: %v\n", err)
			os.Exit(configExitCode)
		}
		fatal("daemon 异常退出: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ysunethelper: "+format+"\n", args...)
	os.Exit(1)
}

func incompleteConfig(path string, err error) {
	printIncompleteConfigHint(path, err)
	os.Exit(configExitCode)
}

func printSystemConfigHint() {
	if _, err := os.Stat(config.SystemPath); err != nil {
		return
	}
	cfg, err := config.Load(config.SystemPath)
	if err == nil {
		if validationErr := cfg.Validate(); validationErr != nil {
			printIncompleteConfigHint(cfg.Path(), validationErr)
		}
		return
	}
	if errors.Is(err, fs.ErrPermission) {
		fmt.Fprintf(os.Stderr, "检测到系统配置文件 %s，但当前用户无权读取。\n请使用 sudoedit %s 检查配置，完成后再启动 daemon。\n",
			config.SystemPath, config.SystemPath)
	}
}

func printIncompleteConfigHint(path string, err error) {
	fmt.Fprintf(os.Stderr, "ysunethelper: 配置文件 %s 尚未完成配置：%v\n", path, err)
	fmt.Fprintln(os.Stderr, "请编辑该文件，填写 username/password，并确认 service；service 默认为“校园网”。")
	fmt.Fprintln(os.Stderr, "配置完成后再启动 daemon，例如：sudo systemctl enable --now ysunethelper")
}
