# Tiku Helper

[![持续集成](https://github.com/liu23zhi/TiKuHelper/actions/workflows/ci.yml/badge.svg)](https://github.com/liu23zhi/TiKuHelper/actions/workflows/ci.yml)

内部项目，不接受外部贡献。

## 构建

```bash
go build ./...
```

## 本地 gofmt 检查

Windows 上 `core.autocrlf=true` 会让工作树是 CRLF，`gofmt -l` 因此误报「未格式化」，
而 CI 在 LF 下通过。用下面这条区分「真格式问题」与「CRLF 假阳性」：

```powershell
.\scripts\local-gofmt.ps1        # REAL=须修 / CRLF-ONLY=无需修 / CLEAN=合规
.\scripts\local-gofmt.ps1 -Fix   # 修 REAL，写回 LF
```

