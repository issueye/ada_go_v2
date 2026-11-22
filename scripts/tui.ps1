# 启动终端交互宿主 ada-tui。默认用离线回放 preset; -Real 切到真实端点。
# 用法:
#   pwsh scripts/tui.ps1                         # 工作区示例(replay)
#   pwsh scripts/tui.ps1 -Workspace docs
#   pwsh scripts/tui.ps1 -Real -Workspace docs   # 需要 ADA_LLM_API_KEY
#   pwsh scripts/tui.ps1 -Preset examples/agent/agent.preset.json
#   pwsh scripts/tui.ps1 -Build                  # 先重编 bin/ada-tui
param(
    [string]$Preset = "examples/workspace/workspace.preset.json",
    [string]$Workspace = "",
    [switch]$Real,
    [switch]$Plain,
    [switch]$Build
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if ($Real) {
        if (-not $env:ADA_LLM_API_KEY) {
            throw "真实端点需要先设置 ADA_LLM_API_KEY(不要写入仓库或脚本)"
        }
        # 真实端点示例固定使用 workspace real preset;模型/端点由 preset 决定。
        $Preset = "examples/workspace/real.preset.json"
    }
    if (-not (Test-Path $Preset)) {
        throw "preset 不存在: $Preset"
    }

    $ext = if ($env:OS -eq "Windows_NT") { ".exe" } else { "" }
    $exe = Join-Path $root ("bin/ada-tui" + $ext)
    if ($Build -or -not (Test-Path $exe)) {
        New-Item -ItemType Directory -Force -Path (Join-Path $root "bin") | Out-Null
        Write-Host "编译 ada-tui -> $exe"
        & go build -trimpath -o $exe ./cmd/ada-tui
        if ($LASTEXITCODE -ne 0) {
            throw "编译 ada-tui 失败(exit $LASTEXITCODE)"
        }
    }

    $tuiArgs = @("--preset", $Preset)
    if ($Workspace) {
        $tuiArgs += @("--workspace", $Workspace)
    }
    if ($Plain) {
        $tuiArgs += "--plain"
    }
    Write-Host "启动 ada-tui: preset=$Preset workspace=$(if ($Workspace) { $Workspace } else { 'auto' })"
    & $exe @tuiArgs
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
