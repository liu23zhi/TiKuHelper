# 部署

本目录是可直接运行的部署样例（`config.yaml` + `docker-compose.yml` + nginx 反代）。

证书放进 `nginx/ssl/`（说明见该目录 README），按 `docker-compose.yml` 顶部注释准备环境变量与凭据，然后：

```bash
cd run && docker compose up -d --build
```

容器内 `/app/data` 是持久化目录：数据库须落在其中，否则重启即丢。只挂载这一个目录即可，不影响程序文件。

部署拓扑、反代与数据库运维细节属于内部资料，不在本仓库内。
