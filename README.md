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

账号为 `admin`。首次建库或补齐空管理员密码时，优先使用 `TIKU_ADMIN_PASSWORD`；未设置则使用 `123456`，不生成随机首密或密码文件。已显式设置自己的非公共密码（最多 72 字节）可直接网页登录；重启、升级和更改环境变量都不会重置已有凭据。

默认密码 `123456` 只用于**本机首次设置**，不是网页密码；显式保留 `changeme` 时同样如此。服务可直接启动。随后在服务的工作目录（或 Compose 部署目录）运行以下命令，依次输入当前初始化口令、新密码并确认。输入不回显；无需重启即可使用新密码登录。该命令不能重置已设置的自选密码。

```sh
./tiku-adapter --init-admin-password
# Compose 部署则使用：
docker compose exec tikuhelper /app/tiku-adapter --init-admin-password
```

从之前生成过随机首密的版本升级不会更改原凭据，也不会读取或删除旧的首密文件；请先自行改密，再安全移除旧文件。内存 SQLite 无法跨进程设置，请在启动前显式配置非公共的 `TIKU_ADMIN_PASSWORD`。
- 登录需要人机验证（`captcha_provider=remote`）。本地脚本化调试可往
  `captcha_tokens` 插一条 `scene=login_password`、`status=active`、未过期的记录，
  然后把它作为 `captchaToken` 提交。
