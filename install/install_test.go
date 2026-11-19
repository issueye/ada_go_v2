package install_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/v2/caps"
	"github.com/adag/ada-go/v2/compile"
	"github.com/adag/ada-go/v2/install"
	"github.com/adag/ada-go/v2/loader"
)

const tsPluginSource = `type Ctx = { log: (m: string) => void }
export function apply(ctx: Ctx): void { ctx.log("hi") }`

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

// writeTSPlugin 写一个可编译的 ts 插件,返回清单原文与 Artifact 摘要。
func writeTSPlugin(t *testing.T) (dir string, raw []byte, digest string) {
	t.Helper()
	dir = t.TempDir()
	manifest := `{"id":"ts-demo","version":"0.1.0","kind":"ts","entry":"plugin.ts","provide":["ts.demo"]}`
	writeFile(t, dir, "plugin.ts", tsPluginSource)
	writeFile(t, dir, "plugin.json", manifest)
	raw = []byte(manifest)
	sources, err := loader.CollectSources(dir)
	if err != nil {
		t.Fatalf("收集源码失败: %v", err)
	}
	artifact, err := compile.Build("ts-demo", "plugin.ts", sources, compile.Options{})
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	return dir, raw, artifact.Digest
}

func writeSignature(t *testing.T, dir, keyID string, sig []byte) {
	t.Helper()
	body := install.Signature{KeyID: keyID, Algo: "ed25519", Signature: base64.StdEncoding.EncodeToString(sig)}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化签名失败: %v", err)
	}
	writeFile(t, dir, install.SignatureFile, string(data))
}

func newKeyRing(t *testing.T, trust caps.Trust) (map[string]install.Key, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	return map[string]install.Key{"k1": {Public: pub, Trust: trust}}, priv
}

// 1. 合法签名:验签通过,信任来自密钥。
func TestSignedVerified(t *testing.T) {
	dir, raw, digest := writeTSPlugin(t)
	keys, priv := newKeyRing(t, caps.TrustVerified)
	writeSignature(t, dir, "k1", install.Sign(priv, raw, digest))

	info, err := install.VerifyDir(dir, install.Options{Keys: keys})
	if err != nil {
		t.Fatalf("验签失败: %v", err)
	}
	if !info.Signed || info.Trust != caps.TrustVerified {
		t.Fatalf("信任信息不正确: %+v", info)
	}
	if info.Digest != digest {
		t.Fatalf("摘要不一致: %s != %s", info.Digest, digest)
	}
}

// 2. 清单被篡改 → 验签失败。
func TestTamperedManifest(t *testing.T) {
	dir, raw, digest := writeTSPlugin(t)
	keys, priv := newKeyRing(t, caps.TrustVerified)
	writeSignature(t, dir, "k1", install.Sign(priv, raw, digest))
	writeFile(t, dir, "plugin.json", string(raw)+"  ")

	if _, err := install.VerifyDir(dir, install.Options{Keys: keys}); err == nil || !strings.Contains(err.Error(), "验签") {
		t.Fatalf("篡改清单应验签失败,实际 %v", err)
	}
}

// 3. 未知密钥 / 错误签名 → 拒绝。
func TestUnknownKeyAndBadSignature(t *testing.T) {
	dir, raw, digest := writeTSPlugin(t)
	keys, _ := newKeyRing(t, caps.TrustVerified)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	_ = otherPub
	writeSignature(t, dir, "k1", install.Sign(otherPriv, raw, digest))
	if _, err := install.VerifyDir(dir, install.Options{Keys: keys}); err == nil {
		t.Fatalf("错误密钥签名应失败")
	}

	writeSignature(t, dir, "nope", make([]byte, ed25519.SignatureSize))
	if _, err := install.VerifyDir(dir, install.Options{Keys: keys}); err == nil || !strings.Contains(err.Error(), "未知签名密钥") {
		t.Fatalf("未知密钥应报错,实际 %v", err)
	}
}

// 4. 未签名:默认按 third-party;要求签名时拒绝。
func TestUnsigned(t *testing.T) {
	dir, _, _ := writeTSPlugin(t)
	info, err := install.VerifyDir(dir, install.Options{})
	if err != nil {
		t.Fatalf("未签名(不强制)应通过: %v", err)
	}
	if info.Signed || info.Trust != caps.TrustThirdParty {
		t.Fatalf("未签名信任不正确: %+v", info)
	}
	if _, err := install.VerifyDir(dir, install.Options{RequireSignature: true}); err == nil {
		t.Fatalf("要求签名时应拒绝")
	}
}

