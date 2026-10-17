# 两个 server 段共用的反向代理参数（被 tiku-adapter.conf include）。
# 文件名不以 .conf 结尾：官方镜像只会 include conf.d/*.conf，不会被重复加载。

proxy_http_version 1.1;
proxy_set_header Host              $http_host;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
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

# 题面/选项极端情况下请求体可能偏大（nginx 默认 1m，超限会返回 413）
client_max_body_size 8m;
