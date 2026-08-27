//go:build !linux

package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// isTerminal 非 Linux 平台的保守实现：仅按字符设备判断。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// readPassword 非 Linux 平台降级为明文读入并告警。
func readPassword(f *os.File, r *bufio.Reader) (string, error) {
	fmt.Fprint(os.Stderr, "(warning: 该平台不支持关闭回显，密码将明文显示) ")
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
