# svrmon 部署与升级手册

本文档覆盖**首次安装**、**版本升级**、**回滚**与**日常运维**。功能说明与架构见 [README](../README.md)，版本历史见 [CHANGELOG](../CHANGELOG.md)。

---

## 1. 部署拓扑

```
┌─ 被监控服务器 ×N ────────────────┐
│  agent（单二进制，本地采集）      │
│   CPU/内存/磁盘/网络/GPU/IPMI/日志 │
│   只做出站 WebSocket 连接，无监听端口 │
└──────────────┬───────────────────┘
               │ ws://<hub>:8080/agent
               ▼
      ┌─ Hub（任一台 Ubuntu / 软路由 Docker / Windows）─┐
      │  单二进制 + SQLite + 内嵌前端                    │
      │  监听 :8080（唯一需要开放的端口）                 │
      └──────────────┬───────────────────────────────────┘
                     │ http://<hub>:8080
                     ▼
                  浏览器
```

**要点**：只有 Hub 需要监听端口；agent 无入站端口。Hub 与 agent 可分别升级。

---

## 2. 首次安装

### 2.1 在开发机构建

```powershell
pwsh -File deploy\build.ps1          # 构建前端 + 交叉编译（需 Go 与 Node/pnpm）
```
产物位于 `deploy/out/`：

| 文件 | 用途 |
|---|---|
| `svrmon-hub-linux-amd64` | Ubuntu/软路由 Hub |
| `svrmon-agent-linux-amd64` | Ubuntu Agent |
| `svrmon-hub.exe` / `svrmon-agent.exe` | Windows 11 |
| `deploy/docker/` | Docker 构建上下文（脚本已自动放入 hub 二进制） |

### 2.2 Hub 安装（Ubuntu，systemd）

```bash
# 把 deploy/ 整个目录 + 二进制拷到服务器
sudo ./install-hub.sh svrmon-hub-linux-amd64
```

脚本会：装二进制到 `/opt/svrmon/hub` → 创建 `svrmon` 系统用户 → 安装 `hub.service` → 首次启动自动生成
`/opt/svrmon/hub.config.json`（含随机 agent token）→ 打印 token 与访问地址。

放行端口（如启用 ufw）：

```bash
sudo ufw allow from 192.168.1.0/24 to any port 8080 proto tcp
```

浏览器访问 `http://<hub IP>:8080`。

### 2.3 Hub 安装（软路由 / NAS 的 Docker，如 N100 小主机）

hub 常驻约 20MB 内存，x86_64 软路由完全够用：

```bash
cd deploy/docker
docker compose up -d --build

docker exec svrmon-hub cat /data/hub.config.json   # 取 agent token
docker logs -f svrmon-hub                          # 查看日志
```

- 数据（配置/数据库/日志）都在命名卷 `svrmon-data` 的 `/data` 目录，**升级换镜像不丢数据**
- 8080 被占用时改 `docker-compose.yml` 的端口映射（如 `"9000:8080"`），agent 的 hubUrl 同步改端口
- 存储占用约 10MB/天/服务器（7 天滚动）

### 2.4 Agent 安装（Ubuntu）

```bash
# 把 deploy/ + 二进制拷到被监控机
sudo ./install-agent.sh svrmon-agent-linux-amd64 ws://<hub IP>:8080/agent <agent token>
```

可选依赖（缺失时对应模块自动跳过，不影响其他指标）：

```bash
sudo apt-get install -y ipmitool openipmi   # IPMI 采集（需主板带 BMC）
# GPU 采集：NVIDIA 驱动自带 nvidia-smi 即可
```

### 2.5 Windows 11 安装（Hub 或 Agent 均可）

Windows 无 systemd，用 **NSSM** 注册为服务（推荐，支持崩溃自动拉起与优雅停止）：

```powershell
# 1) 建目录并放入二进制与配置
mkdir D:\svrmon\logs
copy svrmon-agent.exe D:\svrmon\

# 2) 写配置（内容参考 deploy\agent.config.example.json，至少填 hubUrl / token / name）
notepad D:\svrmon\agent.config.json

# 3) 注册服务
nssm install svrmon-agent D:\svrmon\svrmon-agent.exe
nssm set svrmon-agent AppDirectory D:\svrmon
nssm set svrmon-agent AppParameters "-config D:\svrmon\agent.config.json -log D:\svrmon\logs\agent.log"
nssm set svrmon-agent Start SERVICE_AUTO_START
nssm start svrmon-agent
```

