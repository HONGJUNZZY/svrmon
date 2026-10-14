#!/usr/bin/env bash
# svrmon Agent 升级脚本（Ubuntu）
#   - 停服务 → 备份旧二进制/配置 → 替换 → 启动 → 校验
#   - 配置文件不会被覆盖（保留 token / hubUrl / 自定义间隔）
#
# 用法（root）：
#   ./upgrade-agent.sh svrmon-agent-linux-amd64
set -euo pipefail

BIN="${1:-svrmon-agent-linux-amd64}"
[ -f "$BIN" ] || { echo "找不到 $BIN（先在开发机运行 deploy/build.ps1）"; exit 1; }
[ "$(id -u)" -eq 0 ] || { echo "需要 root 运行"; exit 1; }
[ -x /opt/svrmon/agent ] || { echo "/opt/svrmon/agent 不存在，请先用 install-agent.sh 安装"; exit 1; }

TS="$(date +%Y%m%d-%H%M%S)"
BK="/opt/svrmon/backup/$TS"
install -d -m 755 "$BK"

echo "==> 停止 agent"
systemctl stop agent

echo "==> 备份到 $BK"
cp -a /opt/svrmon/agent "$BK/agent"
[ -f /opt/svrmon/agent.config.json ] && cp -a /opt/svrmon/agent.config.json "$BK/"

echo "==> 替换二进制（配置文件保持不变）"
install -m 755 "$BIN" /opt/svrmon/agent

echo "==> 启动 agent"
systemctl start agent

sleep 2
if systemctl is-active --quiet agent; then
    echo "==> 升级完成，agent 运行中"
    ls -t /var/log/svrmon/agent-*.log 2>/dev/null | head -1 | xargs -r tail -6
else
    echo "!! agent 启动失败，请执行回滚："
    echo "   cp $BK/agent /opt/svrmon/agent && systemctl start agent"
    systemctl status agent --no-pager | tail -15 || true
    exit 1
fi

echo
echo "回滚命令（如升级后异常）："
echo "  systemctl stop agent && cp $BK/agent /opt/svrmon/agent && systemctl start agent"
echo "备份目录：$BK"

# 只保留最近 5 份备份
ls -1dt /opt/svrmon/backup/*/ 2>/dev/null | tail -n +6 | xargs -r rm -rf
