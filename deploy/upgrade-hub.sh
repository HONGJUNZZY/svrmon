#!/usr/bin/env bash
# svrmon Hub 升级脚本（Ubuntu）
#   - 停服务 → 备份旧二进制/配置/数据库 → 替换 → 启动 → 校验
#   - 失败自动提示回滚命令；备份保留最近 5 份
#
# 用法（root）：
#   ./upgrade-hub.sh svrmon-hub-linux-amd64
set -euo pipefail

BIN="${1:-svrmon-hub-linux-amd64}"
[ -f "$BIN" ] || { echo "找不到 $BIN（先在开发机运行 deploy/build.ps1）"; exit 1; }
[ "$(id -u)" -eq 0 ] || { echo "需要 root 运行"; exit 1; }
[ -x /opt/svrmon/hub ] || { echo "/opt/svrmon/hub 不存在，请先用 install-hub.sh 安装"; exit 1; }

TS="$(date +%Y%m%d-%H%M%S)"
BK="/opt/svrmon/backup/$TS"
install -d -m 755 "$BK"

echo "==> 停止 hub（保证数据库备份一致）"
if systemctl is-active --quiet hub; then
    systemctl stop hub
fi

echo "==> 备份到 $BK"
cp -a /opt/svrmon/hub "$BK/hub"
[ -f /opt/svrmon/hub.config.json ] && cp -a /opt/svrmon/hub.config.json "$BK/"
for f in svrmon.db svrmon.db-wal svrmon.db-shm; do
    if [ -f "/opt/svrmon/$f" ]; then
        cp -a "/opt/svrmon/$f" "$BK/"
    fi
done

echo "==> 替换二进制"
install -m 755 "$BIN" /opt/svrmon/hub
chown -R svrmon:svrmon /opt/svrmon

echo "==> 启动 hub"
systemctl start hub

sleep 2
if systemctl is-active --quiet hub; then
    echo "==> 升级完成，hub 运行中"
    ls -t /var/log/svrmon/hub-*.log 2>/dev/null | head -1 | xargs -r tail -6
else
    echo "!! hub 启动失败，请执行回滚："
    echo "   cp $BK/hub /opt/svrmon/hub && cp $BK/svrmon.db* /opt/svrmon/ && systemctl start hub"
    systemctl status hub --no-pager | tail -15 || true
    exit 1
fi

echo
echo "回滚命令（如升级后异常）："
echo "  systemctl stop hub && cp $BK/hub /opt/svrmon/hub && cp $BK/svrmon.db* /opt/svrmon/ 2>/dev/null; systemctl start hub"
echo "备份目录：$BK"

# 只保留最近 5 份备份
ls -1dt /opt/svrmon/backup/*/ 2>/dev/null | tail -n +6 | xargs -r rm -rf
