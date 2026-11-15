# 项目约定

- 全局规则：`~/AGENTS.md`。接续说明：`agent-memory/index.md`。
- 使用 MyGo 的 `ui.View` 构建原生窗口，探测引擎使用 Go。
- 网络和图像解码在后台执行。应用状态经 `Window.Update` 更新。
- 指定 IP 时保留 TLS 验证、图片域名和 Host，拒绝非公网候选地址。
- 验证：`CGO_ENABLED=0 go test ./...`，MyGo vet 和 macOS 构建命令见 [README.md](README.md)。
