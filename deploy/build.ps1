# svrmon 构建脚本
#   1) 构建前端 (web/ -> web/dist)
#   2) 将 dist 嵌入 hub (cmd/hub/assets/dist)
#   3) 交叉编译 linux-amd64 与 windows-amd64 的 hub / agent 到 deploy/out/
#
# 用法:  pwsh -File deploy\build.ps1 [-SkipWeb]
param([switch]$SkipWeb)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

# ---------- 工具链定位 ----------
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    $fallback = 'D:\dev-tools\go\bin'
    if (Test-Path (Join-Path $fallback 'go.exe')) {
        $env:PATH = "$fallback;$env:PATH"
    } else {
        throw "未找到 go，请安装 Go 或将其加入 PATH"
    }
}

# ---------- 前端 ----------
if (-not $SkipWeb) {
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        $bundledNode = Join-Path $env:USERPROFILE '.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\node\bin'
        if (Test-Path (Join-Path $bundledNode 'node.exe')) {
            $env:PATH = "$bundledNode;$env:PATH"
        } else {
            throw "未找到 node，请安装 Node.js 或将其加入 PATH"
        }
    }

    $pnpmMjs = Join-Path $env:USERPROFILE '.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\pnpm\bin\pnpm.mjs'
    if (Get-Command pnpm -ErrorAction SilentlyContinue) {
        $pnpmExe = 'pnpm'; $pnpmPre = @()
    } elseif (Test-Path $pnpmMjs) {
        $pnpmExe = 'node'; $pnpmPre = @($pnpmMjs)
    } else {
        throw "未找到 pnpm"
    }

    Write-Host "==> 构建前端"
    Push-Location (Join-Path $root 'web')
    try {
        & $pnpmExe @($pnpmPre + @('install'))
        if ($LASTEXITCODE -ne 0) { throw "pnpm install 失败" }
        & $pnpmExe @($pnpmPre + @('run', 'build'))
        if ($LASTEXITCODE -ne 0) { throw "前端构建失败" }
    } finally {
        Pop-Location
    }

    Write-Host "==> 嵌入前端到 hub"
    $assets = Join-Path $root 'cmd\hub\assets\dist'
    Remove-Item -Recurse -Force $assets -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force $assets | Out-Null
    Copy-Item -Recurse -Force (Join-Path $root 'web\dist\*') $assets
}

# -SkipWeb 且从未构建前端时，放置占位页以保证 go:embed 不失败
$assetsDist = Join-Path $root 'cmd\hub\assets\dist'
if (-not (Test-Path (Join-Path $assetsDist 'index.html'))) {
    New-Item -ItemType Directory -Force $assetsDist | Out-Null
    Set-Content -Path (Join-Path $assetsDist 'index.html') -Value '<!doctype html><html><head><meta charset="utf-8"><title>svrmon</title></head><body style="background:#0b0f14;color:#d5dee8;font-family:sans-serif;padding:2rem"><h1>svrmon hub</h1><p>前端尚未构建：请运行 deploy/build.ps1（不带 -SkipWeb）。</p></body></html>'
    Write-Host "==> 已放置前端占位页（提示：构建时请去掉 -SkipWeb）"
}

# ---------- 二进制 ----------
$out = Join-Path $root 'deploy\out'
New-Item -ItemType Directory -Force $out | Out-Null
$env:CGO_ENABLED = '0'
$ldflags = '-s -w'

Write-Host "==> 编译 linux-amd64"
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'
foreach ($t in @('hub', 'agent')) {
    & go build -trimpath -ldflags $ldflags -o (Join-Path $out "svrmon-$t-linux-amd64") "./cmd/$t"
    if ($LASTEXITCODE -ne 0) { throw "编译 $t (linux) 失败" }
}
# 供 Docker 构建直接使用
Copy-Item (Join-Path $out 'svrmon-hub-linux-amd64') (Join-Path $root 'deploy\docker\svrmon-hub-linux-amd64') -Force

Write-Host "==> 编译 windows-amd64（本机调试用）"
$env:GOOS = 'windows'; $env:GOARCH = 'amd64'
foreach ($t in @('hub', 'agent')) {
    & go build -trimpath -ldflags $ldflags -o (Join-Path $out "svrmon-$t.exe") "./cmd/$t"
    if ($LASTEXITCODE -ne 0) { throw "编译 $t (windows) 失败" }
}
$env:GOOS = ''; $env:GOARCH = ''

Get-ChildItem $out | Select-Object Name, @{n = 'SizeMB'; e = { [math]::Round($_.Length / 1MB, 1) } }
Write-Host "`n构建完成 → $out"
