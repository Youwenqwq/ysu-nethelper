package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"ysunethelper/internal/config"
	"ysunethelper/internal/eportal"
	"ysunethelper/internal/prompt"
)

func parseSwitchArgs(command string, args []string) (string, bool, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var jsonOut bool
	if command == "switch" {
		fs.BoolVar(&jsonOut, "json", false, "以 JSON 输出切换结果（需要指定运营商）")
	}
	fs.Usage = func() {
		if command == "tui" {
			fmt.Fprintln(fs.Output(), "用法: ysunethelper tui")
		} else {
			fmt.Fprintln(fs.Output(), "用法: ysunethelper switch [--json] <编号|中文名称|英文别名>")
		}
		fmt.Fprintln(fs.Output(), "\n0=校园网(campus)，1=电信(telecom)，2=移动(mobile)，3=联通(unicom)。")
		fmt.Fprintln(fs.Output(), "使用当前在线会话切换，不需要密码；交互菜单请运行 tui。")
		fmt.Fprintln(fs.Output(), "仅切换当前连接，不修改配置文件或 daemon 的重连服务。")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return "", false, err
	}
	if fs.NArg() > 1 || command == "tui" && fs.NArg() != 0 {
		return "", false, fmt.Errorf("%s 参数不正确；使用 %s -h 查看帮助", command, command)
	}
	service := ""
	if fs.NArg() == 1 {
		service = strings.TrimSpace(fs.Arg(0))
		if service == "" {
			return "", false, fmt.Errorf("运营商不能为空")
		}
	}
	if command == "switch" && service == "" {
		fs.Usage()
		return "", false, fmt.Errorf("switch 需要指定运营商，例如 switch 3、switch 联通或 switch unicom")
	}
	return service, jsonOut, nil
}

func cmdSwitch(ctx context.Context, cfg *config.Config, service string, jsonOut, tui bool) error {
	// 切换只需要门户在线会话；不创建 CAS 客户端或读取缓存凭据。
	client := eportal.New(cfg.HTTPTimeout.D())
	if tui {
		reader, err := prompt.TerminalReader(os.Stdin)
		if err != nil {
			return err
		}
		return runServiceMenu(ctx, client, reader, os.Stdout)
	}
	status, err := client.SwitchService(ctx, service)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(struct {
			OK       bool   `json:"ok"`
			Online   bool   `json:"online"`
			Username string `json:"username,omitempty"`
			Service  string `json:"service"`
			UserIP   string `json:"user_ip,omitempty"`
		}{true, status.Online, status.Username, status.Service, status.UserIP})
	}
	fmt.Fprintf(os.Stdout, "当前运营商: %s / IP: %s\n", status.Service, status.UserIP)
	return nil
}

type serviceClient interface {
	Services(context.Context) (*eportal.OnlineStatus, []eportal.Service, error)
	SwitchService(context.Context, string) (*eportal.OnlineStatus, error)
}

// runServiceMenu 使用逐行输入，兼容 SSH、串口和无 ANSI 支持的终端。
func runServiceMenu(ctx context.Context, client serviceClient, reader *bufio.Reader, out io.Writer) error {
	fmt.Fprintln(out, "YSU-NetHelper · 运营商切换")
	fmt.Fprintln(out, "使用当前在线会话，无需输入密码。切换可能短暂断网。")
	fmt.Fprintln(out, "本次选择不写入配置；daemon 掉线重连仍使用配置中的运营商。")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintln(out, "\n正在读取当前连接和可选运营商…")
		status, services, err := client.Services(ctx)
		if err != nil {
			fmt.Fprintf(out, "读取失败: %v\n", err)
			services = nil
		} else {
			fmt.Fprintf(out, "当前运营商: %s / IP: %s\n", status.Service, status.UserIP)
			for _, service := range services {
				mark := ""
				if eportal.IsCurrentService(status, service.Value) || eportal.IsCurrentService(status, service.Key) {
					mark = "（当前）"
				}
				if number, ok := eportal.ServiceNumber(service.Key); ok {
					fmt.Fprintf(out, "  %d. %s%s\n", number, service.Key, mark)
				} else if number, ok := eportal.ServiceNumber(service.Value); ok {
					fmt.Fprintf(out, "  %d. %s%s\n", number, service.Key, mark)
				} else {
					fmt.Fprintf(out, "  %s%s\n", service.Key, mark)
				}
			}
		}
		for {
			fmt.Fprint(out, "输入编号、名称或英文别名切换，r 刷新，q 退出: ")
			line, err := prompt.ReadLine(ctx, reader)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			choice := strings.ToLower(strings.TrimSpace(line))
			if choice == "q" {
				return nil
			}
			if choice == "r" {
				break
			}
			resolved := eportal.ResolveSwitchService(strings.TrimSpace(line))
			var target *eportal.Service
			for i := range services {
				if resolved == services[i].Key || resolved == services[i].Value {
					target = &services[i]
					break
				}
			}
			if target == nil {
				fmt.Fprintln(out, "请输入列表中的运营商、r 或 q。")
				continue
			}
			fmt.Fprintf(out, "正在切换到 %s…\n", target.Key)
			result, err := client.SwitchService(ctx, target.Value)
			if err != nil {
				fmt.Fprintf(out, "切换失败: %v\n", err)
			} else {
				fmt.Fprintf(out, "当前运营商: %s / IP: %s\n", result.Service, result.UserIP)
			}
			break
		}
	}
}
