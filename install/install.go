// Package install 是 M5 的安装与签名(L2 基础设施,不是插件)。
//
// 它只做四件事:清单校验、Artifact 摘要、Ed25519 验签、原子落地到
// <dest>/<id>@<version>/。信任等级来自「签名密钥」而不是清单自述。
package install

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/v2/caps"
	"github.com/adag/ada-go/v2/compile"
	"github.com/adag/ada-go/v2/loader"
	"github.com/adag/ada-go/v2/plugin"
)

// SignatureFile 是插件目录里的签名文件名。
const SignatureFile = "plugin.sig.json"

// MessagePrefix 是签名原文的前缀(区分「插件签名」与其它用途的签名)。
const MessagePrefix = "ada-plugin-v2\n"

// 默认安装上限。
const (
	DefaultMaxFiles = 1000
	DefaultMaxBytes = int64(16 << 20)
)

// Key 是公钥及其代表的信任等级。
type Key struct {
	Public ed25519.PublicKey
	Trust  caps.Trust
}

// Signature 是 plugin.sig.json 的内容。
type Signature struct {
	KeyID     string `json:"keyId"`
	Algo      string `json:"algo"`
	Signature string `json:"signature"` // base64
}

// Options 控制校验与安装。
type Options struct {
	Keys             map[string]Key
	RequireSignature bool
	MaxFiles         int
	MaxBytes         int64
}

// Info 是校验/安装结果。
type Info struct {
	ID       string
	Version  string
	Kind     string
	Digest   string
	Trust    caps.Trust
	Signed   bool
	Dir      string
	Manifest *plugin.Manifest
}

// Message 构造签名原文:前缀 + 清单原文 + digest。
func Message(manifestJSON []byte, digest string) []byte {
	return []byte(MessagePrefix + string(manifestJSON) + "\n" + digest)
}

// Sign 用私钥签名(测试与发布工具使用)。
func Sign(priv ed25519.PrivateKey, manifestJSON []byte, digest string) []byte {
	return ed25519.Sign(priv, Message(manifestJSON, digest))
}

// VerifySignature 校验签名。
func VerifySignature(pub ed25519.PublicKey, manifestJSON []byte, digest string, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("install: 公钥长度非法")
	}
	if !ed25519.Verify(pub, Message(manifestJSON, digest), sig) {
		return fmt.Errorf("install: 签名校验失败")
	}
	return nil
}

// VerifyDir 校验一个插件目录:清单、Artifact 摘要与签名。
func VerifyDir(srcDir string, opts Options) (*Info, error) {
	manifests, err := plugin.LoadDir(srcDir)
	if err != nil {
		return nil, err
	}
	if len(manifests) != 1 {
		return nil, fmt.Errorf("install: %s 必须恰好包含一个插件,实际 %d 个", srcDir, len(manifests))
	}
	m := manifests[0]
	raw, err := os.ReadFile(filepath.Join(srcDir, "plugin.json"))
	if err != nil {
		return nil, fmt.Errorf("install: 读取清单失败: %w", err)
	}

	info := &Info{ID: m.ID, Version: m.Version, Kind: m.Kind, Dir: srcDir, Manifest: m}
	switch m.Kind {
	case "ts":
		sources, err := loader.CollectSources(srcDir)
		if err != nil {
			return nil, err
		}
		artifact, err := compile.Build(m.ID, m.Entry, sources, compile.Options{})
		if err != nil {
			return nil, fmt.Errorf("install: 编译 %s 失败: %w", m.ID, err)
		}
		info.Digest = artifact.Digest
	case "go":
		// Go 插件编译进宿主,没有可签名的 Artifact;信任为 builtin。
		info.Trust = caps.TrustBuiltin
	}

	sigPath := filepath.Join(srcDir, SignatureFile)
	sigData, sigErr := os.ReadFile(sigPath)
	if sigErr != nil {
		if !os.IsNotExist(sigErr) {
			return nil, fmt.Errorf("install: 读取签名失败: %w", sigErr)
		}
		if opts.RequireSignature && m.Kind == "ts" {
			return nil, fmt.Errorf("install: 插件 %s 未签名,但要求签名", m.ID)
		}
		if m.Kind == "ts" {
			info.Trust = caps.TrustThirdParty
		}
		return info, nil
	}
	if m.Kind == "go" {
		return nil, fmt.Errorf("install: Go 插件不接受 Artifact 签名(%s)", m.ID)
	}

	var sig Signature
	if err := json.Unmarshal(sigData, &sig); err != nil {
		return nil, fmt.Errorf("install: 签名文件损坏: %w", err)
	}
	if sig.Algo != "" && sig.Algo != "ed25519" {
		return nil, fmt.Errorf("install: 不支持的签名算法 %q", sig.Algo)
	}
	key, ok := opts.Keys[sig.KeyID]
	if !ok {
		return nil, fmt.Errorf("install: 未知签名密钥 %q", sig.KeyID)
	}
	rawSig, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil {
		return nil, fmt.Errorf("install: 签名不是合法 base64: %w", err)
	}
	if err := VerifySignature(key.Public, raw, info.Digest, rawSig); err != nil {
		return nil, fmt.Errorf("install: 插件 %s 验签失败: %w", m.ID, err)
	}
	info.Signed = true
	info.Trust = key.Trust
	if !caps.ValidTrust(info.Trust) {
		return nil, fmt.Errorf("install: 密钥 %q 的信任等级非法: %q", sig.KeyID, info.Trust)
	}
	return info, nil
}

// Install 校验后原子安装到 <DestRoot>/<id>@<version>/。
func Install(srcDir, destRoot string, opts Options) (*Info, error) {
	info, err := VerifyDir(srcDir, opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return nil, fmt.Errorf("install: 创建安装目录失败: %w", err)
	}
	final := filepath.Join(destRoot, info.ID+"@"+CleanVersion(info.Version))
	if _, err := os.Stat(final); err == nil {
		return nil, fmt.Errorf("install: 目标已存在,拒绝覆盖: %s", final)
	}

	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	tmp, err := os.MkdirTemp(destRoot, ".install-*")
	if err != nil {
		return nil, fmt.Errorf("install: 创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmp) // rename 成功后这里删除的是不存在的旧路径
	if err := copyTree(srcDir, tmp, maxFiles, maxBytes); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, fmt.Errorf("install: 原子重命名失败: %w", err)
	}
	info.Dir = final
	return info, nil
}

// copyTree 复制目录:拒绝符号链接,限制文件数与总字节数。
func copyTree(src, dst string, maxFiles int, maxBytes int64) error {
	files := 0
	var total int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("install: 目录里不允许符号链接: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		files++
		if files > maxFiles {
			return fmt.Errorf("install: 文件数超过上限(%d)", maxFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > maxBytes {
			return fmt.Errorf("install: 总大小超过上限(%d 字节)", maxBytes)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(p, target)
	})
	if err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// TrustFromKey 便于发布工具推导信任等级。
func TrustFromKey(trust string) (caps.Trust, error) {
	t := caps.Trust(trust)
	if !caps.ValidTrust(t) {
		return "", fmt.Errorf("install: 非法信任等级 %q", trust)
	}
	return t, nil
}

// CleanVersion 去掉版本号里的路径分隔符,避免目录逃逸(清单已校验 semver,这里再做一次防御)。
func CleanVersion(v string) string {
	return strings.NewReplacer("/", "_", `\`, "_", ":", "_").Replace(v)
}
