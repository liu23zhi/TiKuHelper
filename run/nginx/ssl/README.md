# 证书目录

把 TLS 证书放这里，文件名与 `conf.d/tiku-adapter.conf` 里的 9043 端口配置一致：

- `fullchain.pem`：证书链
- `privkey.key`：私钥

该目录以只读方式挂载到容器的 `/etc/nginx/ssl`。不需要 https 时可以删掉 9043 那个
`server` 段，以及 compose 里的 `- "9043:9043"` 端口映射。
