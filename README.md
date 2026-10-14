# Tiku Adapter

题库聚合与适配服务，默认监听 `8060` 端口。启动后访问：

```text
http://localhost:8060
```

## 日志

启动时自动在工作目录的 `logs/` 中创建 UTF-8 TXT 日志，同时保留控制台输出：

- `Tiku.txt`：全部日志。
- `Tiku-warning.txt`：warning、error、fatal/panic。
- `Tiku-error.txt`：error、fatal/panic。

每份文件独立按 10 MiB 自动分割，单条日志不拆断。每次启动会把日志根目录中的上一批
日志合并压缩为 `logs/archive/开始时间.zip`，ZIP 内包含 `Tiku.txt`、warning/error
及运行期间产生的轮转文件，然后创建新的活动文件。文件名使用被压缩日志中最早一条日志的
开始时间（格式 `YYYYMMDD_HHMMSS.zip`）；同一秒重复归档会追加序号。系统设置中的“最大日志大小”
默认是 5GB，支持按字节、KB、MB、GB、TB 设置（每级按 1024 换算）。统计活动日志与
`archive` 下 ZIP 的总占用，超过后从最旧归档开始删除，活动日志不会主动删除。
可用 `-log-dir <目录>` 覆盖默认目录。日志时间统一使用上海时间（Asia/Shanghai）。
同一目录只供一个服务进程写入；多实例请使用独立目录。

AI/题库请求失败同时写入管理端“日志列表”，保存请求 ID、失败渠道及完整错误快照；
部分渠道失败与无答案单独显示，不改变既有计费和缓存规则。密钥不会写入诊断日志，
数据库日志不展开 SQL 参数。Docker 部署建议挂载 `./logs:/app/logs`。

AI 联网搜索日志会按 requestId 记录每轮模型端点、工具调用、精炼后的搜索词、搜索预算、
结果数、耗时、HTTP 状态和 token 汇总；服务只记录可审计的调用轨迹与有限响应摘要，不记录
API 密钥或模型隐藏思维链原文。

管理端“AI 搜索配置”中的“模型最大调用次数”和“最大搜索次数”分别控制一次 AI 搜题的
模型请求次数与 web_search 次数，默认值为 4 次和 3 次，范围为 1～20 次。模型上限包含
重试与备用地址请求，也适用于未配置联网搜索工具的普通 AI 路径。每轮模型请求都会收到
剩余预算提示；达到最后一轮时会被要求直接输出最终答案 JSON，不再调用搜索工具。

## 环境变量

### `JWT_SECRET`

用于签发和校验登录 JWT。程序按以下顺序处理：

- 环境变量 `JWT_SECRET` 存在且符合要求时，优先使用该值并写入数据库。
- 环境变量不存在时，优先复用数据库中已经保存的密钥。
- 环境变量存在但不符合要求，或环境变量和数据库都没有有效密钥时，程序自动生成随机密钥并写入数据库。

```powershell
$env:JWT_SECRET = "使用密码管理器生成的至少32字节随机值"
```

有效密钥要求：

- 至少 32 字节。
- 不能使用已知开发密钥 `tiku-adapter-dev-secret`。

配置建议：

- 不要提交到 Git 仓库。
- 不要在多个环境复用同一个值。
- 轮换该值会使现有登录会话失效。

### `TIKU_ADMIN_PASSWORD`

可选，仅用于首次创建管理员账号。首次初始化时：

- 已设置该变量：用户名为 `admin`，密码使用变量值。
- 未设置或留空：用户名为 `admin`，密码使用默认值 `123456`。
- 检测到数据库中的旧版明文默认密码 `123456` 时，程序会将其转换为 bcrypt 哈希，密码值保持不变。

```powershell
$env:TIKU_ADMIN_PASSWORD = "首次初始化时使用的强密码"
```

### `TRUSTED_PROXIES`

可选，用于指定可信反向代理的 IP 或 CIDR，多个值以英文逗号分隔。

```powershell
$env:TRUSTED_PROXIES = "10.0.0.10,172.18.0.0/16"
```

为空时程序信任所有代理转发头；设置后仅信任列表中的代理地址。由于信任所有代理会使客户端能够伪造 `X-Forwarded-For`，生产环境仍建议明确配置该变量。

## Docker 示例

```powershell
docker run -d `
  --name tiku-adapter `
  -e JWT_SECRET="<至少32字节的随机密钥>" `
  -e TIKU_ADMIN_PASSWORD="<首次初始化管理员密码，留空则使用 123456>" `
  -e TRUSTED_PROXIES="10.0.0.10,172.18.0.0/16" `
  -p 8060:8060 `
  -v ./data:/app/data `
  -v ./logs:/app/logs `
  -v ./config.yaml:/app/config.yaml:ro `
  tiku-adapter
```

仓库中的 `run/docker-compose.yml` 也支持通过同名环境变量注入这些配置。

## 安全注意事项

- 不要将 `tiku.db`、`config.yaml`、`.env`、私钥或实际凭据提交到 Git。
- 若未配置 `TIKU_ADMIN_PASSWORD`，首次登录后立即修改默认管理员密码 `123456`。
- 生产环境应使用 HTTPS，并仅通过可信反向代理暴露服务。
- 程序会根据客户端 IP 执行默认限流；反向代理部署时必须正确设置 `TRUSTED_PROXIES`。
