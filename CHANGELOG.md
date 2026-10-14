# 更新日志

本项目版本号在三个位置维护，发版时需同步：

| 位置 | 变量 |
|---|---|
| [internal/hub/hub.go](internal/hub/hub.go) | `HubVersion`（启动日志 + 网页顶栏显示） |
| [internal/agent/client.go](internal/agent/client.go) | `Version`（随 hello 上报，详情页显示） |
| [web/package.json](web/package.json) | `version` |

---

## v0.2.0 — 2026-10-14

### 性能优化（网页加载）
- **主脚本从 1,167KB 降至 40KB（↓96.6%）**：总览/详情/历史按需加载（路由懒加载），ECharts 按需引入
  （仅折线图/坐标/提示/图例/Canvas 渲染），构建时生成 gzip 静态资源。
- Hub 静态文件服务增强（[internal/hub/static.go](internal/hub/static.go)）：内存缓存 + 预构建 gzip 压缩传输
  + ETag 条件请求 + 带内容指纹资源一年 immutable 缓存 + HTML no-cache 重校验。
- 详情页只挂载当前标签（`v-show` → `v-if`），历史数据延迟查询，关闭实时曲线动画，ECharts `lazyUpdate`。

### 修复
- **启用 UI token 后静态资源与 WebSocket 无法认证**：验证 `?token=` 链接后设置 HttpOnly、SameSite=Strict
  的会话 cookie，使静态资源、REST 与 WebSocket 正常通过认证（[internal/hub/api.go](internal/hub/api.go)）。
- **新建克隆后无法编译 / 前端白屏**：
  - `cmd/hub/assets/dist/index.html` 之前跟踪的是**构建产物**（引用 `/assets/index-*.js`），而被引用的 JS/CSS 不入库
    → 新克隆直接 `go build` 会内嵌一个指向不存在资源的页面（白屏）。现在仓库只跟踪 `.gitkeep`，
    `index.html` 与 `assets/` 均为构建产物；`build.ps1` 统一嵌入逻辑（`-SkipWeb` 时优先复用已有 `web/dist`，
    否则写占位页），保证「index.html 与其引用资源始终同时存在」。
  - `build.ps1` 替换 `assets/dist` 目录时会连 `.gitkeep` 一起删除，导致该目录无法入库
    → `go:embed all:assets` 报 `no matching files found`，新克隆**完全无法编译**。现在构建后自动补回。
  - `cmd/hub/main.go`：二进制未内嵌前端时，改用内置提示页（说明需先构建前端），避免源码运行时 404 或白屏。
- 已实测：新克隆（仅 `.gitkeep`）可直接 `go build` 通过；无前端二进制启动后页面返回 200 提示页、API 正常。

### 新增
- 新增静态资源 HTTP 回归测试（[internal/hub/static_test.go](internal/hub/static_test.go)）
  与 [docs/PERFORMANCE.md](docs/PERFORMANCE.md) 构建、缓存与性能说明。
- **Windows 一键安装脚本** `deploy/install-agent-windows.ps1`：自动完成安装目录创建、二进制复制、配置写入、服务注册
  （**NSSM 优先**，缺失时退回**系统自带任务计划**，含 SYSTEM 账户、开机自启、崩溃 1 分钟自动重启）、启动与校验。
  支持 `-NoService`（无需管理员，先前台试跑）、`-ServiceType`、`-NssmPath`、`-Uninstall [-Purge]`。
  已在本机实测：脚本生成的配置可正常连接 hub（hub 出现该机器、日志按 7 天轮转）、权限检查与卸载路径均正确。

### 文档
- [docs/DEPLOY.md](docs/DEPLOY.md) §2.5 Windows 章节重写：一键脚本（推荐）/ 手动 NSSM / 手动任务计划三种方式对比表、
  参数说明、前台试跑步骤、验证与停止/卸载命令、注意事项（管理员权限、仅出站无需开防火墙、
  任务计划强杀 vs NSSM 优雅停止的告警差异、平台限制项）
- 新增 §5.2 Windows 日常运维（进程/服务状态、日志跟踪、改配置重启、卸载）
- §4.5 Windows 升级补全（保留配置的二进制替换流程 + 脚本重装方式）、§4.6 回滚表增加 Windows 行
- README：快速开始增加 Windows 一键安装指引；修正"Windows 磁盘 IO 为空"这条已过时的 FAQ

