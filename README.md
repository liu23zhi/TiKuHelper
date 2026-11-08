# Tiku Adapter

题库聚合与适配服务，默认监听 `8060` 端口。启动后访问：

```text
http://localhost:8060
```

## 日志

启动时自动在工作目录的 `logs/` 中创建 UTF-8 TXT 日志，同时保留控制台输出：

- `Tiku.txt`：基础运行与调用记录。
- `Tiku-warning.txt`：warning、error、fatal/panic。
- `Tiku-error.txt`：error、fatal/panic。
- `Tiku-thinking.txt`：完整且脱敏的模型请求、响应、原始 SSE 与搜索调用。

每份文件独立按 10 MiB 自动分割，单条日志不拆断。每次启动会把日志根目录中的上一批
日志合并压缩为 `logs/archive/开始时间.zip`，ZIP 内包含 `Tiku.txt`、warning/error
、thinking 及运行期间产生的轮转文件，然后创建新的活动文件。文件名使用被压缩日志中最早一条日志的
开始时间（格式 `YYYYMMDD_HHMMSS.zip`）；同一秒重复归档会追加序号。系统设置中的“最大日志大小”
默认是 5GB，支持按字节、KB、MB、GB、TB 设置（每级按 1024 换算）。统计活动日志与
`archive` 下 ZIP 的总占用，超过后从最旧归档开始删除，活动日志不会主动删除。
可用 `-log-dir <目录>` 覆盖默认目录。日志时间统一使用上海时间（Asia/Shanghai）。
同一目录只供一个服务进程写入；多实例请使用独立目录。

AI/题库请求失败同时写入管理端“日志列表”，保存请求 ID、失败渠道及完整错误快照；
部分渠道失败与无答案单独显示，不改变既有计费和缓存规则。密钥不会写入诊断日志，
数据库日志不展开 SQL 参数。Docker 部署建议挂载 `./logs:/app/logs`。

AI 详细轨迹写入 `Tiku-thinking.txt`，按 requestId 记录每轮模型端点、工具调用、搜索词、
剩余预算、尝试次数、结果数、耗时、HTTP 状态和 token 汇总，保留上游实际返回的完整内容
及推理字段。普通日志只保留基础调用记录；全部日志均脱敏密钥等凭据。

管理端“AI 搜索配置”中的“模型最大调用次数”和“最大搜索次数”分别控制一次 AI 搜题的
模型逻辑轮次与 web_search 次数，默认值为 4 次和 3 次，范围为 1～20 次。同轮失败重试不
额外占用模型轮次，切换备用地址占一轮；普通 AI 路径同样受模型轮次限制。每轮模型请求都会收到
剩余预算提示；达到最后一轮时会被要求直接输出最终答案 JSON，不再调用搜索工具。
“每轮重试次数”表示包含首次调用的尝试机会，默认 3 次，可设置为 1～10 次。

“深度思考”默认关闭，普通模式保持原先的一次性回复；开启后需要弹窗确认，允许模型多轮
联网搜索后作答，耗时与费用可能增加。搜索单价默认 0；配置价格后每次实际搜索外呼单独记账，
关联到搜题账单合并展示，在详情查看每次调用。

深度思考还可按需使用 `web_read` 网页读取工具。工具通过 UApiPro 的异步网页转 Markdown
接口提交并轮询任务，默认每次最多读取 5 次，网页读取单价默认 0；设置价格后每次成功完成的
实际读取单独写入账单明细，失败或超时不计费，重复 URL 在同一轮请求内复用结果。普通模式仍保持
旧版一次性回复，不调用网页读取工具。

## IP 与手机号归属地

配置 UApiPro API 密钥后，用户管理、日志列表、账单明细和个人中心自动显示归属地。
成功结果持久化并永久复用，相同类型、地址与查询模式不会因重启或翻页重新查询。
管理员可在“IP 归属地管理”“手机号归属地管理”查看原始响应、刷新或删除记录；普通用户
只能查询本人账号关联的信息。自动查询失败时保留原值，手动刷新失败不会覆盖旧数据。

默认 IP 查询与商业增强查询独立缓存。旧缓存未记录查询模式的 IP 保留为“历史（来源未记录）”，
可查看或删除；新模式首次查询会单独写入。历史空来源 IP 账单没有可靠关联键时不会推测补写。

## 环境变量

### 浏览器扩展更新

原生客户端可通过 `GET /adapter-service/extensions/edge/latest` 查询已验证的 Edge 扩展，
再从响应中的相对 `downloadUrl` 下载 CRX3。管理员在后台“扩展更新”页设置自动检查、检查间隔、
Edge/Chrome 商店 ID，并查看版本和手动刷新；检查间隔支持分钟、小时、天、周、月，月按固定 30 天换算；
配置保存到数据库后立即生效，无需重启。
默认每 10 天检查，失败保留上一份可用包，Chrome 来源默认停用。设置逐字段按“环境变量 →
数据库 → 内置默认值”读取；未设置环境变量的字段可在后台修改，已由环境变量覆盖的字段会标明来源
并禁止编辑。移除覆盖变量并重启后恢复数据库保存值。完整接口、权限和校验规则见
[`docs/extension-update-api.md`](docs/extension-update-api.md)。

```powershell
$env:EXTENSION_UPDATE_ENABLED = "true"
$env:EXTENSION_UPDATE_INTERVAL = "240h"
$env:EXTENSION_CACHE_DIR = "data/extensions"
# 后续上架 Chrome 后再设置；留空时不启用 Chrome 来源。
$env:EXTENSION_CHROME_ID = ""
```