也可用系统自带任务计划（无自动拉起）：

```powershell
schtasks /create /tn svrmon-agent /sc onstart /ru SYSTEM ^
  /tr "D:\svrmon\svrmon-agent.exe -config D:\svrmon\agent.config.json -log D:\svrmon\logs\agent.log"
```

**Windows 注意**：
- **需管理员权限运行**（WMI 硬件信息、系统事件日志读取）
- NSSM 停止服务时发送 Ctrl+C，agent 会发"告别帧"，因此正常停止**不会**触发异常关机告警
- 事件日志采集用 `Get-WinEvent`（System 通道 错误/严重级），无需额外配置
- 异常关机检测依据事件 41/6008，行为与 Ubuntu 端一致

---

## 3. 配置速查

| 部署方式 | 配置文件 | 数据库 | 日志 |
|---|---|---|---|
| Ubuntu Hub | `/opt/svrmon/hub.config.json` | `/opt/svrmon/svrmon.db` | `/var/log/svrmon/hub-YYYYMMDD.log` |
| Ubuntu Agent | `/opt/svrmon/agent.config.json`（权限 600） | 无 | `/var/log/svrmon/agent-YYYYMMDD.log` |
| Docker Hub | 卷内 `/data/hub.config.json` | `/data/svrmon.db` | `/data/logs/hub-*.log` + `docker logs` |
| Windows | 指定路径的 `*.config.json` | Hub 同目录 `svrmon.db` | `-log` 指定的目录 |

常用字段（完整示例见 `deploy/*.config.example.json`）：

- **Hub**：`listen`（端口）、`agentTokens`、`uiToken`（网页访问令牌）、`retentionDays`、`storeEverySec`、`alerts`（邮件/Webhook 告警）
- **Agent**：`hubUrl`、`token`、`name`、`intervalMs`、`gpu.intervalSec`、`ipmi.sensorSec`、`logs.intervalSec`、`hw.intervalMin`

> 日志文件按天轮转、**保留 7 天**自动清理；指标历史、每盘明细、严重日志同样 7 天滚动。

---

## 4. 版本升级

### 4.1 升级前检查清单

- [ ] 开发机上 `pwsh -File deploy\build.ps1` 构建成功，产物在 `deploy/out/`
- [ ] 记录当前版本（网页顶栏 `vX.Y.Z` 或 `docker exec svrmon-hub cat /data/hub.config.json` 无关；以日志/页面为准）
- [ ] 确认磁盘空间足够（备份占用约等于数据库大小）
- [ ] 阅读 [CHANGELOG](../CHANGELOG.md) 了解本版本变化与是否需要改配置

### 4.2 Hub 升级（Ubuntu）

**方式 A：一键脚本（推荐，自动备份 + 校验 + 回滚提示）**

```bash
sudo ./upgrade-hub.sh svrmon-hub-linux-amd64
```

脚本流程：停 hub → 备份旧二进制/配置/数据库到 `/opt/svrmon/backup/<时间戳>/` → 替换二进制 → 启动 → 校验；
失败时打印回滚命令；自动保留最近 5 份备份。

**方式 B：手动三步**

```bash
sudo systemctl stop hub
sudo cp /opt/svrmon/hub /opt/svrmon/hub.bak        # 可选备份
sudo cp /opt/svrmon/svrmon.db /opt/svrmon/svrmon.db.bak   # 可选备份数据库
sudo install -m 755 svrmon-hub-linux-amd64 /opt/svrmon/hub
sudo chown svrmon:svrmon /opt/svrmon/hub
sudo systemctl start hub
systemctl status hub --no-pager
```

**前端也随 hub 一起升级**（前端构建产物内嵌在 hub 二进制里），浏览器请 **Ctrl+F5 强刷**一次。

### 4.3 Agent 升级（Ubuntu）

```bash
sudo ./upgrade-agent.sh svrmon-agent-linux-amd64     # 一键（保留原配置文件的 token/hubUrl）
```

手动方式：停 agent → 替换 `/opt/svrmon/agent` → 启动。**配置文件不要动**，升级后自动沿用。

```bash
sudo systemctl stop agent && sudo cp /opt/svrmon/agent /opt/svrmon/agent.bak
sudo install -m 755 svrmon-agent-linux-amd64 /opt/svrmon/agent
sudo systemctl start agent && systemctl status agent --no-pager
```

