# 如何部署使用

## 您可以使用我们已经部署好的应用
等待建设中...
## 您可以独立将此程序部署到 linux,windows,macos,docker,k8s 等环境中

- windows 直接从release中下载对应的版本即可，解压后运行即可
- linux 直接从release中下载对应的版本即可，解压后运行即可
- macos 直接从release中下载对应的版本即可，解压后运行即可
- docker 使用 Docker 部署，参考下方 Docker 部署说明
- k8s 建设中...

## Docker 部署

### 构建镜像

```bash
docker build -t tiku-adapter .
```

工单提醒 worker 在主服务进程内作为后台子线程运行，不需要额外容器或 HTTP 端口。

### 运行容器

#### 基础运行（使用默认配置）

```bash
docker run -d \
  --name tiku-adapter \
  -e JWT_SECRET="<至少32字节的随机密钥>" \
  -e TIKU_ADMIN_PASSWORD="<首次初始化管理员密码>" \
  -p 8060:8060 \
  -v ./data:/app/data \
  tiku-adapter
```

#### 推荐方式：使用配置文件指定数据库路径

创建 `config.yaml` 文件并设置数据库路径为 `data/tiku.db`：
```yaml
database:
  path: "data/tiku.db"
```

然后运行容器：
```bash
docker run -d \
  --name tiku-adapter \
  -e JWT_SECRET="<至少32字节的随机密钥>" \
  -e TIKU_ADMIN_PASSWORD="<首次初始化管理员密码>" \
  -p 8060:8060 \
  -v ./data:/app/data \
  -v ./config.yaml:/app/config.yaml \
  tiku-adapter
```

### 目录结构说明

容器内的目录结构：
```
/app
├── tiku-adapter        # 程序可执行文件
├── config.yaml         # 配置文件（可选挂载）
└── data                # 数据目录（持久化）
    └── tiku.db         # SQLite 数据库文件
```

### 配置说明

在 `config.yaml` 中可以自定义数据库路径：

```yaml
database:
  path: "data/tiku.db"  # SQLite数据库文件路径
```

**注意事项：**
- `JWT_SECRET` 环境变量符合要求时优先使用并写入数据库；为空时复用数据库密钥，没有有效密钥时自动生成
- `TIKU_ADMIN_PASSWORD` 仅用于首次初始化管理员；未配置时默认密码为 `123456`
- Docker 部署时，**强烈建议**在配置文件中设置 `database.path: "data/tiku.db"`，然后挂载 `/app/data` 目录
- 如果不指定 `database.path`，默认使用 `tiku.db`（向后兼容，但不适合 Docker 持久化）
- 将数据库路径设置在 `data` 目录下，可以只挂载该目录，不会影响容器内的程序文件
- 本地部署（非 Docker）保持默认即可，数据库会在程序所在目录创建

### 原生运行

工单提醒 worker 会随主服务启动，在同一进程内作为后台子线程运行：

```bash
./tiku-adapter
```

SQLite 生产库默认启用 WAL、5000ms `busy_timeout`、单连接池和关闭 GORM
prepared statement；通知子线程与主服务共享同一个数据库连接策略。MySQL 继续使用
原有连接池和 prepared statement 配置，不受这些 SQLite 选项影响。

### 工单通知格式

企业微信机器人使用 Markdown 推送新工单和用户追问，直接展示工单编号、标题、用户、
状态、时间和本次消息内容，不再附带 `/adapter/tickets/...` 路径。消息超过企业微信
4096 字节上限时按 UTF-8 安全截断并显示说明，配置的提醒成员保留在末尾。
未配置、未启用或配置无效时仍跳过通知；此变更无需调整 Compose 或增加容器。

### 单账号登录会话

密码、短信和微信登录统一维护数据库中的当前会话。新登录成功后，之前设备的登录令牌
立即失效；业务页面可见时每 30 秒检查一次，切回前台立即检查并提示重新登录。
退出登录会作废对应会话，迟到的旧会话退出请求不会注销新会话。
个人资料页的微信扫码绑定只查询绑定状态，不会触发账号登录或替换当前会话。

启动时会自动为旧用户表补充 `login_session_id` 列。升级前的登录令牌暂时兼容，账号
首次重新登录后即进入单会话校验。会话变更持久化到现有数据库，无需新增容器或配置。
