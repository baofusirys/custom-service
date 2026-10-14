#requires -Version 7
<#
.SYNOPSIS
  构建 Mac 隔离测试环境（~/cs-testenv）用的后端二进制（darwin/arm64）。仅用于测试，不用于生产。
.DESCRIPTION
  复制 backend 到 _internal/tmp/backend-testbuild，做测试环境专用替换后交叉编译：
  - 运行目录 /app/ → /Users/chengmeiran/cs-testenv/app/
  - 监听地址只绑 127.0.0.1
  - -FastMaintenance：后台维护启动延迟 2 分钟 → 5 秒，自动清理首次立即执行（便于验证维护任务）
  产物：_internal/tmp/backend-testbuild/cs-backend-darwin-arm64
#>
param(
  [switch]$FastMaintenance
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$dst = Join-Path $repo '_internal\tmp\backend-testbuild'
if ($dst -notlike '*\_internal\tmp\backend-testbuild') { throw "unexpected target $dst" }
if (Test-Path $dst) { Remove-Item -LiteralPath $dst -Recurse -Force }
New-Item -ItemType Directory -Path $dst | Out-Null
Get-ChildItem (Join-Path $repo 'backend') -Force | Where-Object { $_.Name -notin @('bin', 'tmp') } | Copy-Item -Destination $dst -Recurse -Force

function Edit-Text([string]$Path, [string]$Old, [string]$New) {
  $text = Get-Content -Raw -Encoding utf8 $Path
  if (-not $text.Contains($Old)) { throw "anchor not found in $Path : $Old" }
  [System.IO.File]::WriteAllText($Path, $text.Replace($Old, $New), (New-Object System.Text.UTF8Encoding($false)))
}
$main = Join-Path $dst 'cmd\server\main.go'
Edit-Text $main '"/app/' '"/Users/chengmeiran/cs-testenv/app/'
Edit-Text $main 'Addr:              ":" + cfg.HTTPPort,' 'Addr:              "127.0.0.1:" + cfg.HTTPPort,'
if ($FastMaintenance) {
  $feat = Join-Path $dst 'internal\service\features.go'
  Edit-Text $feat 'case <-time.After(2 * time.Minute):' 'case <-time.After(5 * time.Second):'
  Edit-Text $feat 'cleanTimer := time.NewTimer(time.Until(nextBeijingClock(time.Now(), autoCleanHour, autoCleanMinute)))' 'cleanTimer := time.NewTimer(time.Second)'
}
Push-Location $dst
try {
  $env:GOOS = 'darwin'; $env:GOARCH = 'arm64'; $env:CGO_ENABLED = '0'; $env:GOTOOLCHAIN = 'go1.26.9'
  go build -trimpath -o cs-backend-darwin-arm64 ./cmd/server
  if ($LASTEXITCODE -ne 0) { throw "go build failed: $LASTEXITCODE" }
} finally {
  Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED, Env:GOTOOLCHAIN -ErrorAction SilentlyContinue
  Pop-Location
}
$out = Join-Path $dst 'cs-backend-darwin-arm64'
"built=$out size=$((Get-Item $out).Length) fast_maintenance=$($FastMaintenance.IsPresent)"
