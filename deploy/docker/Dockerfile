# svrmon hub 镜像（软路由 / NAS / 任意 x86_64 或 arm64 Linux）
# 构建前先把 deploy/out/svrmon-hub-linux-amd64 复制到本目录。
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
COPY svrmon-hub-linux-amd64 /usr/local/bin/svrmon-hub
RUN chmod +x /usr/local/bin/svrmon-hub

ENV TZ=Asia/Shanghai
VOLUME /data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:8080/api/servers >/dev/null || exit 1

# 首次启动自动生成 /data/hub.config.json（含随机 agent token，持久化在卷里）
ENTRYPOINT ["/usr/local/bin/svrmon-hub", \
  "-config", "/data/hub.config.json", \
  "-db",     "/data/svrmon.db", \
  "-log",    "/data/logs/hub.log"]
