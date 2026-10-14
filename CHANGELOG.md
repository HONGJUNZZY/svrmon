# 更新日志

本项目版本号在三个位置维护，发版时需同步：

| 位置 | 变量 |
|---|---|
| [internal/hub/hub.go](internal/hub/hub.go) | `HubVersion`（启动日志 + 网页顶栏显示） |
| [internal/agent/client.go](internal/agent/client.go) | `Version`（随 hello 上报，详情页显示） |
| [web/package.json](web/package.json) | `version` |

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
