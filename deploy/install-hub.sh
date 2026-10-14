#!/usr/bin/env bash
# 在中心服务器（Ubuntu）上以 root 运行：
#   ./install-hub.sh svrmon-hub-linux-amd64
set -e
BIN="${1:-svrmon-hub-linux-amd64}"
[ -f "$BIN" ] || { echo "找不到 $BIN（先在开发机运行 build.ps1）"; exit 1; }

install -d -m 755 /opt/svrmon
install -m 755 "$BIN" /opt/svrmon/hub

id svrmon >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin svrmon
chown -R svrmon:svrmon /opt/svrmon

install -m 644 "$(dirname "$0")/hub.service" /etc/systemd/system/hub.service
systemctl daemon-reload
systemctl enable --now hub

sleep 1
TOKEN=$(python3 -c "import json;print(json.load(open('/opt/svrmon/hub.config.json'))['agentTokens'][0])" 2>/dev/null || true)
chown svrmon:svrmon /opt/svrmon/hub.config.json
echo "hub 已启动: http://$(hostname -I | awk '{print $1}'):8080"
[ -n "$TOKEN" ] && echo "agent token: $TOKEN  ← 部署 agent 时使用"
