# M5 安装与签名(install)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §13.4(签名与安装)、§13.2(信任等级)。

## 1. 范围

```
install/     L2 基础设施:清单校验、Artifact 摘要、Ed25519 验签、原子安装
cmd/ada      verify / install 两个子命令
```

本刀只做「校验 + 落地」;**不包含**在线拉取、索引、更新、回滚与安装同意(见 §6)。

## 2. 签名格式

签名覆盖 Artifact 摘要,而不是目录(运行的是 Artifact):

```
原文 = "ada-plugin-v2\n" + <plugin.json 原文> + "\n" + <artifact digest>
签名 = ed25519(privateKey, 原文)
```

`plugin.sig.json`:

```json
{ "keyId": "official-2026", "algo": "ed25519", "signature": "<base64>" }
```

- 只接受 `ed25519`(缺省 algo 视为 ed25519;其它算法拒绝)。
- 密钥来自调用方提供的 key ring(`keyId → {public, trust}`);未知 keyId → 拒绝。
- **篡改清单或 Artifact 任一项都会验签失败**(摘要与清单原文都在原文里)。

## 3. 信任等级

| 情况 | 信任 |
|---|---|
| ts + 有效签名 | 密钥声明的 trust(`verified` / `official`;`builtin` 应只出现在随二进制编译的插件) |
| ts + 未签名(未要求签名) | `third-party` |
| ts + 未签名(要求签名) | 拒绝 |
| go 插件 | `builtin`(Go 代码编译进宿主,没有 Artifact 可签) |

- Go 插件目录里出现 `plugin.sig.json` → 拒绝(签名只针对 Artifact)。
- `trust` 值来自密钥文件,不来自清单自述。

## 4. 安装语义

`install.Install(src, destRoot, opts)`:

1. `VerifyDir`:清单 → Artifact 摘要 → 验签;
2. 在 `destRoot` 下建临时目录`.install-*`;
3. 复制目录:拒绝符号链接,限制文件数(默认 1000)与总字节(默认 16 MiB);
4. `rename` 到 `<destRoot>/<id>@<version>/`;
5. 目标已存在 → 拒绝覆盖(更新必须走新版本目录);
6. 任一步失败 → 删除临时目录,既有目录不受影响。

## 5. CLI

```powershell
go run ./cmd/ada verify examples/plugins-ts/hello-ts
# 插件: hello-ts@0.1.0(ts)
# 摘要: sha256:...
# 信任: third-party
# 已签名: false

go run ./cmd/ada verify <目录> -keys keys.json -require-signature
go run ./cmd/ada install <目录> -dest <安装根> -keys keys.json -require-signature -yes
```

`keys.json` 格式:

```json
{ "official-2026": { "public": "<base64>", "trust": "official" } }
```

退出码:0 成功 / 1 校验或安装失败 / 2 用法错误(含未确认安装)。
`-yes` 是**安装同意**:插件声明了 `caps` 时,没有 `-yes` 会先展示 id/版本/信任/能力并拒绝安装(退出码 2)。

## 6. 偏差与边界

1. **没有在线拉取与索引**:`index.json`、下载、版本解析、更新、回滚都未实现。
2. **安装同意只有 CLI 级**:`-yes` 让 `ada install` 在展示能力后继续;桌面确认框/逐项授权未实现,`-yes` 是一次性整体同意。
3. **没有 `$ADA_HOME/keys` 与官方 keyring**:key ring 由调用方显式提供。
4. **trust 未进入 caps 强制**:安装得到 trust 后,没有把 `caps` 授权与 trust 关联(调用点强制仍缺)。
5. **摘要只覆盖 Artifact 文件内容**:目录级元数据(权限、空目录)不参与签名。
6. **无并发安装锁**:两个安装进程可能竞争同一目标(rename 语义保证不会半装,但错误信息可能不友好)。
7. **Windows Job Object/长路径**未处理:安装路径长度与保留名未显式校验。

## 7. 测试矩阵

| 场景 | 用例 |
|---|---|
| 合法签名 → verified,摘要一致 | `install.TestSignedVerified` |
| 清单篡改 → 验签失败 | `install.TestTamperedManifest` |
| 错误密钥 / 未知 keyId | `install.TestUnknownKeyAndBadSignature` |
| 未签名 third-party;要求签名则拒绝 | `install.TestUnsigned` |
| Go 插件 builtin,不接受 Artifact 签名 | `install.TestGoPluginBuiltin` |
| 原子安装、拒绝覆盖 | `install.TestInstallAtomic` |
| 符号链接拒绝 | `install.TestRejectsSymlink`(平台不支持时跳过) |
| 文件数/总大小上限 | `install.TestInstallLimits` |
| CLI:verify 能力清单、install 同意(-yes) | `host/cli.TestVerifyCaps`、`host/cli.TestInstallConsent` |

## 8. 下一步

1. 安装同意(能力清单展示 + CLI flag / 桌面确认),把 trust 灌进 `caps`。
2. `$ADA_HOME/keys` 与 keyring;签名发布工具(生成 `plugin.sig.json`)。
3. 索引/更新/回滚、并发安装锁。
4. 在线仓库拉取与 digest 固定(供应链完整性)。

## 9. 验证命令

```powershell
go test -race -count=1 ./install
go run ./cmd/ada verify examples/plugins-ts/hello-ts
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```