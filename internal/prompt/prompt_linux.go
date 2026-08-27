//go:build linux

package prompt

import (
	"bufio"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// isTerminal 通过 TCGETS ioctl 判断是否为终端。
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
		syscall.TCGETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return errno == 0
}

// readPassword 关闭回显读入一行密码，随后恢复原始终端属性。
func readPassword(f *os.File, r *bufio.Reader) (string, error) {
	var oldState syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
		syscall.TCGETS, uintptr(unsafe.Pointer(&oldState)), 0, 0, 0); errno != 0 {
		return "", errno
	}
	newState := oldState
	newState.Lflag &^= syscall.ECHO
	newState.Lflag |= syscall.ICANON | syscall.ISIG
	newState.Iflag |= syscall.ICRNL
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
		syscall.TCSETS, uintptr(unsafe.Pointer(&newState)), 0, 0, 0); errno != 0 {
		return "", errno
	}
	defer syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
		syscall.TCSETS, uintptr(unsafe.Pointer(&oldState)), 0, 0, 0)

	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
