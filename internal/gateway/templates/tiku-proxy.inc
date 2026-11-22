proxy_http_version 1.1;
proxy_set_header Host              $http_host;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $remote_addr;
proxy_set_header Forwarded         "";
proxy_set_header X-Forwarded-Proto $tiku_forwarded_proto;
proxy_set_header X-Forwarded-Port  $server_port;

proxy_set_header Upgrade    $http_upgrade;
proxy_set_header Connection $connection_upgrade;

proxy_connect_timeout 60s;
proxy_send_timeout    3600s;
proxy_read_timeout    3600s;
proxy_buffering       off;
proxy_request_buffering off;
client_max_body_size 200m;
