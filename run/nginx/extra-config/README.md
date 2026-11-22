# 可选 Nginx 扩展

此目录默认为空，Compose 将其只读映射到容器 `/app/nginx-extra`。网关始终使用镜像内置基础模板；这里的 `.conf` 和 `.inc` 文件作为可选 HTTP 配置扩展参与候选生成。

扩展不能重新定义 `tiku_adapter`、`tiku_safe`、`map $http_upgrade`、`/api/Wechat_qc_code`，也不能声明生成的 `8060`、`8680`、`9043` 默认监听或 `server_name _` 捕获站点。冲突和单文件语法错误只会跳过对应文件，不会阻止基础配置启动。