---

## v0.1.1 — 2026-10-14

### 修复
- **Hub：agent 连接每 120 秒被强制断开**（[internal/hub/hub.go](internal/hub/hub.go)）。原因是连接建立时设置了一次性读超时，
  而续期只写在 `SetPongHandler` 里——但 hub 从不主动发 ping，因此永远收不到 pong，该处理函数从未执行；
  即使 agent 一直正常上报数据，连接也在 120 秒后被关闭（日志中表现为每 120 秒一次的断开/重连循环，
  每次约 2 秒空窗、丢失少量快照）。现在**每收到一帧（快照/配置/命令结果/ping）即刷新读超时**，
  同时保留空闲超时语义：agent 真正静默 120 秒仍会被识别为失联。
  agent 端无需修改（agent 的读超时由 hub 的默认 ping 应答维持，本来正常）。

### 文档
- 新增 [docs/DEPLOY.md](docs/DEPLOY.md)：**部署与升级手册**（Ubuntu / Docker 软路由 / Windows 11 安装步骤、升级前检查清单、回滚、日常运维、发版流程）
- 新增 `deploy/upgrade-hub.sh` 与 `deploy/upgrade-agent.sh`：**一键升级脚本**——停服务 → 备份二进制/配置/数据库（保留最近 5 份）→ 替换 → 启动 → 校验，失败时打印回滚命令
- README 的「版本与迭代」章节改为指向手册与升级脚本

### 升级说明
- **只需升级 Hub**（本次修复在 hub 端）；agent 保持 v0.1.0 亦可正常工作
- 无数据库结构变更，升级不需要备份数据库（仍建议按 [docs/DEPLOY.md](docs/DEPLOY.md) §4.2 走一键脚本）

---

## v0.1.0 — 初始版本

**架构**：中心 Hub（Go 单二进制，内嵌前端 + SQLite）+ 各服务器 Agent（Go 单二进制，本地采集后 WebSocket 出站推送）+ Vue3/ECharts 网页端。支持 Ubuntu（systemd）与 Windows 11（NSSM/任务计划）双平台，Hub 可部署到软路由/NAS 的 Docker（amd64）。

### 监控能力
- **CPU**：总/每核利用率、iowait、温度（Ubuntu）、负载、运行时间
- **内存**：已用/可用/缓存/buffer、swap
- **磁盘**：每块盘读写吞吐、IOPS、繁忙度（含 md/LVM 卷，排除 loop/zram 伪设备）+ 各挂载点容量；历史图带**窗口峰值线**（突发不被平均抹掉）
- **网络**：各网卡上下行吞吐、错误/丢包
- **GPU**：nvidia-smi 采集 利用率/显存/温度/风扇/功耗/频率/P-State/ECC/占用进程（独立刷新间隔，默认 5s）
- **IPMI**：本地 ipmitool 传感器（温度/风扇/电压/功率）、SEL 事件日志、整机功耗（默认 30s 刷新）
- **电源控制**：开机/软关机/硬断电/硬重启/断电循环（页面二次确认 + 审计日志）

### 日志与告警
- **严重日志采集**：双后端自动切换——Ubuntu `journalctl -p 3`、Windows 系统事件日志（错误/严重级），增量去重入库
- **异常关机告警**：告别帧 + 启动证据（Ubuntu wtmp/boot_id、Windows 事件 41/6008），智能判定——正常关机不打扰、失联超时告警、重启后确认异常
- **通知渠道**：SMTP 邮件（QQ/163/Gmail 等，含重试与节流）+ Webhook（钉钉/企业微信 text 机器人）
- **数据保留**：指标历史、每盘明细、严重日志、运行日志统一 **7 天自动清理**

### 配置与运维
- **本机配置清单**：CPU/内存条/硬盘/网卡/GPU 型号明细、主板与整机序列号、BIOS/BMC 版本（Ubuntu 用 lscpu/dmidecode/lsblk/lspci，Windows 用 WMI），30 分钟刷新
- **服务器重命名**：网页 ✎ 修改显示名，持久化，告警随新名发送
- **部署**：一键构建脚本（交叉编译 linux/windows）、systemd 单元、安装脚本、Dockerfile + docker-compose（软路由）、运行日志按天轮转
- **安全**：agent token 认证、可选 UI token、局域网部署
