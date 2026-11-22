// 命令 ada-tui 是终端交互宿主入口:实现位于 platform/host/tui。
package main

import (
	"os"

	"github.com/issueye/ada_go_v2/platform/host/tui"
)

func main() {
	os.Exit(tui.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
