# Tiku Helper

[![持续集成](https://github.com/liu23zhi/TiKuHelper/actions/workflows/ci.yml/badge.svg)](https://github.com/liu23zhi/TiKuHelper/actions/workflows/ci.yml)

内部项目，不接受外部贡献。

## 构建

```powershell
pwsh ./scripts/prepare-web-dist.ps1   # 构建前端并同步到 cmd/adapter-service/dist/
go build ./...
```

⚠️ **前端必须先构建并同步到 `cmd/adapter-service/dist/`**。

`cmd/adapter-service/dist/` 是构建产物目录，**不进 git**（仓库里只有 `.gitkeep`）。
`go:embed all:dist` 是编译期读取，所以少了这一步 `go build` 依然**成功**，
只是打出来的二进制前端为空 —— 启动正常，访问任何页面返回 503。
这类失败发生在运行时而非构建时，最容易漏。脚本会把缺产物变成 `exit 1`。

也可以直接 `go generate ./cmd/adapter-service`（内部调用同一个脚本）。

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

## 首次登录

账号为 `admin`。生产环境首次创建管理员或补齐空凭据时：

- 可预先通过 `TIKU_ADMIN_PASSWORD` 指定自己的密码（最多 72 个 UTF-8 字节）。
- 未指定，或留下 `123456` / `changeme` 占位值时，服务照常启动并生成本实例独有的随机初始密码。启动日志只给出**确切的本机文件路径**，不打印密码；在服务器上以服务账号读取，登录改密后删除该文件。SQLite 文件库使用数据库所在目录，MySQL / 内存库使用工作目录下的 `data/`；数据目录应仅允许受信任的运维账号写入。
- 重启和修改环境变量均不会重置已有管理员密码。只要当前初始密码文件还在，重启会再次提示其路径；它不是登录一次即失效的令牌，**改密后**才不能再用于登录。仅显式开发模式兼容旧的无配置默认密码，请勿用于对外服务。
- 登录需要人机验证（`captcha_provider=remote`）。本地脚本化调试可往
  `captcha_tokens` 插一条 `scene=login_password`、`status=active`、未过期的记录，
  然后把它作为 `captchaToken` 提交。
