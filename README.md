# svrmon · 局域网服务器监控系统

简洁的多服务器监控系统：每台 Ubuntu 服务器运行一个 **Agent**（本地采集系统指标 / NVIDIA GPU / IPMI），主动推送到中心 **Hub**（SQLite 存历史 + 内嵌 Web 前端），浏览器打开即用。

```
┌─ Ubuntu 服务器 A ─────┐   ┌─ Ubuntu GPU 服务器 B ─┐
│ agent（Go 单二进制）    │   │ agent（Go 单二进制）    │
│  ├ CPU/内存/磁盘/网络   │   │  ├ nvidia-smi → GPU    │
│  └ 本地 ipmitool → IPMI│   │  └ 同左（无 BMC 自动跳过）│
└─────────┬─────────────┘   └──────────┬─────────────┘
          │ WebSocket 出站连接（被监控机无需开入站端口）
          ▼                             ▼
     ┌──────────── Hub（任一台 Ubuntu，单二进制，systemd 常驻）────────────┐
     │ agent 接入 + Token 认证 → SQLite 落盘（10s 粒度，7 天滚动清理）      │
     │ → 浏览器 REST + WebSocket → 内嵌 Vue3 + ECharts 前端                │
     └─────────────────────────────────────────────────────────────────────┘
```

## 功能

| 模块 | 内容 | 采集方式 | 频率 |
|---|---|---|---|
| CPU | 总/每核利用率、iowait、温度、负载、运行时间 | /proc、hwmon | 2s |
| 内存 | 已用/可用/缓存/buffer、swap | /proc/meminfo | 2s |
| 磁盘 | **每块盘**读写吞吐、IOPS、繁忙度（含 md/LVM 卷，排除 loop/zram 等伪设备）；各挂载点容量 | /proc/diskstats、df | 2s / 30s |
| 网络 | 各网卡上下行吞吐、错误/丢包累计 | /proc/net/dev | 2s |
| GPU | 利用率、显存、温度、风扇、功耗、频率、P-State、ECC、占用进程 | nvidia-smi（无需 cgo） | 5s（`gpu.intervalSec`） |
| IPMI | 温度/风扇/电压/功率传感器、整机功耗、SEL 事件日志 | 本地 ipmitool | 30s / 60s（`ipmi.sensorSec`/`selSec`） |
| 电源控制 | 开机 / 软关机 / 硬断电 / 硬重启 / 断电循环（页面二次确认） | ipmitool chassis power | 按需 |
| 日志 | 内核/硬件/系统严重错误（journalctl err 级以上，含 MCE·EDAC·PCIe AER） | journalctl -p 3 | 30s |
| 本机配置 | CPU/内存条/硬盘/网卡/GPU 型号明细、主板与整机序列号、BIOS/BMC 版本 | lscpu·dmidecode·lsblk·lspci·nvidia-smi | 30min |
| 历史 | 全指标趋势图，1小时 ~ 7天 | SQLite 聚合查询 | 10s 落盘 |

页面：**总览**（多机卡片网格）→ 单机详情四个标签：**实时**（CPU/内存/磁盘/网络曲线、磁盘容量、每核负载）/ **GPU**（每卡参数 + 进程表）/ **IPMI**（传感器分组表 + 电源控制 + SEL）/ **历史**。

## 目录结构

```
cmd/hub/          中心服务（前端通过 go:embed 打进二进制）
cmd/agent/        采集 agent
internal/model/   agent↔hub↔浏览器 的消息契约
internal/agent/   agent 运行时 + 采集器（collect/{sys,gpu,ipmi}.go）
internal/hub/     hub：WS 接入 / 广播 / SQLite / REST API / 静态托管
web/              Vue3 + ECharts 前端
deploy/           build.ps1、systemd 服务、配置示例、安装脚本
```

## 快速开始

> **完整的安装 / 升级 / 回滚 / 运维手册见 [docs/DEPLOY.md](docs/DEPLOY.md)**（含 Docker 软路由、Windows 11、版本升级一键脚本）。下面是最短路径。

### 1. 构建（开发机，Windows 或 Linux 均可）

需要 Go 1.22+ 和 Node.js（pnpm）。

```powershell
# Windows
pwsh -File deploy\build.ps1
# 产物: deploy/out/svrmon-{hub,agent}-linux-amd64
```

```bash
# Linux/macOS 等价命令
(cd web && pnpm install && pnpm run build)
rm -rf cmd/hub/assets/dist && mkdir -p cmd/hub/assets/dist && cp -r web/dist/* cmd/hub/assets/dist/
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o deploy/out/svrmon-hub-linux-amd64 ./cmd/hub
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o deploy/out/svrmon-agent-linux-amd64 ./cmd/agent
```

