# 内置 Nginx 模板说明

网关的正式基础配置已编译进 `/app/container-gateway`，运行时写入私有生成目录并通过 `nginx -t` 校验。此目录中的两个文件只保留为源码参考，不再挂载到容器的 `/etc/nginx/conf.d`，也不会覆盖镜像内置模板。

运维需要增加完整 `server` 或 HTTP 片段时，请把 `.conf` 或 `.inc` 文件放入 `../nginx-extra/`（Compose 中映射到 `/app/nginx-extra`）。网关会逐个检查；冲突保留项或语法错误文件会记录并跳过，其他文件和基础配置继续生效。
