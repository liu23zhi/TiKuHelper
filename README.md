# Tiku Adapter

[![持续集成](https://github.com/liu23zhi/tikuHelper/actions/workflows/ci.yml/badge.svg)](https://github.com/liu23zhi/tikuHelper/actions/workflows/ci.yml)

把多个网课题库渠道聚合成一个搜题接口，附带账户余额、账单和管理后台。默认监听 `8060`。

## 它是什么

单进程 Go 服务。收到 `question` / `options` / `type` 后并发查询本地题库、第三方题库渠道和
AI 联网搜索，聚合各渠道答案后按命中的渠道单价结算进账户余额。

自带 Vue 管理后台（题库、渠道 Key、用户、账单、系统设置），前端产物编译进二进制，部署时
只有一个可执行文件加一个数据目录。服务同时给客户端插件下发浏览器扩展包。

## 快速开始

1. 复制 [`configs/config.yaml`](configs/config.yaml) 到工作目录，把 SQLite 放进挂载目录
   （不写的话库落在 `/app/tiku.db`，不会持久化）：

   ```yaml
   database:
     path: data/tiku.db
   ```

2. 构建并启动：

   ```bash
   docker build -t tiku-adapter .
   docker run -d --name tiku-adapter -p 8060:8060 \
     -e JWT_SECRET="<至少 32 字节随机值>" \
     -e TIKU_ADMIN_PASSWORD="<至少 8 位，含大小写、数字、特殊字符>" \
     -e TRUSTED_PROXIES="none" \
     -v ./config.yaml:/app/config.yaml:ro \
     -v ./data:/app/data \
     tiku-adapter
   ```

3. 打开 <http://localhost:8060>，用账号 `admin` 和你设置的密码登录。

生产模式缺 `TRUSTED_PROXIES` 或弱 `TIKU_ADMIN_PASSWORD` 会直接拒绝启动。服务不替你猜
「该信任哪些代理」，也不接受已知默认密码。

## 部署

**Docker Compose**（含 nginx 反代与 WebSocket 透传）：把证书放进 `run/nginx/ssl/`，按
[`run/docker-compose.yml`](run/docker-compose.yml) 顶部注释填好环境变量后
`docker compose up -d --build`，从 `8680` / `9043` 访问。

**二进制**：`./tiku-adapter`，可用 `-log-dir <目录>` 改日志目录。跨平台的发布构建在
[`ci.yml`](.github/workflows/ci.yml) 的 `release-build` 任务里，本地打多平台包另见
[`.goreleaser.yaml`](.goreleaser.yaml)。

**源码**：先 `sh buildweb.sh` 生成前端并放进 `cmd/adapter-service/dist`，再
`go build -o tiku-adapter ./cmd/adapter-service`。跳过第一步会把仓库里的占位
`dist/index.html` 嵌进二进制，页面能打开但前端资源并不存在。

## 配置

`config.yaml` 与工作目录同级，缺失时用内置默认值；另一部分设置在管理后台维护，存在数据库里。

| 项 | 说明 |
|---|---|
| `database.path` | SQLite 文件路径，默认 `tiku.db` |
| `mysql` / `SQL_DSN` | MySQL DSN，环境变量优先于配置文件 |
| `limit` | 按来源 IP 限流，默认 60 秒 120 次 |
| `api` | 自定义外部题库渠道（URL、请求体、jsonpath 取答案） |
| `recordEmptyAnswer` | 是否把无答案的题也收进题库 |
| `JWT_SECRET` | 登录令牌签名密钥，至少 32 字节；缺省时复用或生成数据库中的密钥 |
| `TIKU_ADMIN_PASSWORD` | 生产模式首次初始化管理员所需的强密码 |
| `TRUSTED_PROXIES` / `TRUSTED_PROXIES_FILE` | 生产模式必须显式选 `none` 或受限 IP/CIDR |

其余配置项见 configs/config.yaml 与 internal/service/system_setting.go；密钥类配置在管理后台的「系统设置」里维护。

## 管理后台

管理员看到的菜单按能力分成这几组，用户侧只保留个人相关项。

- **题库与渠道**：维护本地题目答案、批量导入，并管理各题库渠道的 Key、启用状态与单价。
- **AI 搜索**：配置模型端点、轮次与搜索预算、上下文压缩顺序，以及按模型定价的价格表。
- **Key 与额度**：为用户签发访问令牌并限制累计额度，同时生成客户端脚本的短指令。
- **用户与账单**：管理用户与余额，查看充值订单、账单明细和每笔消费的渠道构成。
- **工单与退款**：受理用户工单，走独立退款流程并保留凭证。
- **归属地**：展示 IP 与手机号归属地，缓存可查可刷。
- **商业化开关**：充值赠送、设备上限、邀请返佣、评价返现——**一律默认关闭**，未获批准的
  方案不会因为代码已合入就自动生效。
- **运维**：日志列表、系统设置、扩展更新、系统指标。客户端数据库工具仅管理员可用。

## 开发

```bash
go build ./...                 # 编译全部包
go vet ./...                   # 静态检查
go test -short -p 1 ./...      # 与 CI 相同的后端测试命令
python -m unittest discover -s tools -p 'test_*.py'   # 迁移工具测试
```

前端：

```bash
cd web
npm ci                         # 唯一受支持的锁文件是 web/package-lock.json
npm run serve                  # dev server，把 /adapter-service 代理到 127.0.0.1:8060
npm run lint -- --no-fix
npm run test:unit
npm run build                  # 生产构建
```

健康检查：`/healthz` 看进程存活，`/readyz` 看数据库就绪。日志写在 `logs/`。

## 想看实现细节

本仓库只保留运行所需的代码与配置，设计文档不在其中。需要细节时按位置读代码：

| 想了解 | 看哪里 |
|---|---|
| 系统设置项与默认值 | `internal/service/system_setting.go` |
| 接口清单 | `internal/api/router.go` |
| 缓存与计费口径 | `internal/controller/search_flight.go`、`internal/service/billing_record.go` |
| 启动迁移 | `internal/registry/*_migration.go` |
| 日志与轮转 | `pkg/logger/` |
| 前端页面 | `web/src/pages/` |

`AGENTS.md` 记录了本项目踩过的坑与不可回退的约定，改代码前值得先扫一遍。

## 许可

派生自 [itihey/tikuAdapter](https://github.com/itihey/tikuAdapter)（MIT）。本仓库当前未保留
`LICENSE` 文件，对外分发前需先补齐。
