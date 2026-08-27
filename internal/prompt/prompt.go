// Package prompt 实现终端交互式账密输入。
// Linux 下通过 termios 关闭回显读密码；其他平台降级为明文读入并告警
// （保持零三方依赖，目标平台是 Linux 嵌入式设备）。
package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Credentials 从终端交互读取用户名与密码。stdin 不是终端时报错
// （Daemon/服务场景应使用配置文件而非交互输入）。
func Credentials(stdin *os.File) (username, password string, err error) {
	if !isTerminal(stdin) {
		return "", "", fmt.Errorf("stdin 不是终端，无法交互输入；请在配置文件中提供 username/password，或用 -config 指定")
	}
	r := bufio.NewReader(stdin)

	fmt.Fprint(os.Stderr, "用户名: ")
	line, err := r.ReadString('\n')
	if err != nil {
		return "", "", fmt.Errorf("读取用户名失败: %w", err)
	}
	username = strings.TrimSpace(line)
	if username == "" {
		return "", "", fmt.Errorf("用户名不能为空")
	}

	fmt.Fprint(os.Stderr, "密码: ")
	password, err = readPassword(stdin, r)
	fmt.Fprintln(os.Stderr) // 密码无回显，补一个换行
	if err != nil {
		return "", "", fmt.Errorf("读取密码失败: %w", err)
	}
	if password == "" {
		return "", "", fmt.Errorf("密码不能为空")
	}
	return username, password, nil
}
