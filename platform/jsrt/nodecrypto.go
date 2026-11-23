package jsrt

// 本文件是 node:crypto 的宿主能力:哈希与随机数。
//
// 放在执行层的理由:它们没有策略语义(不读文件、不发请求、不改进程状态),
// 与 node:path / node:os 同属「默认可用、无需授权的环境能力」。所有返回值都是
// 字符串,跨 JSON 边界不会出现二进制对象;node:crypto 模块负责把它包装成 Node 形状。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// maxRandomHexBytes 与 node:crypto.randomBytes 的模块侧上限保持一致。
const maxRandomHexBytes = 65536

// nativeSHA256 实现 __adaHost.sha256(text):返回 UTF-8 字符串的 SHA-256(hex)。
func (r *Runtime) nativeSHA256(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if len(args) < 1 {
			return engine.Undefined(), fmt.Errorf("sha256(text) 需要一个参数")
		}
		text, err := toString(raw, args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("sha256 的输入必须是字符串: %w", err)
		}
		sum := sha256.Sum256([]byte(text))
		return raw.FromGo(hex.EncodeToString(sum[:]))
	}
}

// nativeRandomHex 实现 __adaHost.randomHex(n):返回 n 字节 crypto/rand 的 hex。
func (r *Runtime) nativeRandomHex(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if len(args) < 1 {
			return engine.Undefined(), fmt.Errorf("randomHex(n) 需要一个参数")
		}
		v, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("randomHex 的长度无法转换: %w", err)
		}
		n, ok := toInt64(v)
		if !ok || n < 0 {
			return engine.Undefined(), fmt.Errorf("randomHex 的长度必须是非负整数")
		}
		if n > maxRandomHexBytes {
			return engine.Undefined(), fmt.Errorf("randomHex 单次上限 %d 字节", maxRandomHexBytes)
		}
		buf := make([]byte, n)
		if _, rerr := rand.Read(buf); rerr != nil {
			return engine.Undefined(), fmt.Errorf("randomHex 生成随机数失败: %w", rerr)
		}
		return raw.FromGo(hex.EncodeToString(buf))
	}
}
