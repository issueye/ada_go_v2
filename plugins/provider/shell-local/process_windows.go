//go:build windows

package shelllocal

import "os/exec"

// configure 目前无平台特定设置(Job Object 支持见 docs/seam-shell.md 边界)。
func configure(cmd *exec.Cmd) {}

// terminate 只终止父进程;进程树终止需要 Job Object,尚未实现。
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