### 4.4 Docker 升级（软路由 / NAS）

```bash
# 开发机重新构建后，把新的 deploy/docker/svrmon-hub-linux-amd64 拷到软路由
cd deploy/docker
docker compose up -d --build      # 重建容器，数据卷保留
docker exec svrmon-hub ls -l /data # 确认配置与数据库仍在
```

回滚：把上一版二进制放回 `deploy/docker/`，重新 `docker compose up -d --build`。

### 4.5 Windows 升级

```powershell
nssm stop svrmon-agent
copy /Y svrmon-agent.exe D:\svrmon\agent-new.exe
move /Y D:\svrmon\svrmon-agent.exe D:\svrmon\svrmon-agent.bak
move /Y D:\svrmon\agent-new.exe D:\svrmon\svrmon-agent.exe
nssm start svrmon-agent
```

### 4.6 回滚

| 场景 | 操作 |
|---|---|
| Hub（脚本升级） | 按脚本结束时打印的命令：停服务 → 从 `/opt/svrmon/backup/<时间戳>/` 拷回 `hub` 与 `svrmon.db*` → 启动 |
| Hub（手动升级） | `systemctl stop hub && cp /opt/svrmon/hub.bak /opt/svrmon/hub && cp /opt/svrmon/svrmon.db.bak /opt/svrmon/svrmon.db && systemctl start hub` |
| Agent | `systemctl stop agent && cp /opt/svrmon/agent.bak /opt/svrmon/agent && systemctl start agent` |
| Docker | 换回旧二进制重建容器（数据卷不动） |

### 4.7 数据库结构与兼容性

- **结构变更自动迁移**：hub 启动时自动建表/加列，日志可见 `store: migrated samples: added column ...`，无需手工执行 SQL
- **升级不丢历史**：只做加列/加表，不删列；升级前按 4.1 备份数据库最稳妥
- **新旧混跑**：Hub 与 agent 可分别升级——新增的 WS 消息类型与字段都是可选的，旧端点会忽略未知消息、新端点对缺失字段降级。
  但**跨版本使用新功能时必须两端都升级**（例如 Windows 事件日志采集、日志/本机配置标签页），因此建议一并升级

---

## 5. 日常运维

```bash
# 服务管理
systemctl status hub|agent          # 状态
systemctl restart hub|agent         # 重启（改配置后）
systemctl stop hub|agent            # 停止（agent 会发告别帧，不触发误告警）

# 日志（按天轮转，保留 7 天）
tail -f /var/log/svrmon/agent-$(date +%Y%m%d).log
ls /var/log/svrmon/

# 查看/更换 agent token
cat /opt/svrmon/hub.config.json          # agentTokens 数组
# 修改后：systemctl restart hub，并同步更新各 agent 的 token

# 修改采集频率（例：GPU 10 秒、IPMI 60 秒）
vi /opt/svrmon/agent.config.json
systemctl restart agent

# 网页访问令牌（可选，软路由等全局域网可达场景建议开启）
# 在 hub.config.json 设置 "uiToken": "xxx"，重启后访问 http://<hub>:8080/?token=xxx
```

**卸载**：

```bash
systemctl disable --now hub agent
rm -f /etc/systemd/system/hub.service /etc/systemd/system/agent.service
systemctl daemon-reload
rm -rf /opt/svrmon /var/log/svrmon
# Docker: docker compose down -v （-v 会删除数据卷）
```

---

## 6. 发版流程（开发者）

1. 改代码 → `pwsh -File deploy\build.ps1` 构建验证（含前端嵌入与双平台交叉编译）
2. 同步**三处版本号**：

   | 文件 | 变量 | 显示位置 |
   |---|---|---|
   | [internal/hub/hub.go](../internal/hub/hub.go) | `HubVersion` | 启动日志 + 网页顶栏 |
   | [internal/agent/client.go](../internal/agent/client.go) | `Version` | 详情页 agent 版本 |
   | [web/package.json](../web/package.json) | `version` | 前端包版本 |

3. 更新 [CHANGELOG.md](../CHANGELOG.md)
4. 提交推送：

   ```bash
   git add -A && git commit -m "v0.1.1: ..." && git push
   ```

5. 通知各机器按本文第 4 节升级（Hub 与 agent 建议一起升，顺序上**先升 Hub 再升 agent** 最稳）
