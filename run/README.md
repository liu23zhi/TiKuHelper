# 部署

本目录是可直接运行的部署样例（`config.yaml` + `docker-compose.yml` + nginx 反代）。

证书放进 `nginx/ssl/`（说明见该目录 README）。本部署文件不读取宿主机的 `${...}` 插值；可选参数直接编辑 `docker-compose.yml` 的空值，再执行：

```bash
cd run && docker compose up -d --build
```

容器内 `/app/data` 是持久化目录：数据库须落在其中，否则重启即丢。只挂载这一个目录即可，不影响程序文件。

首次登录：内置账号为 `admin`，首次建库优先使用 `TIKU_ADMIN_PASSWORD`；设置非公共密码后可直接登录。不设置则默认 `123456`，不会生成随机首密或密码文件。默认及显式 `changeme` 只能用来在本机初始化：在本目录执行 `docker compose exec tikuhelper /app/tiku-adapter --init-admin-password`，按提示输入当前初始化口令、新密码并确认（不回显），然后使用新密码网页登录。初始化不依赖代理或转发 IP；重启与升级不会重置既有密码。

## Compose 可选参数

Compose 中保留这些变量名是为了让服务读取统一配置，但值默认直接写成空字符串；不要在命令行或 `.env` 中期待自动替换。

| 配置 | 是否必填 | 空值行为 | 手动填写限制 |
|---|---|---|---|
| `TIKU_APP_MODE` | 否 | 等同 `production` | 只能填 `production` 或 `development`；不区分大小写。 |
| `JWT_SECRET` | 否 | 从数据库复用密钥；没有时生成随机密钥并持久化 | 至少 32 个 UTF-8 字节；没有最大长度和字符集限制，可用特殊符号。首尾空白会裁剪；更换后旧 JWT 失效。 |
| `TIKU_ADMIN_PASSWORD` | 否 | 使用本地初始化口令 `123456` | 最多 72 个 UTF-8 字节，可用特殊符号。生产模式建议至少 8 个字符并包含大小写、数字、特殊字符；不满足时记录警告但仍启动。已有账号密码不会被环境值重置。 |
| `TRUSTED_PROXIES` | 否 | 与文件都无有效内容时信任所有 `X-Forwarded-For` 来源 | 非空时优先于文件；填写逗号分隔的 IP/CIDR。`none` 与空值相同，不能用来表达“完全不信任”。 |
| `TRUSTED_PROXIES_FILE` | 否 | 空值读取 `/app/trusted_proxies.txt` | 可填写容器内文件路径。空值对应的默认文件不存在按空文件处理；明确填写的路径不存在会拒绝启动。文件为空且 `TRUSTED_PROXIES` 为空时信任所有；文件含不可解析条目会拒绝启动。 |
| `TZ` | 否 | 固定 `Asia/Shanghai` | 本部署不从宿主环境读取；需要其它时区时直接修改 Compose 文件。 |

可信代理的优先级是 `TRUSTED_PROXIES` 非空值，然后是 `TRUSTED_PROXIES_FILE` 指向的文件，最后才是空配置的全信任回退。`TRUSTED_PROXIES` 配置具体 IP/CIDR 时，服务端仍会补入本地、Docker 和私网段；这些设置只影响来源 IP 和转发协议判断，不会改变脚本域名禁止规则。

工单图片始终启用，图片仍会加密保存并经过内容检测。首次启动自动生成图片加密密钥并随数据库保存，无需另配开关或环境变量。备份时须完整备份数据库，勿删除或修改 `system_settings` 中的 `ticket_attachment_encryption_key`。已有部署的 `TIKU_TICKET_ATTACHMENT_KEY`（或兼容的 `TIKU_REFUND_PROOF_KEY`）会作为初始密钥保存；保存后即使取消该环境变量也能读取原图。修改环境密钥会因与已保存密钥不一致而拒绝启动，避免历史图片失效。

## 网关与热更新

镜像内的 `/app/container-gateway` 同时托管 Nginx 和 Go。Go 仅监听 `127.0.0.1:8061`；Nginx 负责宿主回环入口 `8060`、HTTP `8680` 和有证书时的 HTTPS `9043`。初始化管理员仍执行 `docker compose exec tikuhelper /app/tiku-adapter --init-admin-password`。直接运行 Go 二进制的默认端口仍为 8060。

| 改了文件 | 是否需要重启 | 行为 |
|---|---|---|
| `nginx/conf.d/*.conf`、`*.inc` | 不需要 | 每 3 秒复制完整候选配置，`nginx -t` 成功后 reload；失败保留旧版本 |
| `nginx/ssl/` | 不需要 | 递归按 PEM 内容与 SAN 匹配证书/密钥；使用私有快照，不依赖源文件持续完整 |
| `trusted_proxies.txt`、`TRUSTED_PROXIES`、`TRUSTED_PROXIES_FILE` | 需要重启 `tikuhelper` | 入口来源策略在启动时读取；文件路径为空时默认读取 `/app/trusted_proxies.txt` |
| `nginx/html/*` | 不需要 | 静态文件直接读取 |

`TRUSTED_PROXIES` 非空时优先于文件。它留空且 `TRUSTED_PROXIES_FILE` 为空时，服务先尝试读取工作目录默认文件 `trusted_proxies.txt`（容器内为 `/app/trusted_proxies.txt`）；默认文件不存在或为空，才回退为读取任意来源的 `X-Forwarded-For`。如果明确填写了文件路径，该路径不存在会拒绝启动。填写具体 IP/CIDR 时仍补充已有的本地与私网范围；文件含不可解析条目会拒绝启动。HTTPS 协议头只接受明确填写的上游 IP/CIDR，未受信来源不能伪造 HTTPS；直接访问 9043 时始终按 TLS 判定。Go 只信任本容器回环 Nginx 的转发头。

首次没有有效证书时 HTTP 正常运行、9043 不监听，日志及健康检查输出会标明“HTTPS 未启用”。后续放入有效证书会自动启用 HTTPS；源文件暂时缺失/损坏时保留仍有效的快照，快照全部过期后关闭 TLS。Nginx 的配置检查与实际 worker 代际切换都成功才接受候选版本。

健康检查同时验证活动 Nginx 配置、直连 Go 的 `/readyz` 和经 Nginx 的 `/readyz`，因此数据库或代理不可用会显示 unhealthy。HTTP 健康通过只表示 HTTP 可用，TLS 状态在健康输出中单独显示。任一子进程退出会终止整个容器，由 `restart: unless-stopped` 恢复；正常停止会先排空 Nginx，再结束 Go。

升级前备份 `data/`、`logs/` 和 `nginx/ssl/`，保留原有卷目录。旧版双容器部署升级前停止并移除旧 `nginx` 服务，避免占用 8680/9043：`docker compose up -d --build --remove-orphans`。网关不修改数据库内容。

部署回归可执行 `node --test test/deployment-nginx.test.mjs`；设置 `TIKU_GATEWAY_TEST_IMAGE` 为待测镜像标签后，还会创建独立临时容器验证代理、TLS 与热更新，不使用本目录的数据或现有容器。
