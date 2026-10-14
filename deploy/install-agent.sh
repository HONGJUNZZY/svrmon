#!/usr/bin/env bash
# 在每台被监控的 Ubuntu 服务器上以 root 运行：
#   ./install-agent.sh svrmon-agent-linux-amd64 ws://192.168.1.10:8080/agent <token>
set -e
BIN="${1:-svrmon-agent-linux-amd64}"
HUB_URL="${2:?用法: install-agent.sh <agent二进制> <hub ws url> <token>}"
TOKEN="${3:?缺少 token}"
[ -f "$BIN" ] || { echo "找不到 $BIN"; exit 1; }

install -d -m 755 /opt/svrmon
install -m 755 "$BIN" /opt/svrmon/agent

NAME="${4:-$(hostname)}"
cat > /opt/svrmon/agent.config.json <<EOF
{
  "hubUrl": "$HUB_URL",
  "token": "$TOKEN",
  "name": "$NAME",
  "intervalMs": 2000,
  "gpu": { "enable": true, "path": "nvidia-smi", "intervalSec": 5 },
  "ipmi": { "enable": true, "path": "ipmitool", "sensorSec": 30, "selSec": 60 },
  "logs": { "enable": true, "path": "journalctl", "intervalSec": 30, "backfillMin": 10 },
  "hw": { "enable": true, "intervalMin": 30 }
}
EOF
chmod 600 /opt/svrmon/agent.config.json

# IPMI 本地采集依赖（可选，失败不影响其他指标）
command -v ipmitool >/dev/null 2>&1 || apt-get install -y ipmitool >/dev/null 2>&1 || \
  echo "提示: 未安装 ipmitool，IPMI 采集将被跳过 (apt-get install ipmitool openipmi)"

install -m 644 "$(dirname "$0")/agent.service" /etc/systemd/system/agent.service
systemctl daemon-reload
systemctl enable --now agent
echo "agent 已启动 (name=$NAME hub=$HUB_URL)"
ls -t /var/log/svrmon/agent-*.log 2>/dev/null | head -1 | xargs -r tail -5
