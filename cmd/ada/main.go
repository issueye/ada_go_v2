// 命令 ada 是装配工具入口:实现位于 host/cli(平台映射与输出)。
package main

import (
	"os"

	"github.com/adag/ada-go/v2/host/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
