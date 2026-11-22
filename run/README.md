# 部署

本目录是可直接运行的部署样例（`config.yaml` + `docker-compose.yml` + nginx 反代）。

证书放进 `nginx/ssl/`（说明见该目录 README），按 `docker-compose.yml` 顶部注释准备环境变量与凭据，然后：

```bash
cd run && docker compose up -d --build
```

容器内 `/app/data` 是持久化目录：数据库须落在其中，否则重启即丢。只挂载这一个目录即可，不影响程序文件。

首次登录：内置账号为 `admin`，首次建库优先使用 `TIKU_ADMIN_PASSWORD`；设置非公共密码后可直接登录。不设置则默认 `123456`，不会生成随机首密或密码文件。默认及显式 `changeme` 只能用来在本机初始化：在本目录执行 `docker compose exec tikuhelper /app/tiku-adapter --init-admin-password`，按提示输入当前初始化口令、新密码并确认（不回显），然后使用新密码网页登录。初始化不依赖代理或转发 IP；重启与升级不会重置既有密码。

工单图片始终启用，图片仍会加密保存并经过内容检测。首次启动自动生成图片加密密钥并随数据库保存，无需另配开关或环境变量。备份时须完整备份数据库，勿删除或修改 `system_settings` 中的 `ticket_attachment_encryption_key`。已有部署的 `TIKU_TICKET_ATTACHMENT_KEY`（或兼容的 `TIKU_REFUND_PROOF_KEY`）会作为初始密钥保存；保存后即使取消该环境变量也能读取原图。修改环境密钥会因与已保存密钥不一致而拒绝启动，避免历史图片失效。

## 网关与热更新

镜像内的 `/app/container-gateway` 同时托管 Nginx 和 Go。Go 仅监听 `127.0.0.1:8061`；Nginx 负责宿主回环入口 `8060`、HTTP `8680` 和有证书时的 HTTPS `9043`。初始化管理员仍执行 `docker compose exec tikuhelper /app/tiku-adapter --init-admin-password`。直接运行 Go 二进制的默认端口仍为 8060。

| 改了文件 | 是否需要重启 | 行为 |
|---|---|---|
| `nginx/conf.d/*.conf`、`*.inc` | 不需要 | 每 3 秒复制完整候选配置，`nginx -t` 成功后 reload；失败保留旧版本 |
| `nginx/ssl/` | 不需要 | 递归按 PEM 内容与 SAN 匹配证书/密钥；使用私有快照，不依赖源文件持续完整 |
| `trusted_proxies.txt`、`TRUSTED_PROXIES`、`TRUSTED_PROXIES_FILE` | 需要重启 `tikuhelper` | 入口来源策略在启动时读取；仅挂载代理文件不会自动选择该文件 |
| `nginx/html/*` | 不需要 | 静态文件直接读取 |

`TRUSTED_PROXIES` 留空或为 `none` 时保留现有客户端 IP 规则：读取任意来源的 X-Forwarded-For。填写具体 IP/CIDR 时仍补充已有的本地与私网范围。HTTPS 协议头只接受明确填写的上游 IP/CIDR，未受信来源不能伪造 HTTPS；直接访问 9043 时始终按 TLS 判定。Go 只信任本容器回环 Nginx 的转发头。

首次没有有效证书时 HTTP 正常运行、9043 不监听，日志及健康检查输出会标明“HTTPS 未启用”。后续放入有效证书会自动启用 HTTPS；源文件暂时缺失/损坏时保留仍有效的快照，快照全部过期后关闭 TLS。Nginx 的配置检查与实际 worker 代际切换都成功才接受候选版本。

健康检查同时验证活动 Nginx 配置、直连 Go 的 `/readyz` 和经 Nginx 的 `/readyz`，因此数据库或代理不可用会显示 unhealthy。HTTP 健康通过只表示 HTTP 可用，TLS 状态在健康输出中单独显示。任一子进程退出会终止整个容器，由 `restart: unless-stopped` 恢复；正常停止会先排空 Nginx，再结束 Go。

升级前备份 `data/`、`logs/` 和 `nginx/ssl/`，保留原有卷目录。旧版双容器部署升级前停止并移除旧 `nginx` 服务，避免占用 8680/9043：`docker compose up -d --build --remove-orphans`。网关不修改数据库内容。

部署回归可执行 `node --test test/deployment-nginx.test.mjs`；设置 `TIKU_GATEWAY_TEST_IMAGE` 为待测镜像标签后，还会创建独立临时容器验证代理、TLS 与热更新，不使用本目录的数据或现有容器。
