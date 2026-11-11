# 两个 server 段共用的反向代理参数（被 tiku-adapter.conf include）。
# 文件名不以 .conf 结尾：官方镜像只会 include conf.d/*.conf，不会被重复加载。

proxy_http_version 1.1;
proxy_set_header Host              $http_host;
# 对外入口重建来源头，不能保留客户端自带的 XFF 链（后端空代理名单默认全信任）。
# 多层代理应先用 set_real_ip_from 校验上游并还原 $remote_addr，见 tiku-adapter.conf。
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $remote_addr;
proxy_set_header Forwarded         "";
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Port  $server_port;

# WebSocket 必需的两行：升级请求带 Connection: upgrade，普通请求保持空值
proxy_set_header Upgrade    $http_upgrade;
proxy_set_header Connection $connection_upgrade;

proxy_connect_timeout 60s;
proxy_send_timeout    3600s;
proxy_read_timeout    3600s;
proxy_buffering         off;
proxy_request_buffering off;

# 工单图片单张 20 MB（Q35 定案），但这里限制的是**整个请求体**，不是单张。
# 多图走 multipart 时总大小会叠加，所以不能设成 20m —— 那意味着多图必然 413。
# 200m 是给「数量不限制」留的总量余量，实际单张仍由后端校验 20 MB。
#
# ⚠️ 本值必须 ≥ 后端 BodyLimit（internal/api/router.go 的全局 BodyLimit），
# 否则 nginx 会先返回 413，应用层那条更友好的错误提示永远出不来。
client_max_body_size 200m;
