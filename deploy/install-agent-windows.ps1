<#
.SYNOPSIS
  svrmon agent Windows 11 安装 / 卸载脚本。

.DESCRIPTION
  自动完成：创建安装目录 → 复制二进制 → 写入配置 → 注册为服务（NSSM 优先，缺失时用系统自带任务计划）
  → 启动 → 校验。需要以管理员身份运行。

.EXAMPLE
  # 以管理员身份运行 PowerShell，然后：
  .\install-agent-windows.ps1 -HubUrl ws://192.168.1.10:8080/agent -Token 你的token -Name win-gpu01

.EXAMPLE
  # 只放文件与配置，不注册服务（无需管理员，适合先手动试跑）
  .\install-agent-windows.ps1 -HubUrl ws://192.168.1.10:8080/agent -Token xxx -NoService

.EXAMPLE
  .\install-agent-windows.ps1 -Uninstall          # 停止并移除服务/任务（保留配置与日志）
  .\install-agent-windows.ps1 -Uninstall -Purge   # 连同目录一起删除
#>
[CmdletBinding()]
param(
    [string]$HubUrl,
    [string]$Token,
    [string]$Name = $env:COMPUTERNAME,
    [string]$InstallDir = "$env:ProgramData\svrmon",
    [string]$BinaryPath,
    [ValidateSet('Auto', 'Nssm', 'Task')]
    [string]$ServiceType = 'Auto',
    [string]$NssmPath,
    [switch]$NoService,
    [switch]$Uninstall,
    [switch]$Purge
)

$ErrorActionPreference = 'Stop'
$ServiceName = 'svrmon-agent'
$TaskName = 'svrmon-agent'

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Warn($msg) { Write-Host "!! $msg" -ForegroundColor Yellow }

# ---------- 1. 解析二进制路径 ----------
function Resolve-AgentBinary {
    if ($BinaryPath) {
        if (-not (Test-Path $BinaryPath)) { throw "指定的二进制不存在：$BinaryPath" }
        return (Resolve-Path $BinaryPath).Path
    }
    $candidates = @(
        (Join-Path $PSScriptRoot 'svrmon-agent.exe'),
        (Join-Path $PSScriptRoot 'out\svrmon-agent.exe'),
        (Join-Path $PSScriptRoot '..\svrmon-agent.exe')
    )
    foreach ($c in $candidates) {
        if (Test-Path $c) { return (Resolve-Path $c).Path }
    }
    throw "找不到 svrmon-agent.exe，请把二进制放在脚本同目录，或用 -BinaryPath 指定（开发机产物在 deploy\out\）"
}

# ---------- 2. 停止并移除已有服务 / 任务 ----------
function Remove-ExistingService {
    $nssm = Get-Nssm
    if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
        Write-Step "移除已有服务 $ServiceName"
        if ($nssm) {
            & $nssm stop $ServiceName | Out-Null
            & $nssm remove $ServiceName confirm | Out-Null
        } else {
            Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
            & sc.exe delete $ServiceName | Out-Null
        }
        Start-Sleep -Seconds 1
    }
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Write-Step "移除已有任务计划 $TaskName"
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    }
}

# ---------- 3. NSSM 定位（可选） ----------
function Get-Nssm {
    if ($NssmPath -and (Test-Path $NssmPath)) { return (Resolve-Path $NssmPath).Path }
    $cmd = Get-Command nssm -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $common = @("$env:ProgramData\chocolatey\bin\nssm.exe", "$env:ProgramFiles\nssm\nssm.exe", 'C:\nssm\nssm.exe')
    foreach ($p in $common) { if (Test-Path $p) { return $p } }
    return $null
}

# ---------- 4. 安装服务（NSSM） ----------
function Install-NssmService($nssm, $exe, $args) {
    Write-Step "以 NSSM 注册服务（支持优雅停止：Ctrl+C → agent 发告别帧，不触发误告警）"
    & $nssm install $ServiceName $exe | Out-Null
    & $nssm set $ServiceName AppDirectory $InstallDir | Out-Null
    & $nssm set $ServiceName AppParameters $args | Out-Null
    & $nssm set $ServiceName Start SERVICE_AUTO_START | Out-Null
    & $nssm set $ServiceName AppStdout (Join-Path $InstallDir 'logs\nssm-out.log') | Out-Null
    & $nssm set $ServiceName AppStderr (Join-Path $InstallDir 'logs\nssm-err.log') | Out-Null
    & $nssm set $ServiceName AppRotateFiles 1 | Out-Null
    & $nssm start $ServiceName | Out-Null
}

# ---------- 5. 安装任务计划（系统自带，无需第三方） ----------
function Install-ScheduledTask($exe, $args) {
    Write-Warn "使用任务计划方式：系统自带、崩溃自动重启；但停止任务为强制结束进程，"
    Write-Warn "  维护性停止若超过离线观察期（默认 60s）会收到一封『失联』告警。需要优雅停止请用 NSSM。"
    $action = New-ScheduledTaskAction -Execute $exe -Argument $args -WorkingDirectory $InstallDir
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Seconds 0) `
        -MultipleInstances IgnoreNew -StartWhenAvailable
    Write-Step "注册任务计划 $TaskName（SYSTEM 账户，开机自启，崩溃后 1 分钟重启）"
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal `
        -Settings $settings -Description 'svrmon monitoring agent' | Out-Null
    Start-ScheduledTask -TaskName $TaskName
}

