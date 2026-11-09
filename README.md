# Tiku Adapter

[![持续集成](https://github.com/liu23zhi/tikuHelper/actions/workflows/ci.yml/badge.svg)](https://github.com/liu23zhi/tikuHelper/actions/workflows/ci.yml)

把多个网课题库渠道聚合成一个搜题接口，附带账户余额、账单和管理后台。默认监听 `8060`。

## 这是什么，解决什么问题

网课客户端要答案，答案散在很多地方：自己攒的本地题库、几个第三方题库接口、还有大模型联网搜索。
每个渠道的请求格式、鉴权方式、计费口径都不一样，客户端不可能逐个对接。

这个服务把它们收拢到**一个 HTTP 接口**后面：客户端只发 `question` / `options` / `type`，
服务并发查所有已启用渠道，聚合出答案，再按命中的渠道单价从账户余额扣费。

单进程 Go 服务，Vue 管理后台编译进二进制。部署产物是一个可执行文件加一个数据目录，
不需要额外的数据库容器（默认 SQLite，可选 MySQL）。

它同时给客户端插件下发浏览器扩展包，所以 `chaoxing_plus` 之类的客户端不用自己管扩展更新。

## 三步跑起来

**1. 准备配置**（把数据库放进挂载目录，否则库落在容器内 `/app/tiku.db`，重启就丢）

```bash
cp configs/config.yaml ./config.yaml
```

```yaml
database:
  path: data/tiku.db
```

**2. 构建并启动**

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

**3. 打开 <http://localhost:8060>**，账号 `admin`，密码是上一步设置的值。

生产模式下缺 `TRUSTED_PROXIES` 或 `TIKU_ADMIN_PASSWORD` 不符合要求会**直接拒绝启动**。
服务不替你猜「该信任哪些代理」，也不接受已知默认密码 `123456`。

## 部署方式

### Docker Compose（带 nginx 反代）

仓库内 [`run/docker-compose.yml`](run/docker-compose.yml) 已经配好 nginx 反向代理、WebSocket 透传和
可信代理转换脚本。把证书放进 `run/nginx/ssl/`，按文件顶部注释填环境变量，然后：

```bash
cd run
docker compose up -d --build
```

从 `8680`（HTTP）或 `9043`（HTTPS）访问。后端 `8060` 只绑 `127.0.0.1`，由 nginx 通过 compose 网络访问。

### 二进制

自行交叉编译（无需 Docker，但前端要先按下一节构建好）：

```bash
CGO_ENABLED=0 go build -o tiku-adapter ./cmd/adapter-service
./tiku-adapter -log-dir ./logs
```

`-log-dir` 可改日志目录（默认工作目录下 `logs/`）。跨平台编译矩阵在
[`ci.yml`](.github/workflows/ci.yml) 的 `release-build` 任务里，本地打多平台包见
[`.goreleaser.yaml`](.goreleaser.yaml)（`goreleaser release`，前置钩子会先跑 `buildweb.sh`）。

### 源码

前端必须先构建，否则会把仓库里的占位页嵌进二进制 —— 页面能打开，但前端资源并不存在：

```bash
sh buildweb.sh                                    # npm ci + build + 拷到 cmd/adapter-service/dist
go build -o tiku-adapter ./cmd/adapter-service
```

## 常用配置

只列最常改的。`config.yaml` 与工作目录同级，缺失时用内置默认值；另一部分设置在管理后台维护、存数据库。

| 项 | 作用 |
|---|---|
| `database.path` | SQLite 文件路径，默认 `tiku.db`；Docker 部署建议 `data/tiku.db` |
| `mysql` / `SQL_DSN` | MySQL DSN，环境变量 `SQL_DSN` 优先于配置文件 |
| `limit` | 按来源 IP 限流，默认 60 秒 120 次 |
| `api` | 自定义外部题库渠道：URL、方法、请求体模板、用 jsonpath 取答案 |
| `recordEmptyAnswer` | 是否把查不到答案的题也收进题库，便于日后人工整理 |
| `JWT_SECRET` | 登录令牌签名密钥，至少 32 字节；缺省时复用数据库中的密钥或自动生成 |
| `TIKU_ADMIN_PASSWORD` | 生产模式首次初始化管理员所需的强密码 |
| `TRUSTED_PROXIES` / `TRUSTED_PROXIES_FILE` | 生产模式必须显式选 `none` 或受限 IP/CIDR |

`TIKU_APP_MODE` 缺省 `production`，只有显式设成 `development` 才启用旧开发兼容行为
（包括允许默认密码），不要用它绕过生产启动保护。

完整清单看 [`configs/config.yaml`](configs/config.yaml) 的注释（文件级配置）与
`internal/service/system_setting.go` 的常量（后台配置）；按主题找代码见文末[代码导航](#代码导航)。

## 接口概览

主入口是同步搜题，鉴权用 `Authorization: Bearer <key>`：

```http
POST /adapter-service/search
Content-Type: application/json
Authorization: Bearer <access-token>

{"question":"题干","options":["A. …","B. …"],"type":0}
```

`type` 决定答案判定方式：`0` 单选、`1` 多选、`3` 判断要求答案命中选项；`2` 填空/连线、
`4` 简答/阅读、`-4` 阅读理解允许自由文本。传错类型会直接导致答案错乱。
判定统一在 `pkg/util/find_answer.go` 的 `IsObjectiveQuestionType` / `AllowsRepeatedAnswers`，
新增题型时改那里，不要各处各写一份。

同一请求可命中多个渠道，各渠道单价分别落账，账单里合并展示。命中服务端缓存（10 分钟）不再计费。

其余接口（异步任务、AI 兜底、扩展更新、管理端）见 `internal/api/router.go`。

## 管理后台

管理员看到的菜单按能力分组，用户侧只保留个人相关项。每项一句话说清它解决什么问题：

- **题库总览 / 智能导入** — 维护本地题目答案，批量导入成型的题库文件，让常见题不花钱直接命中。
- **默认账号配置** — 管理各题库渠道的账号、Key、启用状态与单价，决定一次搜题会去打哪些上游、按什么价计费。
- **AI 搜索配置** — 配模型端点、模型轮次与搜索预算、上下文压缩顺序和按模型的价格表，控制 AI 兜底的成本与耗时。
- **令牌管理（Key）** — 给调用方签发访问令牌并限制累计额度，客户端凭它调接口，不必暴露账户密码。
- **脚本管理** — 生成客户端脚本的短指令与授权码，把「装什么、连哪个服务」这件事从口头交代变成可下发的东西。
- **用户管理** — 建用户、改余额、重置密码，处理「这个账号为什么扣了钱」这类问题。
- **账单明细 / 充值记录** — 看每笔消费命中了哪些渠道、各自多少钱，以及充值订单和退款状态。
- **商业化开关** — 充值赠送、设备上限、邀请返佣、评价返现，**一律默认关闭**；代码合入不等于功能上线，需显式开启。
- **邀请返佣** — 记录邀请关系与返佣账本，结算只改状态、不直接写用户钱包，便于核对与回退。
- **评价返现** — 用户提交商店评价截图，系统抓取公开评价并打分，管理员复核后发放赠金；打分只作建议。
- **客户端数据库工具** — 对考试客户端本地库做脱机加解密与只读预览，仅管理员可用，用于排障而非修改线上数据。
- **工单与退款** — 受理用户反馈，走独立退款流程，凭证独立加密存放、只有申请人与授权管理员可读。
- **归属地管理** — 展示 IP 与手机号的归属地，结果持久化复用，可单条或批量重查。
- **系统管理 / 扩展更新 / 日志列表** — 维护密钥类配置、控制客户端扩展包的自动检查与下发、按请求 ID 查失败原因。

> 密钥类配置（支付私钥、短信密钥、微信 Token、UApiPro 密钥）在「系统管理」里明文返回给管理员，
> 因为只有管理员能打开该页，返回掩码反而让人无法核对已配置的值。日志里仍然一律脱敏。

## 开发

```bash
go build ./...                 # 编译全部包
go vet ./...                   # 静态检查
go test -short -p 1 ./... -count=1   # 与 CI 相同的后端测试命令
python -m unittest discover -s tools -p 'test_*.py'   # SQLite→MySQL 迁移工具测试
```

前端（`web/`）：

```bash
cd web
npm ci                         # 唯一受支持的锁文件是 web/package-lock.json
npm run serve                  # dev server，把 /adapter-service 代理到 127.0.0.1:8060
npm run lint -- --no-fix
npm run test:unit
npm run build
```

健康检查：`/healthz` 看进程存活，`/readyz` 看数据库就绪。日志默认写 `logs/`。

## 代码导航

设计文档、验收记录和踩坑清单不在本仓库（已迁出并从历史移除），存放在一个**只有协作者能打开**的私有文档仓库。
本仓库不提供指向它的链接 —— 对外部读者那是死链。想知道某块怎么实现的，按下表直接读代码，
那里是同一份事实的权威来源，不会随文档漂移。

| 想了解 | 读哪里 |
|---|---|
| 日志格式、轮转与归档 | `pkg/logger/` |
| 出站请求重试策略 | `internal/uapi/`、`internal/service/location.go` |
| 工单退款与凭证加密 | `internal/controller/ticket_refund.go` |
| 答案质量验收边界 | `pkg/util/tiku_hash.go` |
| 图片题 OCR 辅助 | `internal/uapi/ocr.go`、`internal/service/ai_ocr_cache.go` |
| 模型上下文窗口与压缩 | `internal/service/ai_context_compression_config.go` |
| 浏览器扩展更新接口 | `internal/controller/extension.go` |
| 接口与路由全表 | `internal/api/router.go` |
| 后台配置项常量 | `internal/service/system_setting.go` |
| 前端源码与工具链 | `web/src/`、`web/package.json` |

改代码前值得先扫一遍 `AGENTS.md` —— 那里记着本项目踩过的坑与不可回退的约定
（金额精度、缓存并发合并、启动迁移写法、答案判定等）。

## 许可

派生自 [itihey/tikuAdapter](https://github.com/itihey/tikuAdapter)（MIT）。本仓库当前未保留
`LICENSE` 文件，对外分发前需先补齐。
