# 网页加载与渲染优化

## 问题与结果

原先总览入口同时导入详情页、历史组件和完整 ECharts 库，首次打开总览也需要下载约 1.17 MB 的 JavaScript；静态文件未设置压缩或缓存。详情页通过 `v-show` 隐藏其他标签，仍会初始化历史图表并发起历史查询。

调整后，总览入口约 102 KB，gzip 传输约 40 KB；详情代码约 508 KB，gzip 约 170 KB，仅在打开单机详情时加载；历史组件约 6 KB，gzip 约 2 KB，仅在打开历史标签时加载。具体大小会随依赖版本和代码变化。

## 实现

- Vue Router 对详情页使用动态导入，历史组件使用异步组件。
- ECharts 通过 `echarts/core` 按需注册折线图、坐标、提示、图例、轴指示器和 Canvas 渲染器。
- 详情页只挂载当前标签，切换标签后实时数据仍保留在全局 ring 缓冲区。
- 图表关闭动画，并通过浅监听和 `lazyUpdate` 减少重复遍历与重绘。
- Vite 构建完成后为 HTML、JS、CSS 和 SVG 生成 `.gz` 文件，无需新增依赖。
- Hub 启动时缓存嵌入的静态资源，并按 `Accept-Encoding` 提供 gzip 版本。
- `/assets/` 下的内容指纹资源使用 `public, max-age=31536000, immutable`；HTML 使用 `no-cache` 并通过 ETag 重新校验。
- gzip 和原始响应采用各自的 ETag，并设置 `Vary: Accept-Encoding`。
- 缺失的静态资源返回 404，普通页面路径继续回退至 `index.html`。

## 构建

沿用原有构建流程，无需修改部署配置。应复制完整 `web/dist`，包括生成的 `.gz` 文件。

```powershell
# 在仓库根目录执行，需 Go 与 Node/pnpm
pwsh -File deploy/build.ps1
```

Linux/macOS：

```bash
(cd web && pnpm install --frozen-lockfile && pnpm run build)
mkdir -p cmd/hub/assets/dist
cp -r web/dist/. cmd/hub/assets/dist/
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o deploy/out/svrmon-hub-linux-amd64 ./cmd/hub
```

构建文件嵌入 Hub 可执行文件；升级后刷新浏览器获取新的 HTML 入口即可。Agent 不需要随这些网页改动升级。

## 验证

```bash
go test ./internal/hub
go vet ./cmd/hub ./internal/hub
```

静态资源测试覆盖 gzip 内容一致性、响应类型、ETag/304、HTML 重新校验、HEAD、缺失资源及 `gzip;q=0`。上线后可检查响应的 `Content-Encoding: gzip` 和 `Cache-Control`，并确认总览不会提前请求详情和历史资源。

## 同步修复

Hub 收到 Agent 消息后刷新 120 秒读取超时，避免持续上报的正常连接被误断开。启用可选 UI token 时，认证链接设置 HttpOnly、SameSite=Strict 的会话 cookie，使页面后续的静态资源、API 与 WebSocket 请求能继续认证；HTTPS 下同时设置 Secure。
