# 内核门禁脚本(M1 起接入 CI)。
# 用法:pwsh scripts/guard.ps1
# 退出码非 0 表示违反 AGENTS.md §5 的硬规则。

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $root

$fail = 0
function Pass($name) { Write-Host "== $name`n  OK" }
function Fail($name, $detail) {
    Write-Host "== $name"
    $detail | ForEach-Object { Write-Host "  违规: $_" }
    $script:fail = 1
}

# 1. 内核不得出现 agent 词汇:出现即说明业务逻辑被塞进了内核。
$hits = Get-ChildItem kernel -Recurse -Include *.go |
    Select-String -Pattern '\b(Turn|Message|ToolCall|Model|Session|Agent)\b'
if ($hits) {
    Fail "kernel 无 agent 词汇" ($hits | ForEach-Object { "$($_.Path):$($_.LineNumber) $($_.Line.Trim())" })
} else {
    Pass "kernel 无 agent 词汇"
}

# 2. 内核不得直接碰系统调用面:资源访问必须走 seam 与 caps。
$hits = Get-ChildItem kernel -Recurse -Include *.go |
    Select-String -Pattern 'os/exec|net/http|os\.Open|os\.Create|syscall\.'
if ($hits) {
    Fail "kernel 无 os/exec、net/http、os.Open" ($hits | ForEach-Object { "$($_.Path):$($_.LineNumber)" })
} else {
    Pass "kernel 无 os/exec、net/http、os.Open"
}

# 3. 内核不得依赖执行层 / 宿主 / 插件。
$deps = go list -deps ./kernel 2>&1
$bad = $deps | Where-Object { $_ -match 'ada-go/v2/(jsrt|compile|host|plugins)' }
if ($bad) {
    Fail "kernel 依赖不含 jsrt/compile/host/plugins" $bad
} else {
    Pass "kernel 依赖不含 jsrt/compile/host/plugins"
}

# 4. 格式:未格式化的文件不进主干(评审不该花在空格上)。
$unformatted = gofmt -l .
if ($unformatted) {
    Fail "gofmt" $unformatted
} else {
    Pass "gofmt"
}

# 5. 编译、静态检查、竞态测试。
go build ./... 2>&1 | Write-Host
if ($LASTEXITCODE -ne 0) { Fail "go build" @("go build 失败") } else { Pass "go build" }

go vet ./... 2>&1 | Write-Host
if ($LASTEXITCODE -ne 0) { Fail "go vet" @("go vet 失败") } else { Pass "go vet" }

# 5. staticcheck(AGENTS §5.2)。工具链版本不匹配时给安装命令,而不是静默跳过:
#    「跳过」会让门禁看起来通过,实际什么都没检查。
$sc = Get-Command staticcheck -ErrorAction SilentlyContinue
if (-not $sc) {
    Fail "staticcheck" @("未安装 staticcheck;go install honnef.co/go/tools/cmd/staticcheck@latest")
} else {
    staticcheck ./... 2>&1 | Write-Host
    if ($LASTEXITCODE -ne 0) { Fail "staticcheck" @("staticcheck 报出问题") } else { Pass "staticcheck" }
}

go test -race -count=1 -timeout 120s ./... 2>&1 | Write-Host
if ($LASTEXITCODE -ne 0) { Fail "go test -race" @("测试失败") } else { Pass "go test -race" }

# 6. 交叉编译(AGENTS §5.2 / 硬约束 C8):纯 Go、CGO=0、三平台可构建。
$prevCGO = $env:CGO_ENABLED
$prevOS = $env:GOOS
$prevArch = $env:GOARCH
$env:CGO_ENABLED = "0"
$crossFail = @()
foreach ($os in @("linux", "darwin", "windows")) {
    $env:GOOS = $os
    $env:GOARCH = "amd64"
    go build ./... 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) { $crossFail += "GOOS=$os GOARCH=amd64 构建失败" }
}
$env:CGO_ENABLED = $prevCGO
$env:GOOS = $prevOS
$env:GOARCH = $prevArch
if ($crossFail.Count -gt 0) {
    Fail "交叉编译(linux/darwin/windows, CGO=0)" $crossFail
} else {
    Pass "交叉编译(linux/darwin/windows, CGO=0)"
}

# 7. 代码预算(AGENTS §5.3):kernel ≤ 3000 行。
#    只统计生产代码——预算是「内核越厚,插件越难替换」,测试不随二进制发布,
#    但测试行数一并打印,避免有人用测试把真实增长藏起来。
$prod = (Get-ChildItem kernel -Recurse -Include *.go |
    Where-Object { $_.Name -notlike '*_test.go' } | Get-Content | Measure-Object -Line).Lines
$test = (Get-ChildItem kernel -Recurse -Include *_test.go | Get-Content | Measure-Object -Line).Lines
if ($prod -gt 3000) {
    Fail "kernel 行数预算" @("生产代码 $prod 行,超过 3000(测试另有 $test 行)")
} else {
    Pass "kernel 行数预算(生产 $prod / 3000,测试 $test 行)"
}

if ($fail -ne 0) {
    Write-Host "`n门禁失败。" -ForegroundColor Red
    exit 1
}
Write-Host "`n门禁通过。" -ForegroundColor Green
