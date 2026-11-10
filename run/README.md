# 部署

本目录是可直接运行的部署样例（`config.yaml` + `docker-compose.yml` + nginx 反代）。
二进制与其它平台从 release 下载对应版本，解压即用。

## Docker Compose

证书放进 `nginx/ssl/`（说明见该目录 README），按 `docker-compose.yml` 顶部注释准备环境变量
与凭据，然后 `cd run && docker compose up -d --build`。浏览器按 compose 注释里的地址访问：
后端只绑回环，由 nginx 通过 compose 网络访问，无需对外发布。

## Docker（单容器）

```bash
docker build -t tiku-adapter .
docker run -d --name tiku-adapter \
  -v ./config.yaml:/app/config.yaml:ro \
  -v ./data:/app/data \
  tiku-adapter
```

容器内 `/app/data` 是持久化目录：数据库须落在其中（`database.path: "data/tiku.db"`），
否则重启即丢。只挂载这一个目录即可，不影响程序文件。

## 原生运行

直接执行 `./tiku-adapter`。工单提醒 worker 随主服务在同一进程内作为后台子线程运行，
不需要额外容器或 HTTP 端口。

SQLite 生产库默认启用 WAL、5000ms `busy_timeout`、单连接池并关闭 GORM prepared statement；MySQL 用原有连接池与 prepared statement 配置。
