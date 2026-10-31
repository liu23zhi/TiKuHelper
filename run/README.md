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

镜像同时包含主服务 `/app/tiku-adapter` 和工单提醒 worker
`/app/ticket-notification-worker`。使用本目录的 Compose 配置时，两个进程共享
`./data`、`./config.yaml` 和日志目录；worker 不监听 HTTP 端口。

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

使用 `docker run` 时还需要单独启动共享同一数据卷的 worker：

```bash
docker run -d \
  --name tiku-ticket-notification-worker \
  --restart unless-stopped \
  --entrypoint /app/ticket-notification-worker \
  -v ./data:/app/data \
  -v ./logs:/app/logs \
  -v ./config.yaml:/app/config.yaml:ro \
  tiku-adapter -log-dir /app/logs/ticket-worker
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

主服务和工单提醒 worker 需要作为两个进程启动，并使用同一工作目录、配置文件和数据库：

```bash
./tiku-adapter
./ticket-notification-worker -log-dir logs/ticket-worker
```

SQLite 生产库默认启用 WAL、5000ms `busy_timeout`、单连接池和关闭 GORM
prepared statement；worker 与主服务可以共享同一个数据库文件。MySQL 继续使用
原有连接池和 prepared statement 配置，不受这些 SQLite 选项影响。
