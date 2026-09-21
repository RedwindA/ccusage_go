# 发布指南

本仓库为 https://github.com/RedwindA/ccusage_go；原上游保留为本地 `upstream`。
保留原提交历史和 MIT 许可证。Go module 路径为
`github.com/RedwindA/ccusage_go`。

## 发布新版本

1. 将变更记录从 `CHANGELOG.md` 的 `Unreleased` 移至新版本标题，例如
   `## [v0.16.0] - YYYY-MM-DD`。版本标签必须与标题一致。
2. 本地验证并提交：

   ```sh
   go test -race ./...
   go vet ./...
   python3 -m unittest discover -s scripts -p 'test_*.py' -v
   git add <要发布的文件>
   git commit -m "chore: prepare v0.16.0"
   git push origin main
   ```

3. 确认 main 的 Build and Test 成功，再创建并推送标签：

   ```sh
   git tag -a v0.16.0 -m "Release v0.16.0"
   git push origin v0.16.0
   ```

4. 在仓库 Actions 查看 Release。标签触发的工作流会重新运行 Linux、macOS、
   Windows 测试和 vet，构建六种平台包，生成 SHA-256 校验和，再发布 GitHub Release。
   发布说明取自该版本的 CHANGELOG。安装脚本随 Release 上传，默认安装最新版。
   包内包含原 MIT 许可证。带 `-` 的标签（例如 `v0.16.0-rc.1`）标记为预发布。

工作流仅使用内置 `GITHUB_TOKEN`，发布 job 已配置 `contents: write`，无需个人 token。
失败时先查看日志；可在 Actions 重跑失败的任务。工作流先建立草稿，全部附件上传后
才公开发布。不要移动已经公开的版本标签；代码修复后应创建下一个版本。

## 本地构建发布包

需要 Go（按 go.mod 的 toolchain）、make、tar、zip 和 sha256sum：

```sh
make release-all VERSION=v0.16.0
```

输出至 `dist/`：

- `ccusage_go-{linux,darwin}-{amd64,arm64}.tar.gz`
- `ccusage_go-windows-{amd64,arm64}.exe.zip`
- `checksums.txt`

也可单独构建：

```sh
VERSION=v0.16.0 GOOS=linux GOARCH=arm64 sh scripts/build-release.sh
```

预编译版本显示标签中的版本号；`make build` 使用 Git 描述；直接 `go build`
或 `go install` 的默认版本显示为 `dev`。

## 安装验证

```sh
curl -fsSL https://github.com/RedwindA/ccusage_go/releases/latest/download/install.sh -o /tmp/ccusage-install.sh
INSTALL_DIR="$HOME/.local/bin" sh /tmp/ccusage-install.sh
"$HOME/.local/bin/ccusage_go" --version
```

可传入标签安装指定版本：`sh /tmp/ccusage-install.sh v0.15.0`。
脚本在校验成功后才替换现有二进制。Linux/macOS 使用 `sha256sum` 或 `shasum`；
Windows 用户从 Release 下载 ZIP，解压后放入 PATH。

流程依据：[GitHub Go CI 文档](https://docs.github.com/en/actions/tutorials/build-and-test-code/go)、
[gh release create](https://cli.github.com/manual/gh_release_create)。