### 2. 部署 Hub（选一台 Ubuntu）

```bash
# 把 deploy/ 目录和二进制拷到服务器，然后：
sudo ./install-hub.sh svrmon-hub-linux-amd64
# 首次启动自动生成 /opt/svrmon/hub.config.json（内含随机 agent token，脚本会打印）
```

浏览器打开 `http://<hub IP>:8080`。

#### 也可以部署到软路由 / NAS 的 Docker（如 N100 小主机）

hub 只有约 20MB 内存占用、单个静态二进制 + 一个 SQLite 文件，软路由 Docker 完全够用：

```bash
# 开发机：deploy/build.ps1 已自动把二进制放进 deploy/docker/
# 软路由上（把整个 deploy/docker 目录拷过去）：
cd docker && docker compose up -d --build

# 取 agent token（首次启动自动生成，持久化在 docker 卷里）：
docker exec svrmon-hub cat /data/hub.config.json

# 查看 hub 日志（同时也会写入 /data/logs/ 按天轮转，保留 7 天）：
docker logs -f svrmon-hub
```

要点：
- 配置/数据库/日志都在 `svrmon-data` 卷的 `/data` 下，升级只需换镜像重建容器，数据不丢
- 软路由 8080 被占用时，改 compose 里的端口映射（如 `"9000:8080"`），agent 的 hubUrl 相应改为 `ws://<路由IP>:9000/agent`
- 存储需求：约 10MB/天/服务器（7 天滚动保留），N100 8G + 常见 SSD 毫无压力
- 路由器重启的窗口期内告警不可用（hub 单点）；恢复后 agent 自动重连，只丢失该窗口的历史数据

### 3. 部署 Agent（每台被监控的 Ubuntu）

```bash
sudo ./install-agent.sh svrmon-agent-linux-amd64 ws://<hub IP>:8080/agent <agent token>
```

可选依赖：
- IPMI 采集：`sudo apt-get install ipmitool openipmi`（需主板带 BMC；无 BMC 自动跳过）
- GPU 采集：NVIDIA 驱动自带 nvidia-smi 即可

