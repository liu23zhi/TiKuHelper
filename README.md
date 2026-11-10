# Tiku Helper

[![持续集成](https://github.com/liu23zhi/tikuHelper/actions/workflows/ci.yml/badge.svg)](https://github.com/liu23zhi/tikuHelper/actions/workflows/ci.yml)

把多个题库渠道聚合成一个统一的搜题接口，自带账户与计费。

## 三步跑起来

**1. 准备配置**

```bash
cp configs/config.yaml ./config.yaml
```

**2. 构建并启动**

```bash
docker build -t tiku-adapter .
docker run -d --name tiku-adapter -p 8060:8060 \
  -v ./config.yaml:/app/config.yaml:ro \
  -v ./data:/app/data \
  tiku-adapter
```

首次启动需提供初始化凭据，取值要求见启动日志提示。

**3. 用浏览器打开服务地址**，按页面引导完成初始化。

## 开发

```bash
go build ./...
go test -short -p 1 ./... -count=1
```

前端源码在 `web/`。

详细部署与配置见项目内部文档。
