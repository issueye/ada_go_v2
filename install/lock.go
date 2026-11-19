// 安装目录并发锁:同一 destRoot 同一时间只允许一个安装/卸载/升级/回滚。
package install

import (
	"fmt"
	"os"
	"path/filepath"
)

// LockFile 是安装根目录下的锁文件名。
const LockFile = ".install.lock"

// withLock 在 destRoot 上获取进程锁;被占用时明确报错,不静默覆盖。
func withLock[T any](destRoot string, fn func() (T, error)) (T, error) {
	var zero T
	if destRoot == "" {
		return zero, fmt.Errorf("install: 安装根目录不能为空")
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return zero, fmt.Errorf("install: 创建安装根失败: %w", err)
	}
	lockPath := filepath.Join(destRoot, LockFile)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return zero, fmt.Errorf("install: 目标目录正在被另一个安装进程操作(%s)", lockPath)
		}
		return zero, fmt.Errorf("install: 获取安装锁失败: %w", err)
	}
	_, _ = fmt.Fprintf(f, "pid=%d", os.Getpid())
	_ = f.Close()
	defer os.Remove(lockPath)
	return fn()
}

// Install 校验后原子安装到 <DestRoot>/<id>@<version>/(持安装锁)。
func Install(srcDir, destRoot string, opts Options) (*Info, error) {
	return withLock(destRoot, func() (*Info, error) {
		return installUnlocked(srcDir, destRoot, opts)
	})
}

// Upgrade 安装更高版本并清理旧版本(持安装锁)。
func Upgrade(src, destRoot string, opts Options) (*Info, error) {
	return withLock(destRoot, func() (*Info, error) {
		return upgradeUnlocked(src, destRoot, opts)
	})
}

// Uninstall 删除某插件的全部版本(持安装锁)。
func Uninstall(destRoot, id string) (int, error) {
	return withLock(destRoot, func() (int, error) {
		return uninstallUnlocked(destRoot, id)
	})
}

// Rollback 回滚到次新版本(持安装锁)。
func Rollback(destRoot, id string) (string, error) {
	return withLock(destRoot, func() (string, error) {
		return rollbackUnlocked(destRoot, id)
	})
}
