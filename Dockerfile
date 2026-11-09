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

# 创建数据目录用于持久化数据库文件
RUN mkdir -p /app/data

EXPOSE 8060
WORKDIR /app

# 声明数据卷用于持久化
VOLUME ["/app/data"]

ENTRYPOINT ["/app/tiku-adapter"]
