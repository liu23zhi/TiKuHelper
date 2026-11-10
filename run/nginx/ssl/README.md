# 证书目录

把 TLS 证书放这里：

- `fullchain.pem`：证书链
- `privkey.key`：私钥

该目录以只读方式挂载到容器的 nginx 证书路径。启用 https 时按 `conf.d/` 内的配置对应文件名放置。
