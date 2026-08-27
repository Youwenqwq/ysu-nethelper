// ysunethelper：燕山大学校园网助手 CLI。
//
// 用法：
//
//	ysunethelper [-config path] status    查询 portal 在线状态与 Internet 连通性
//	ysunethelper [-config path] login     认证上线（CAS → ePortal；TGC 失效且无
//	                                      配置账密时交互式询问）
//	ysunethelper [-config path] logout    登出下线
//	ysunethelper [-config path] daemon    Daemon 模式：自动保持在线（前台运行，
//	                                      由 systemd/OpenRC 托管）
//
// 配置文件解析顺序：-config 指定 > 当前目录 ./ysunethelper.json >
// ~/.config/ysunethelper/config.json > /etc/ysunethelper/config.json。
// status/logout 无需配置文件；daemon 首次运行且未找到配置时，
// 自动在当前目录生成 ysunethelper.json 模板（0600）后退出。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ysunethelper/internal/authd"
	"ysunethelper/internal/config"
	"ysunethelper/internal/probe"
	"ysunethelper/internal/prompt"
)

func main() {
	fs := flag.NewFlagSet("ysunethelper", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径（默认依次尝试 ./ysunethelper.json、~/.config/ysunethelper/config.json、/etc/ysunethelper/config.json）")
	verbose := fs.Bool("v", false, "debug 级日志")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ysunethelper [-config path] [-v] <status|login|logout|daemon>\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	cmd := fs.Arg(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "status":
		cfg, err := config.LoadOptional(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdStatus(ctx, cfg)
	case "logout":
		cfg, err := config.LoadOptional(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdLogout(ctx, cfg)
	case "login":
		cfg, err := config.LoadOptional(*configPath)
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		cmdLogin(ctx, cfg)
	case "daemon":
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
			fmt.Printf("未找到配置文件，已生成模板 %s（权限 0600）。\n请填写 username/password 后重新运行。\n", target)
			os.Exit(0)
		}
		if err != nil {
			fatal("加载配置失败: %v", err)
		}
		if err := cfg.Validate(); err != nil {
			fatal("%v", err)
		}
		cmdDaemon(ctx, cfg, *verbose)
	default:
		fs.Usage()
		os.Exit(2)
	}
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

func cmdLogin(ctx context.Context, cfg *config.Config) {
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
		u, p, err := prompt.Credentials(os.Stdin)
		if err != nil {
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
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	d := authd.New(cfg, log)
	if err := d.Run(ctx); err != nil {
		fatal("daemon 异常退出: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ysunethelper: "+format+"\n", args...)
	os.Exit(1)
}
