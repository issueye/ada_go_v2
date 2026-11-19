//go:build !windows

package shelllocal

import (
	"os/exec"
	"syscall"
)

// configure 让子进程进入独立进程组,便于超时时杀掉整棵树。
func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate 终止整个进程组(POSIX)。
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
