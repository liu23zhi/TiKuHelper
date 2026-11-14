FROM node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS builder1

WORKDIR /build
COPY ./web .
RUN npm ci && npm run build


FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS builder2
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux

WORKDIR /build
ADD go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=builder1 /build/dist ./cmd/adapter-service/dist
RUN go build -ldflags "-s -w -extldflags '-static'" -o tiku-adapter ./cmd/adapter-service

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

RUN apk update \
    && apk upgrade \
    && apk add --no-cache ca-certificates tzdata \
    && update-ca-certificates

# 时区固定上海：镜像默认 TZ=UTC 会让 Gin 访问日志/GORM 时间变 UTC，
# 与业务日志（固定 +8）差 8 小时。程序启动时也会强制 time.Local。
ENV TZ=Asia/Shanghai

COPY --from=builder2 /build/tiku-adapter /app/tiku-adapter

# --- 考试客户端 DB 脱机工具（管理员专用）内嵌到镜像 -------------------------
#
# 这个 exe 是 PyInstaller 打的**单文件 Windows PE**（约 14.8 MB，自带 Python
# 运行时与 SQLite3MC 原生扩展），管理员下载到本机双击即可用，无需装 Python。
# 服务端从不执行它，只把它原样发出去（controller/user_exam_tool.go:137
# `http.ServeContent`），所以宿主是 alpine/Linux 也不影响。
#
# 它**不进 git**：.gitignore:56 排除 tools/userexam-crypto/dist/*（15 MB 的
# 二进制放进去会永久污染历史，且每次重建都产生新 blob）。因此镜像构建前必须
# 先在宿主机跑一次 `tools/userexam-crypto/build-exe.ps1` 产出该文件。
#
# 缺失时的处置由 ARG WITH_USEREXAM_TOOL 决定（默认 1 = 要求存在）：
#
#   1（默认，本地/发布构建）→ 缺 exe 直接失败，不产出「管理台显示未部署」的假成功镜像。
#   0（CI 的 docker 作业）  → 跳过内嵌，镜像照常构建，管理台如实显示「未部署脱机工具」。
#
# 为什么需要 0：exe 由 PyInstaller 在 **Windows** 上构建（build-exe.ps1 明确要求
# 64 位 CPython 3.11 + PyInstaller 6.x），Linux runner 造不出来；而 exe 又被
# .gitignore 排除，checkout 后必然不存在。若只有「必须有」一种行为，CI 的 docker
# 作业会永久红 —— 那是假红，比跳过更糟（会训练人忽略这条）。
ARG WITH_USEREXAM_TOOL=1

# COPY 不能条件化，所以用 shell 完成三件事：判断、复制、权限。
# 目录先建好，保证 ARG=0 时 ENV 指向的路径存在性检查不会误报（文件仍不存在的语义正确）。
RUN --mount=type=bind,source=tools/userexam-crypto/dist,target=/src,ro \
    if [ "$WITH_USEREXAM_TOOL" = "1" ]; then \
        test -s /src/userexam-crypto.exe || { \
            echo "缺少脱机工具：请先运行 tools/userexam-crypto/build-exe.ps1 生成 dist/userexam-crypto.exe，" >&2; \
            echo "或显式声明 --build-arg WITH_USEREXAM_TOOL=0 以跳过内嵌（管理台会如实显示未部署）。" >&2; \
            exit 1; \
        }; \
        mkdir -p /app && cp /src/userexam-crypto.exe /app/userexam-crypto.exe \
        && chmod 0755 /app/userexam-crypto.exe; \
    else \
        echo "WITH_USEREXAM_TOOL=0：跳过内嵌脱机工具，管理台将显示「未部署脱机工具」。" >&2; \
    fi

# USEREXAM_TOOL_PATH 指向镜像内路径后，管理台「未部署脱机工具（未配置
# USEREXAM_TOOL_PATH）」的提示消失，变为「已部署 + 版本号 + SHA-256」。
# 运维仍可用 `-e USEREXAM_TOOL_PATH=/…` 覆盖成自己构建的 exe
# （controller/user_exam_tool.go:35 里 os.Getenv 优先，环境变量覆盖有效）。
# 这里不需要让路径跟着 WITH_USEREXAM_TOOL 走：
#
# describeUserExamTool（controller/user_exam_tool.go:116-131）的判定是
#   路径为空        → Reason="未配置 USEREXAM_TOOL_PATH"，Available=false
#   os.Open 失败    → Reason="脱机工具不可读"，Available=false
# 两种情况都是 Available=false，前端都会如实显示「未部署」。所以 ARG=0 时
# 路径指向不存在的文件**不会**造成假「已部署」——判定读的是文件本身，不是路径非空。
ENV USEREXAM_TOOL_PATH=/app/userexam-crypto.exe

# --- favicon：默认用前端内嵌版本，外部覆盖需显式启用 ------------------------
#
# web/public/favicon.ico 经 `npm run build` → web/dist/favicon.ico →
# builder2 的 `//go:embed dist` 编进二进制。运行时读的是**嵌入树**，所以
# 单纯把文件挂进容器（-v）或在这里 COPY 都**不会**生效。
#
# 真正的覆盖通道是环境变量 FAVICON_PATH：服务端启动时读它，若文件可读就
# 优先返回磁盘版本（internal/api/router.go 的 serveFavicon）。默认不设置
# = 使用内嵌 favicon.ico，与「默认注释掉」的要求一致。
#
# 启用方式（取消下面两行注释并重新构建）：
#   COPY favicon.ico /app/data/favicon.ico
#   ENV FAVICON_PATH=/app/data/favicon.ico
#
# 或在运行时挂载（无需重建镜像）：
#   docker run -v /host/my-favicon.ico:/app/data/favicon.ico \
#              -e FAVICON_PATH=/app/data/favicon.ico ...
# ENV FAVICON_PATH=/app/data/favicon.ico

# 创建数据目录用于持久化数据库文件
RUN mkdir -p /app/data

EXPOSE 8060
WORKDIR /app

# 声明数据卷用于持久化
VOLUME ["/app/data"]

ENTRYPOINT ["/app/tiku-adapter"]
