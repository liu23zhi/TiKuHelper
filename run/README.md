# 部署

本目录是可直接运行的部署样例（`config.yaml` + `docker-compose.yml` + nginx 反代）。

证书放进 `nginx/ssl/`（说明见该目录 README），按 `docker-compose.yml` 顶部注释准备环境变量与凭据，然后：

```bash
cd run && docker compose up -d --build
```

容器内 `/app/data` 是持久化目录：数据库须落在其中，否则重启即丢。只挂载这一个目录即可，不影响程序文件。

## 热生效说明

`nginx` 容器启动后会每 3 秒比对一次配置指纹，变化时先 `nginx -t` 校验、通过后再 `nginx -s reload`。

| 改了文件 | 是否需要重启 | 为什么 |
|---|---|---|
| `nginx/conf.d/*.conf` | 不需要（3 秒内自动 reload） | 轮询检测配置指纹变化 |
| `nginx/conf.d/*.inc` | 不需要 | 同上 |
| `trusted_proxies.txt` | **需要重启两个容器** | nginx 侧会自动重转，但后端 Go 进程只在启动时读 |
| `nginx/ssl/*` | 不需要 | 同上自动 reload |
| `nginx/html/*` | 不需要 | 静态文件直接读 |

改 `trusted_proxies.txt` 后：

```bash
docker compose restart tikuhelper nginx
```

**为什么必须重启两个而不是只 reload nginx**：`trusted_proxies.txt` 被两个容器读取，语义不同 ——
后端用它决定信任哪些转发头，nginx 用它生成 `set_real_ip_from`。只 reload nginx 会造成两侧不一致，
表现为「nginx 日志里 IP 对，应用日志里 IP 是代理地址」，极难排查。

配置写错时**服务不会挂**：`nginx -t` 不通过就跳过 reload，旧配置继续生效，
容器日志里会出现 `reload skipped: nginx -t failed` 与具体报错行。

部署拓扑、反代与数据库运维细节属于内部资料，不在本仓库内。
