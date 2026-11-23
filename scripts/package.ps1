# 编译并打包发布物:
#   dist/<name>/bin/       入口二进制
#   dist/<name>/plugins/   由编译进二进制的 builtins 自动导出的 plugin.json
#   dist/<name>/presets/   只引用包内 plugins/workspace/data 的发布 preset
#   dist/<name>/workspace/ 默认工作区与 docs 只读示例
#
# 关键约束:plugins/ 不是从 examples/ 手抄,而是从 builtins.Plugins() 导出,
# 因此发布包的清单与二进制内置插件集合一致;preset 也不包含 examples/ 路径。
param(
    [string]$Version = "",
    [string]$GOOS = "",
    [string]$GOARCH = "",
    [string]$OutputDir = "dist",
    [string]$LLMEndpoint = "",
    [string]$LLMModel = "",
    [switch]$SkipBuild,
    [switch]$NoZip,
    [switch]$StripDebug
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $goosValue = if ($GOOS) { $GOOS } else { (& go env GOOS).Trim() }
    $goarchValue = if ($GOARCH) { $GOARCH } else { (& go env GOARCH).Trim() }
    $hostGOOS = (& go env GOOS).Trim()
    $hostGOARCH = (& go env GOARCH).Trim()
    $ext = if ($goosValue -eq "windows") { ".exe" } else { "" }
    $commit = (& git rev-parse --short HEAD).Trim()
    $ver = if ($Version) { $Version } else { "dev-$commit" }
    if ($ver -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') {
        throw "版本号只能包含字母、数字、点、下划线与短横线: $ver"
    }
    $pkgName = "ada-$ver-$goosValue-$goarchValue"
    if ($LLMEndpoint -and -not $LLMModel) {
        throw "-LLMEndpoint 与 -LLMModel 必须同时提供"
    }
    if ($LLMModel -and -not $LLMEndpoint) {
        throw "-LLMEndpoint 与 -LLMModel 必须同时提供"
    }

    $pkgRel = Join-Path $OutputDir $pkgName
    $outRoot = Join-Path $root $OutputDir
    $pkg = Join-Path $root $pkgRel
    $binDir = Join-Path $pkg "bin"
    if ((Test-Path $pkg) -and $SkipBuild) {
        if (-not (Test-Path $binDir)) {
            throw "-SkipBuild 要求已有 $binDir"
        }
        # 复用 bin;清掉上次生成的数据,避免旧 preset/清单残留。
        foreach ($sub in @("plugins", "presets", "data", "workspace", "docs")) {
            $old = Join-Path $pkg $sub
            if (Test-Path $old) { Remove-Item -LiteralPath $old -Recurse -Force }
        }
    } elseif (Test-Path $pkg) {
        $resolved = (Resolve-Path $pkg).Path
        $prefix = $outRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "拒绝删除非输出目录: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $pkg | Out-Null

    if (-not $SkipBuild) {
        & pwsh -NoProfile -File (Join-Path $root "scripts/build.ps1") `
            -OutputDir (Join-Path $pkgRel "bin") -GOOS $goosValue -GOARCH $goarchValue -StripDebug:$StripDebug
        if ($LASTEXITCODE -ne 0) {
            throw "编译失败(exit $LASTEXITCODE)"
        }
    }
    foreach ($cmd in @("ada", "ada-rpc-agent", "ada-demo", "ada-ts-demo")) {
        $exe = Join-Path $binDir ($cmd + $ext)
        if (-not (Test-Path $exe)) {
            throw "缺少二进制: $exe"
        }
    }

    # 从 compiled builtins 生成清单;不复制 examples 里的手写清单。
    $plugins = Join-Path $pkg "plugins"
    & go run ./scripts/tools/builtins-dump -out $plugins
    if ($LASTEXITCODE -ne 0) {
        throw "导出内置插件清单失败(exit $LASTEXITCODE)"
    }
    $ids = @(Get-ChildItem $plugins -Directory | Select-Object -ExpandProperty Name | Sort-Object)
    if ($ids.Count -eq 0) {
        throw "没有导出任何内置插件"
    }

    $agentAllow = @(
        "agent-loop", "compaction-basic", "fs-local", "llm-replay",
        "seam-fs", "seam-llm", "seam-prompts", "seam-session", "seam-tools",
        "session-memory", "session-stats", "system-prompt", "tool-fs"
    )
    $workspaceAllow = $agentAllow + @(
        "seam-workspace", "tool-workspace", "workspace-guard", "workspace-local"
    )
    $missing = @($agentAllow | Where-Object { $_ -notin $ids })
    if ($missing.Count -gt 0) {
        throw "allowlist 引用了不存在的 builtin: $($missing -join ', ')"
    }

    $dataDir = Join-Path $pkg "data"
    New-Item -ItemType Directory -Force -Path $dataDir, $plugins | Out-Null
    Copy-Item "examples/agent/replay.jsonl" (Join-Path $dataDir "agent-replay.jsonl") -Force
    Copy-Item "examples/workspace/replay.jsonl" (Join-Path $dataDir "workspace-replay.jsonl") -Force

    $workspaceDir = Join-Path $pkg "workspace"
    $docsDir = Join-Path $pkg "docs"
    New-Item -ItemType Directory -Force -Path $workspaceDir, $docsDir | Out-Null
    Copy-Item "examples/agent/workspace/a.txt" (Join-Path $workspaceDir "a.txt") -Force
    Copy-Item "examples/workspace/docs/a.txt" (Join-Path $docsDir "a.txt") -Force

    $presetsDir = Join-Path $pkg "presets"
    New-Item -ItemType Directory -Force -Path $presetsDir | Out-Null
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    $replayModels = @([ordered]@{ id = "replay"; contextWindow = 8192; maxOutput = 1024 })
    $disableAgent = @($ids | Where-Object { $_ -notin $agentAllow })
    $disableWorkspace = @($ids | Where-Object { $_ -notin $workspaceAllow })

    $agentPreset = [ordered]@{
        name = "agent-release"
        plugins = @("../plugins")
        disable = $disableAgent
        patch = [ordered]@{
            "agent-loop" = @{ model = "replay" }
            "llm-replay" = [ordered]@{
                path = "data/agent-replay.jsonl"; models = $replayModels; defaultModel = "replay"
            }
            "fs-local" = @{ workspace = "workspace"; tmp = "workspace/.ada-tmp" }
        }
    }
    $workspacePreset = [ordered]@{
        name = "workspace-release"
        plugins = @("../plugins")
        disable = $disableWorkspace
        patch = [ordered]@{
            "agent-loop" = @{ model = "replay" }
            "llm-replay" = [ordered]@{
                path = "data/workspace-replay.jsonl"; models = $replayModels; defaultModel = "replay"
            }
            "fs-local" = @{ workspace = ""; tmp = "workspace/.ada-tmp" }
            "workspace-local" = [ordered]@{
                active = "repo"
                workspaces = @(
                    [ordered]@{ id = "repo"; name = "示例工作区"; root = "workspace"; ignore = @(".git", "bin", "dist") },
                    [ordered]@{ id = "docs"; name = "只读文档"; root = "docs"; readOnly = $true }
                )
            }
            "workspace-guard" = @{ denyWrite = @(".git", ".ada-tmp"); denyExec = @(".git") }
        }
    }
    [System.IO.File]::WriteAllText((Join-Path $presetsDir "agent-release.preset.json"),
        (($agentPreset | ConvertTo-Json -Depth 10) + "`n"), $utf8)
    [System.IO.File]::WriteAllText((Join-Path $presetsDir "workspace-release.preset.json"),
        (($workspacePreset | ConvertTo-Json -Depth 10) + "`n"), $utf8)

    if ($LLMEndpoint -and $LLMModel) {
        $realAllow = @($agentAllow | Where-Object { $_ -ne "llm-replay" }) + @("llm-openai")
        $disableReal = @($ids | Where-Object { $_ -notin $realAllow })
        $realPreset = [ordered]@{
            name = "agent-real-release"
            plugins = @("../plugins")
            disable = $disableReal
            patch = [ordered]@{
                "agent-loop" = @{ model = $LLMModel }
                "llm-openai" = @{ endpoint = $LLMEndpoint; model = $LLMModel; apiKeyEnv = "ADA_LLM_API_KEY"; maxTokens = 512 }
                "fs-local" = @{ workspace = "workspace"; tmp = "workspace/.ada-tmp" }
            }
        }
        [System.IO.File]::WriteAllText((Join-Path $presetsDir "agent-real-release.preset.json"),
            (($realPreset | ConvertTo-Json -Depth 10) + "`n"), $utf8)
    }

    $binNames = @(Get-ChildItem $binDir -File | Select-Object -ExpandProperty Name)
    $readme = @"
Ada 发布包 $pkgName

在包根目录运行(不要回退到源码目录):
  bin/ada$ext plan  --preset presets/agent-release.preset.json
  bin/ada$ext agent --preset presets/agent-release.preset.json -message "读取 a.txt 并总结"
  bin/ada$ext agent --preset presets/workspace-release.preset.json --workspace docs -message "查询工作区并读取 a.txt"

plugins/ 由编译进二进制的 builtins 自动导出,与源码手写清单无关;
presets/ 只引用 ../plugins、workspace/ 与 data/,不包含开发目录路径。
真实端点可用 LLMEndpoint/LLMModel 参数生成 agent-real-release.preset.json。
"@
    [System.IO.File]::WriteAllText((Join-Path $pkg "README.txt"), ($readme.TrimEnd() + "`n"), $utf8)

    $meta = [ordered]@{
        name = $pkgName; version = $ver; commit = $commit
        goos = $goosValue; goarch = $goarchValue
        plugins = $ids; binaries = $binNames
    }
    [System.IO.File]::WriteAllText((Join-Path $pkg "package.json"),
        (($meta | ConvertTo-Json -Depth 6) + "`n"), $utf8)

    # 同平台时用打包后的二进制自检,确保 preset 不依赖开发目录。
    if ($goosValue -eq $hostGOOS -and $goarchValue -eq $hostGOARCH) {
        $ada = Join-Path $binDir ("ada" + $ext)
        Push-Location $pkg
        try {
            foreach ($name in @("agent-release.preset.json", "workspace-release.preset.json")) {
                & $ada plan --preset (Join-Path "presets" $name) | Out-Null
                if ($LASTEXITCODE -ne 0) {
                    throw "包内 plan 自检失败: $name"
                }
            }
        } finally {
            Pop-Location
        }
        Write-Host "包内自检通过"
    } else {
        Write-Host "交叉编译目标,跳过包内运行自检"
    }

    $zip = Join-Path $root (Join-Path $OutputDir ($pkgName + ".zip"))
    if ($NoZip) {
        if (Test-Path $zip) { Remove-Item -LiteralPath $zip -Force }
    } else {
        if (Test-Path $zip) { Remove-Item -LiteralPath $zip -Force }
        Compress-Archive -Path $pkg -DestinationPath $zip -Force
        Write-Host "打包完成: $zip"
    }
    Write-Host "发布目录: $pkg"
}
finally {
    Pop-Location
}