**Windows 11 被监控机**：把 `svrmon-agent.exe` 拷到 `deploy\` 后，以管理员身份运行

```powershell
.\install-agent-windows.ps1 -HubUrl ws://<hub IP>:8080/agent -Token <agent token> -Name <显示名>
```

一键完成目录创建、写配置、注册服务（NSSM 优先，否则系统任务计划）、启动与校验；支持 `-NoService` 先试跑、`-Uninstall` 卸载。详见 [docs/DEPLOY.md §2.5](docs/DEPLOY.md)。

### 4. 完成

打开 hub 页面即可看到所有服务器。手动配置时参数见 `deploy/*.config.example.json`，服务管理 `systemctl status hub|agent`，日志在 `/var/log/svrmon/`（按天一个文件，自动保留 7 天，如 `tail -f /var/log/svrmon/agent-*.log`）。

## 配置说明

网页采用按需加载、构建时 gzip 和内容指纹缓存；构建与验证说明见 [docs/PERFORMANCE.md](docs/PERFORMANCE.md)。升级网页只需重新构建并替换 Hub，Agent 无需改动。

**hub.config.json**

| 字段 | 默认 | 说明 |
|---|---|---|
| listen | ":8080" | HTTP/WS 监听地址 |
| dbPath | svrmon.db | SQLite 文件路径 |
| retentionDays | 7 | 历史保留天数（每小时清理） |
| storeEverySec | 10 | 历史落盘粒度（实时推送不受影响） |
| agentTokens | 自动生成 | 允许接入的 agent token 列表 |
| uiToken | ""（关闭） | 非空时网页/API 需带 `?token=` 或 `Authorization: Bearer` |
| logFile | ""（systemd 部署时由 -log 参数指定） | 运行日志文件；设置后按天轮转 |
| logRetentionDays | 7 | 日志保留天数，过期文件自动删除 |

**数据与日志保留**：指标历史（SQLite）、每盘 IO 明细、严重日志（`logs` 表）和运行日志（`/var/log/svrmon/*.log`）都只保留 7 天、过期自动删除，无需人工清理；前者由 hub 每小时清理，后者由日志写入器在启动/日切/每小时三个时机清理。

**严重日志采集**：双后端自动切换——Ubuntu 用 `journalctl -p 3`（err 级以上，覆盖内核崩溃、MCE/EDAC/PCIe AER 硬件错误、服务故障）；**Windows 用系统事件日志**（System 通道 错误/严重级，Get-WinEvent 按 RecordId 增量去重）。按游标增量去重后随快照推给 hub 落库；详情页"日志"标签可按时段回看（实时推送 + 数据库），历史页有"严重日志条数"曲线。IPMI 硬件事件（SEL）仍在 IPMI 标签查看。

**异常关机检测的平台差异**：Ubuntu 依据 wtm（`last -x`）+ boot_id；**Windows 依据 Kernel-Power 事件 41 / EventLog 6008** + 由启动时间派生的 boot 标识——两平台的失联告警与重启后确认邮件行为一致。

**本机配置采集的平台差异**：Ubuntu 用 lscpu/dmidecode/lsblk/lspci；**Windows 用一条 PowerShell/CIM 脚本**（Win32_BaseBoard/ComputerSystemProduct/PhysicalMemory/Get-PhysicalDisk/NetworkAdapter/VideoController），同样产出主板与整机序列号、BIOS、逐条内存明细（类型/速率/厂商/料号）、硬盘型号与 SSD/HDD/接口、物理网卡、GPU（无 nvidia-smi 时用 WMI 显卡信息兜底）。已知平台限制：Windows 上 CPU 温度、磁盘繁忙度%、系统负载无对应数据源，显示为空/0。

**agent.config.json**

| 字段 | 默认 | 说明 |
|---|---|---|
| hubUrl | ws://127.0.0.1:8080/agent | hub 地址 |
| token | change-me | 必须匹配 hub 的 agentTokens |
| name | 主机名 | 展示名（多机重名会互相顶替，请保持唯一） |
| intervalMs | 2000 | 快照推送间隔（最小 500） |
| gpu.enable / gpu.path / gpu.intervalSec | true / nvidia-smi / 5 | 无 NVIDIA 卡可关；nvidia-smi 执行间隔（秒） |
| ipmi.enable / sensorSec / selSec | true / 30 / 60 | 传感器与 SEL 刷新间隔（秒） |
| logs.enable / path / intervalSec / backfillMin | true / journalctl / 30 / 10 | 严重日志采集（err 级以上）、轮询间隔与启动回看时长 |
| hw.enable / intervalMin | true / 30 | 硬件配置清单采集（详情页"本机配置"），刷新间隔（分钟） |

命令行参数 `-hub -token -name -interval` 可覆盖配置文件，便于临时调试。

## 异常关机告警（邮件 / Webhook）

**服务器重命名**：总览卡片或详情页右上角的 ✎ 按钮可修改显示名（agent 上报的真实主机名不变）。新名字持久化在 hub 的 SQLite 中、hub 重启后依然生效，页面、URL、历史查询与**告警邮件/Webhook** 全部使用新名字；详情页信息栏保留"真实主机名"以便对照。

hub 内置告警引擎，判定逻辑（智能模式，正常关机不打扰）：

| 情形 | 结果 |
|---|---|
| systemd 正常关机/重启（agent 收到 SIGTERM 后发告别帧） | 不告警；可选信息邮件（`notifyOnCleanShutdown`） |
| 无告别帧失联，超过观察期（`offlineGraceSec`，默认 60s）未恢复 | 🔴 **告警：疑似异常关机/断电/宕机** |
| 失联后恢复，boot_id 未变（网络闪断） | 🟡 通知：恢复在线，判定为网络中断 |
| 重启后 agent 上报"上次关机无正常记录"（wtmp 证据） | 🔴 **告警：确认异常关机**（含失联/恢复/启动时间线） |
| 同一服务器同一事件 | `cooldownMin`（默认 30 分钟）内不重复 |

邮件通过 SMTP 发送（QQ/163 需在邮箱设置开启 SMTP 并生成**授权码**），Webhook 支持 JSON 通用格式和钉钉/企业微信 text 格式。两者可同时启用、互不影响；发送失败自动重试 3 次。

**hub 配置 `alerts` 块**（完整示例见 `deploy/hub.config.example.json`，已按 QQ 邮箱预填）：

| 字段 | 默认 | 说明 |
|---|---|---|
| enabled | false | 告警总开关 |
| offlineGraceSec | 60 | 失联观察期（秒），期内恢复不告警 |
| cooldownMin | 30 | 同一事件重复告警抑制 |
| notifyOnCleanShutdown | false | 正常关机/重启也发信息邮件 |
| smtp.host / port / security | smtp.qq.com / 465 / auto | security: auto/tls/starttls/none |
| smtp.username / password | — | QQ 邮箱 password 填**授权码** |
| smtp.from / to | — | 发件人显示名 / 收件人列表（可多个） |
| webhook / webhookStyle | "" / json | POST 地址；钉钉/企微 text 机器人填 `text` |

**测试方法**：配置好后 `systemctl stop agent` → 不应有告警；`kill -9 $(pgrep svrmon-agent)` 或直接拔电 → 观察 `offlineGraceSec` 后收到"失联"告警邮件；机器重启后会再收到"确认异常关机"邮件。

## API

| 接口 | 说明 |
|---|---|
| GET /api/servers | 全部服务器 + 最新快照 |
| GET /api/history?server=X&from=&to=&points= | 历史序列（自动按时间桶聚合） |
| POST /api/power {"server","action"} | action: on/off/soft/cycle/reset/status |
| POST /api/rename {"server","name"} | 修改服务器显示名（持久化，告警随之使用新名） |
| GET /api/logs?server=X&from=&to=&limit= | 严重日志明细（最新在前，默认 24h/500 条） |
| WS /agent | agent 接入（?token=） |
| WS /ws | 浏览器实时流（init/snapshot/join/offline 消息） |

## 版本与迭代

当前版本 **v0.2.0**（版本历史见 [CHANGELOG.md](CHANGELOG.md)，**安装/升级/回滚完整步骤见 [docs/DEPLOY.md](docs/DEPLOY.md)**）。

发版四步：

1. 改代码 → `deploy/build.ps1` 构建验证（含前端嵌入与 Linux/Windows 双平台交叉编译）
2. 同步三处版本号：`internal/hub/hub.go` 的 `HubVersion`、`internal/agent/client.go` 的 `Version`、`web/package.json` 的 `version`
3. 更新 `CHANGELOG.md`
4. 提交推送：

```bash
git add -A && git commit -m "v0.1.1: ..." && git push
```

部署侧升级用一键脚本（自动备份 + 校验 + 回滚提示），配置与数据库不受影响：

```bash
sudo ./upgrade-hub.sh svrmon-hub-linux-amd64      # 中心机
sudo ./upgrade-agent.sh svrmon-agent-linux-amd64  # 各被监控机（保留原配置）
```

Docker 部署则替换二进制后 `docker compose up -d --build`；数据库结构变更由 hub 启动时自动迁移。

## 安全建议

- 仅在**局域网**使用；不要把 hub 端口暴露到公网。需要远程访问请走 VPN 或加反向代理 + HTTPS。
- `agentTokens` 只用于 hub 验证 agent；`uiToken` 设置后网页与 API 都需要携带。
- agent 配置文件含 token，权限应为 600（安装脚本已处理）。
- 电源控制是真实操作（硬断电会丢数据），页面上有确认弹窗，请谨慎授权 uiToken。

## 常见问题

- **IPMI 显示"不可用"**：未装 ipmitool（`apt-get install ipmitool`）、主板无 BMC、或 `/dev/ipmi0` 权限不足（agent 以 root 运行，或将用户加入 ipmi 组并 `modprobe ipmi_devintf`）。
- **GPU 不显示**：确认 `nvidia-smi` 命令可用；部分虚拟化环境需 `nvidia-smi` 可见设备。
- **Windows 上部分指标为空**：CPU 温度、磁盘繁忙度 %、系统负载在 Windows 上无数据源（显示空或 0）；磁盘读写速率/IOPS、每盘明细、GPU、事件日志、硬件配置均正常。
- **Windows 服务注册**：用 `deploy\install-agent-windows.ps1`（管理员运行）一键完成；停止任务计划属于强杀进程，维护性停机超过 60 秒会收到一封"失联"告警，需优雅停止请用 NSSM 方式。
- **历史图表为空**：hub 每 storeEverySec 秒落一次盘，刚部署需等待片刻；确认服务器系统时间正常。
- **agent 反复重连**：检查 token 与 hubUrl；`tail -f /var/log/svrmon/agent-*.log`（Ubuntu）或 `Get-Content "$env:ProgramData\svrmon\logs\agent.log" -Wait`（Windows）看具体报错。

## 扩展方向

- 阈值告警（温度/占用/离线 → Webhook/邮件）
- Redfish 支持（远程 BMC，无需每台装 agent 也能采 IPMI）
- AMD GPU（rocm-smi）、磁盘 SMART、进程 Top
- 多用户权限、操作审计日志

---
构建产物仅供内部使用；hub 二进制内嵌前端，升级时重新运行 build.ps1 并替换 /opt/svrmon 下的文件后 `systemctl restart hub`。
