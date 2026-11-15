# 微博图片恢复

这是一个使用 MyGo NativeUI 的桌面应用，通过多个 CDN 节点寻找仍可访问的微博图片版本。

## 打包

### 在 macOS 上打包

使用 Go 1.27.4 和完整 Xcode。在项目根目录执行以下命令，可同时生成 Apple Silicon 的 macOS 包和 x64 的 Windows 应用。需要 Windows 安装程序时，先运行 `brew install makensis`。

```sh
project_dir="$PWD"
go mod download github.com/egoist/mygo
mygo_dir=$(go list -m -f '{{.Dir}}' github.com/egoist/mygo)
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
  go -C "$mygo_dir" run ./cmd/mygo build -platform darwin/arm64,windows/amd64 "$project_dir"
```

只打包一个平台时，保留对应的 `-platform` 值即可。加 `-skip-dmg` 可仅生成 macOS 应用包。若 Xcode 安装在其他位置，请修改 `DEVELOPER_DIR`。

### 在 Windows 上打包

在项目根目录打开 PowerShell，使用 Go 1.27.4。MyGo 会在需要安装程序且本机缺少 NSIS 时自动下载 NSIS。

```powershell
$projectDir = (Get-Location).Path
go mod download github.com/egoist/mygo
$mygoDir = go list -m -f '{{.Dir}}' github.com/egoist/mygo
go -C "$mygoDir" run ./cmd/mygo build -platform windows/amd64 "$projectDir"
```

两个命令都使用 `go.mod` 锁定的 MyGo 0.9.0。只需要 Windows 应用本体时，可以加 `-skip-installer`。

### 产物目录

| 平台 | 项目内目录 | 文件 |
| --- | --- | --- |
| macOS Apple Silicon | `build/darwin-arm64/` | `微博图片恢复.app` 是应用包，`wei-bo-tu-pian-hui-fu-1.2.1-arm64.dmg` 是分发用磁盘映像 |
| Windows x64 | `build/windows-amd64/` | `微博图片恢复.exe` 是应用本体，`wei-bo-tu-pian-hui-fu-setup-1.2.1-amd64.exe` 是 NSIS 安装程序 |

双击 `.app` 或 Windows 应用本体即可启动。也可打开 `.dmg` 将应用拖进「应用程序」，或在 Windows 上运行安装程序。默认 macOS 包使用临时签名，公开分发时可配置 Developer ID 签名和 Apple 公证。

## 开发验证

```sh
CGO_ENABLED=0 go test ./...
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer go test -race ./...
go -C "$mygo_dir" run ./cmd/mygo vet "$project_dir"
```

`mygo_dir` 和 `project_dir` 沿用上方 macOS 打包命令中的值。
