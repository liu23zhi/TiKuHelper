# Tiku Helper

[![持续集成](https://github.com/liu23zhi/TiKuHelper/actions/workflows/ci.yml/badge.svg)](https://github.com/liu23zhi/TiKuHelper/actions/workflows/ci.yml)

内部项目，不接受外部贡献。

## 构建

```bash
cd web && npm ci && npm run build && cd ..
mkdir -p cmd/adapter-service/dist && cp -R web/dist/. cmd/adapter-service/dist/
go build ./...
```

⚠️ **前端必须先构建并复制到 `cmd/adapter-service/dist/`**。

后端用 `//go:embed dist` 把前端产物编译进二进制，而 `cmd/adapter-service/dist/`
被 `.gitignore` 忽略（只有 `index.html` 强制跟踪）。所以：

- **Docker 构建**：`Dockerfile` 的 builder1 阶段构建前端并 `COPY` 过去，自动完成
- **本地 `go build` / `go run`**：**不会自动做**，漏了这一步得到的是旧前端或白屏

症状：改完前端代码、单测全绿，跑起来却还是旧页面。
`internal/api/web_dist_freshness_test.go` 会在「产物存在但过期」时报错。

## 本地 gofmt 检查

Windows 上 `core.autocrlf=true` 会让工作树是 CRLF，`gofmt -l` 因此误报「未格式化」，
而 CI 在 LF 下通过。用下面这条区分「真格式问题」与「CRLF 假阳性」：

```powershell
.\scripts\local-gofmt.ps1        # REAL=须修 / CRLF-ONLY=无需修 / CLEAN=合规
.\scripts\local-gofmt.ps1 -Fix   # 修 REAL，写回 LF
```