缓存默认在 `data/extensions`（相对服务工作目录）。使用 `run/docker-compose.yml` 时，容器路径为
`/app/data/extensions`，宿主路径为 `run/data/extensions`，已包含在 `./data:/app/data` 挂载中，
无需新增缓存卷。目录中保存已验证的 CRX 与最新版本元数据；升级或重建容器时保留 `data` 即可保留缓存。
`EXTENSION_CACHE_DIR` 是部署目录设置，不存于数据库；如改到 `/app/data` 以外，须另行挂载该目录才能持久化。

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

### `TIKU_APP_MODE`

缺省为 `production`；只有显式设为 `development` 才启用旧开发兼容行为。
该模式独立于 `GIN_MODE`，不要用开发模式绕过生产启动保护。

### `TIKU_ADMIN_PASSWORD`

生产模式下，新建管理员或管理员凭据为空时，必须提供强密码：

- 用户名为 `admin`；密码至少 8 个字符，包含大写字母、小写字母、数字和特殊字符，UTF-8 总长度不超过 72 字节。
- 已有非空凭据不会被环境变量重置；旧明文凭据按原值升级为 bcrypt，不改成环境变量中的密码。
- 已有已知默认密码 `123456`（包括其哈希）会阻止生产启动，应通过受控改密流程处理，不能靠环境变量强行重置。
- 仅显式 `development` 允许未提供密码时的旧默认 `123456`，不应用于生产部署。

```powershell
$env:TIKU_ADMIN_PASSWORD = "<使用密码管理器生成符合上述规则的强密码>"
```

管理员创建用户或重置用户密码仅要求非空且不超过 72 字节；该简化仅用于管理员创建/重置用户，
不放宽用户注册或管理员初始化的规则。

### `TRUSTED_PROXIES` / `TRUSTED_PROXIES_FILE`

生产模式必须显式选择以下一种配置：

- `TRUSTED_PROXIES=none`：不信任任何代理转发头，来源 IP 使用直连地址。
- `TRUSTED_PROXIES`：实际可信代理的 IP/CIDR，多个值以英文逗号分隔。
- `TRUSTED_PROXIES` 留空时，显式设置 `TRUSTED_PROXIES_FILE` 为容器内可读文件路径，内容为 `none` 或受限 IP/CIDR。

```powershell
$env:TRUSTED_PROXIES = "none"
# 反向代理部署请改成实际 nginx IP/CIDR；或显式选择已挂载的文件：
# $env:TRUSTED_PROXIES_FILE = "/app/trusted_proxies.txt"
```

未配置、空/仅注释文件、非法条目，或单条/多条合并信任整个 IPv4/IPv6 地址族都会拒绝生产启动；
不会自动补入本机、局域网或私网网段。非空 `TRUSTED_PROXIES` 优先于显式文件。
反向代理部署应只信任真实代理地址；选择 `none` 时不会还原代理转发的客户端 IP。
仅挂载默认文件而未显式设置 `TRUSTED_PROXIES_FILE` 不构成生产配置。

示例Nginx保留转发头清洗；仅按有效受限配置恢复可信上游来源，不为空文件补全信任、不隐式加入局域网，整族覆盖会拒绝。Nginx与应用是两层独立边界，后端仅发布宿主回环，须防不可信调用方绕过网关。

### 跨域调用

Token/API Key 支持任意来源跨域调用，不设置来源白名单。跨站客户端应携带 Token/API Key，
不要使用浏览器登录 Cookie；服务端拒绝明确的跨源或跨站 Cookie 鉴权，不把 CORS 当作完整 CSRF 防御。

## Docker 示例

```powershell
docker run -d `
  --name tiku-adapter `
  -e JWT_SECRET="<至少32字节的随机密钥>" `
  -e TIKU_APP_MODE="production" `
  -e TIKU_ADMIN_PASSWORD="<替换为首次初始化强密码：至少8字符、四类齐全且不超过72字节>" `
  -e TRUSTED_PROXIES="none" `
  -p 8060:8060 `
  -v ./data:/app/data `
  -v ./logs:/app/logs `
  -v ./config.yaml:/app/config.yaml:ro `
  tiku-adapter
```

上述占位内容必须替换，不能原样用于部署。已有有效管理员凭据且无须初始化时，可省略密码变量；
是否需要密码由后端检查数据库状态决定。
仓库中的 `run/docker-compose.yml` 也支持通过同名环境变量注入这些配置；启动前必须显式配置可信代理策略。

扩展更新字段仅在显式设置EXTENSION_*时覆盖数据库，不注入空占位值。隔离开发预览必须显式EXTENSION_UPDATE_ENABLED=false、空Edge/ChromeID并禁用工单通知/清空Webhook，不能仅以网络不可达代替禁用。

## 安全注意事项

- 不要将 `tiku.db`、`config.yaml`、`.env`、私钥或实际凭据提交到 Git。
- 生产新建/空凭据管理员必须配置强密码；已有凭据不被环境变量重置，已知默认 `123456` 不允许生产启动。
- 生产环境应使用 HTTPS，并仅通过可信反向代理暴露服务。
- 程序会根据客户端 IP 执行默认限流；反向代理部署需正确处理来源头与后端访问边界，不能让不可信调用方绕过 Nginx。