# ================= 主流程 =================
if ($Uninstall) {
    if ((Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) -or
        (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue)) {
        if (-not (Test-Admin)) { throw '卸载服务需要管理员权限，请以管理员身份运行 PowerShell' }
    }
    Remove-ExistingService
    Write-Host "已停止并移除 $ServiceName（配置与日志保留在 $InstallDir）"
    if ($Purge) {
        Remove-Item $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "已删除 $InstallDir"
    }
    return
}

if (-not $HubUrl -or -not $Token) {
    throw "缺少参数：请指定 -HubUrl ws://<hub>:8080/agent 与 -Token <agent token>（token 在 hub 的 hub.config.json 里）"
}
if ($Token -eq 'change-me') { throw 'token 不能是 change-me，请填 hub 生成的 agent token' }

$needAdmin = -not $NoService
if ($needAdmin -and -not (Test-Admin)) {
    throw "注册服务需要管理员权限：请右键『以管理员身份运行』PowerShell 后重试，或加 -NoService 仅部署文件"
}

# 1) 文件与配置
$exeSrc = Resolve-AgentBinary
Write-Step "安装目录：$InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir, (Join-Path $InstallDir 'logs') | Out-Null
$exeDst = Join-Path $InstallDir 'svrmon-agent.exe'
Copy-Item $exeSrc $exeDst -Force
Write-Step "已复制二进制：$exeDst"

$cfg = [ordered]@{
    hubUrl     = $HubUrl
    token      = $Token
    name       = $Name
    intervalMs = 2000
    gpu        = @{ enable = $true; path = 'nvidia-smi'; intervalSec = 5 }
    ipmi       = @{ enable = $true; path = 'ipmitool'; sensorSec = 30; selSec = 60 }
    logs       = @{ enable = $true; path = 'journalctl'; intervalSec = 30; backfillMin = 10 }
    hw         = @{ enable = $true; intervalMin = 30 }
    logFile    = (Join-Path $InstallDir 'logs\agent.log')
    logRetentionDays = 7
}
$cfgPath = Join-Path $InstallDir 'agent.config.json'
$json = $cfg | ConvertTo-Json -Depth 6
[IO.File]::WriteAllText($cfgPath, $json, (New-Object Text.UTF8Encoding($false)))
Write-Step "已写入配置：$cfgPath（name=$Name, hub=$HubUrl）"

$agentArgs = "-config `"$cfgPath`" -log `"$(Join-Path $InstallDir 'logs\agent.log')`""

if ($NoService) {
    Write-Host ''
    Write-Host '已按 -NoService 跳过服务注册。可前台试跑（Ctrl+C 退出）：' -ForegroundColor Green
    Write-Host "  & '$exeDst' -config `"$cfgPath`" -log `"$(Join-Path $InstallDir 'logs\agent.log')`""
    Write-Host ''
    Write-Host '确认能连上 hub 后，以管理员身份重跑本脚本（去掉 -NoService）即可注册为服务。'
    return
}

# 2) 注册服务
Remove-ExistingService
$nssm = Get-Nssm
$useNssm = $false
switch ($ServiceType) {
    'Nssm' { if (-not $nssm) { throw "未找到 nssm，请用 -NssmPath 指定，或改用 -ServiceType Task" }; $useNssm = $true }
    'Task' { $useNssm = $false }
    default { $useNssm = [bool]$nssm }
}
if ($useNssm) { Install-NssmService $nssm $exeDst $agentArgs }
else { Install-ScheduledTask $exeDst $agentArgs }

# 3) 校验
Start-Sleep -Seconds 5
$proc = Get-Process -Name 'svrmon-agent' -ErrorAction SilentlyContinue
Write-Host ''
if ($proc) {
    Write-Host "==> 安装完成，agent 正在运行（PID $($proc.Id -join ',')）" -ForegroundColor Green
} else {
    Write-Warn "未检测到 svrmon-agent 进程，请查看日志排查：$InstallDir\logs\"
}

$logFile = Get-ChildItem (Join-Path $InstallDir 'logs\agent-*.log') -ErrorAction SilentlyContinue |
    Sort-Object Name | Select-Object -Last 1
if ($logFile) {
    Write-Host "--- $($logFile.Name) 末尾 ---"
    Get-Content $logFile.FullName -Tail 8
}
Write-Host ''
Write-Host "后续操作：" -ForegroundColor Green
Write-Host "  查看进程： Get-Process svrmon-agent"
Write-Host "  查看日志： Get-Content '$InstallDir\logs\agent.log' -Tail 50 -Wait"
Write-Host "  停止/卸载：.\install-agent-windows.ps1 -Uninstall"
Write-Host "  网页确认： http://<hub IP>:8080 应出现名为 $Name 的服务器"
