// ysunethelper：燕山大学校园网助手 CLI。
//
// 用法：
//
//	ysunethelper [-config path] status    查询 portal 在线状态与 Internet 连通性
//	ysunethelper [-config path] login [-u username] [-p password] [-s service]
//	                                      认证上线（CAS → ePortal；TGC 失效且无
//	                                      配置账密时交互式询问）
//	ysunethelper [-config path] logout    登出下线
//	ysunethelper [-config path] daemon    Daemon 模式：自动保持在线（前台运行，
//	                                      由 systemd/OpenRC 托管）
//
// daemon 默认解析：-config 指定 > 当前目录 ./ysunethelper.json >
// ~/.config/ysunethelper/config.json > /etc/ysunethelper/config.json。
// status/login/logout 的隐式解析不读取系统级配置；显式 -config 仍然生效。
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
	"syscall"

	"ysunethelper/internal/authd"
	"ysunethelper/internal/config"
	"ysunethelper/internal/logx"
	"ysunethelper/internal/probe"
	"ysunethelper/internal/prompt"
)

const configExitCode = 78 // EX_CONFIG

func main() {
	fs := flag.NewFlagSet("ysunethelper", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径（默认路径依命令而异；daemon 包含 /etc/ysunethelper/config.json）")
	verbose := fs.Bool("v", false, "debug 级日志")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ysunethelper [-config path] [-v] <status|login|logout|daemon>\n\n")
		fmt.Fprintln(os.Stderr, "login: ysunethelper login [-u username] [-p password] [-s service]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])
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
	case "status":
		ensureNoCommandArgs(cmd, cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdStatus(ctx, cfg)
	case "logout":
		ensureNoCommandArgs(cmd, cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdLogout(ctx, cfg)
	case "login":
		username, password, service := parseLoginArgs(cmdArgs)
		cfg, err := config.LoadOptionalCLI(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdLogin(ctx, cfg, username, password, service)
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

// parseLoginArgs 解析 login 子命令的参数。它们仅影响本次登录，不会改写配置文件。
func parseLoginArgs(args []string) (username, password, service string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&username, "u", "", "统一身份认证用户名")
	fs.StringVar(&username, "username", "", "统一身份认证用户名")
	fs.StringVar(&password, "p", "", "统一身份认证密码")
	fs.StringVar(&password, "password", "", "统一身份认证密码")
	fs.StringVar(&service, "s", "", "网络服务名（campus/unicom/telecom/mobile 或服务全名）")
	fs.StringVar(&service, "service", "", "网络服务名（campus/unicom/telecom/mobile 或服务全名）")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ysunethelper login [-u username] [-p password] [-s service]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	return username, password, service
}

func ensureNoCommandArgs(cmd string, args []string) {
	if len(args) == 0 {
		return
	}
	fatal("%s 不接受额外参数: %v", cmd, args)
}

func cmdStatus(ctx context.Context, cfg *config.Config) {
	_, portalClient, err := authd.NewClients(cfg)
	if err != nil {
		fatal("%v", err)
	}
	st, err := portalClient.GetStatus(ctx)
	if err != nil {
		fatal("查询 portal 状态失败: %v", err)
	}
	out, _ := json.MarshalIndent(st, "", "  ")
	fmt.Println(string(out))

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

func cmdLogin(ctx context.Context, cfg *config.Config, username, password, service string) {
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
	// TGC 有效时无需账密；失效时优先用配置账密，缺失则交互式询问
	ok, err := casClient.IsAuthenticated(ctx)
	if err != nil {
		fatal("CAS 网关不可达: %v", err)
	}
	if !ok && (cfg.Username == "" || cfg.Password == "") {
		u, p, err := prompt.Credentials(ctx, os.Stdin)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "ysunethelper: 已取消")
				os.Exit(130)
			}
			fatal("%v", err)
		}
		cfg.Username, cfg.Password = u, p
	}
	st, err := authd.Authenticate(ctx, cfg, casClient, portalClient)
	if err != nil {
		fatal("认证失败: %v", err)
	}
	fmt.Printf("login ok: user=%s service=%s ip=%s\n", st.Username, st.Service, st.UserIP)
}

func cmdLogout(ctx context.Context, cfg *config.Config) {
	_, portalClient, err := authd.NewClients(cfg)
	if err != nil {
		fatal("%v", err)
	}
	if err := portalClient.Logout(ctx); err != nil {
		fatal("登出失败: %v", err)
	}
	fmt.Println("logout ok")
}

func cmdDaemon(ctx context.Context, cfg *config.Config, verbose bool) {
	level := logx.LevelInfo
	if verbose {
		level = logx.LevelDebug
	}
	log := logx.New(os.Stdout, level)
	d := authd.New(cfg, log)
	if err := d.Run(ctx); err != nil {
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
	fmt.Fprintf(os.Stderr, "请编辑该文件（例如 sudoedit %s），填写 username/password，并确认 service；service 默认为“校园网”。\n", path)
	fmt.Fprintln(os.Stderr, "配置完成后再启动 daemon，例如：sudo systemctl enable --now ysunethelper")
}