// 5. Go 插件:信任 builtin,不参与 Artifact 签名。
func TestGoPluginBuiltin(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plugin.json", `{"id":"go-demo","version":"0.1.0","kind":"go"}`)
	info, err := install.VerifyDir(dir, install.Options{RequireSignature: true})
	if err != nil {
		t.Fatalf("Go 插件应通过(签名只针对 Artifact): %v", err)
	}
	if info.Trust != caps.TrustBuiltin || info.Signed {
		t.Fatalf("Go 插件信任不正确: %+v", info)
	}
}

// 6. 安装:原子落地,拒绝覆盖既有版本。
func TestInstallAtomic(t *testing.T) {
	dir, raw, digest := writeTSPlugin(t)
	keys, priv := newKeyRing(t, caps.TrustVerified)
	writeSignature(t, dir, "k1", install.Sign(priv, raw, digest))

	dest := t.TempDir()
	info, err := install.Install(dir, dest, install.Options{Keys: keys})
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	want := filepath.Join(dest, "ts-demo@0.1.0")
	if info.Dir != want {
		t.Fatalf("安装目录不正确: %s", info.Dir)
	}
	if _, err := os.Stat(filepath.Join(want, "plugin.ts")); err != nil {
		t.Fatalf("插件文件未复制: %v", err)
	}
	if _, err := install.Install(dir, dest, install.Options{Keys: keys}); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("重复安装应拒绝覆盖,实际 %v", err)
	}
}

// 7. 目录里的符号链接拒绝安装。
func TestRejectsSymlink(t *testing.T) {
	dir, raw, digest := writeTSPlugin(t)
	keys, priv := newKeyRing(t, caps.TrustVerified)
	writeSignature(t, dir, "k1", install.Sign(priv, raw, digest))

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写外部文件失败: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.txt"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("平台无法创建符号链接: %v", err)
	}
	if _, err := install.Install(dir, t.TempDir(), install.Options{Keys: keys}); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("符号链接应拒绝,实际 %v", err)
	}
}

// 8. 安装上限:文件数与总字节数。
func TestInstallLimits(t *testing.T) {
	dir, raw, digest := writeTSPlugin(t)
	keys, priv := newKeyRing(t, caps.TrustVerified)
	writeSignature(t, dir, "k1", install.Sign(priv, raw, digest))
	writeFile(t, dir, "extra.txt", "x")

	if _, err := install.Install(dir, t.TempDir(), install.Options{Keys: keys, MaxFiles: 1}); err == nil || !strings.Contains(err.Error(), "文件数") {
		t.Fatalf("文件数超限应拒绝,实际 %v", err)
	}
	if _, err := install.Install(dir, t.TempDir(), install.Options{Keys: keys, MaxBytes: 8}); err == nil || !strings.Contains(err.Error(), "总大小") {
		t.Fatalf("总大小超限应拒绝,实际 %v", err)
	}
}

// 9. 安装后写索引;List 能读回。
func TestIndexWrittenAndListed(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"),
		[]byte(`{"id":"idx-demo","version":"0.1.0","kind":"go","caps":["fs.read"]}`), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	dest := t.TempDir()
	if _, err := install.Install(src, dest, install.Options{}); err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	entries, err := install.List(dest)
	if err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("索引应有 1 条,实际 %d", len(entries))
	}
	e := entries[0]
	if e.ID != "idx-demo" || e.Trust != string(caps.TrustBuiltin) || e.Signed {
		t.Fatalf("索引条目不正确: %+v", e)
	}
	if len(e.Caps) != 1 || e.Caps[0] != "fs.read" {
		t.Fatalf("索引未记录能力: %+v", e)
	}
}

// 10. 空目录:List 返回空,不报错。
func TestListEmpty(t *testing.T) {
	entries, err := install.List(t.TempDir())
	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("空目录应无条目,实际 %d", len(entries))
	}
}

// 11. 索引损坏:明确报错。
func TestListCorrupt(t *testing.T) {
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, install.IndexFile), []byte("not json"), 0o644); err != nil {
		t.Fatalf("写坏索引失败: %v", err)
	}
	if _, err := install.List(dest); err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("损坏索引应报错,实际 %v", err)
	}
}
