# syntax=docker/dockerfile:1

# ---- 构建阶段 ----------------------------------------------------------
# 交叉编译在构建平台上进行(--platform=$BUILDPLATFORM),配合 buildx
# 可以直接产出 linux/amd64 与 linux/arm64 镜像。
FROM --platform=$BUILDPLATFORM golang:1.25.12-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
# modernc.org/sqlite 是纯 Go 实现,CGO_ENABLED=0 产出静态二进制。
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/amdl-api ./cmd/amdl-api

# ---- 运行阶段 ----------------------------------------------------------
FROM alpine:3.22

# ffmpeg:重封装扁平化与可选完整性校验(tools.ffmpeg,默认命令名即可)。
# ca-certificates:访问 Apple Music API 的 HTTPS 请求。
# tzdata:日志与 hooks 时间戳的时区支持。
# su-exec:入口脚本以 root 修正卷属主后降权到 PUID:PGID 运行后端。
RUN apk add --no-cache ca-certificates ffmpeg tzdata su-exec \
    && addgroup -g 1000 amdl \
    && adduser -D -u 1000 -G amdl amdl

WORKDIR /app

# 示例配置内置到 /opt/amdl,入口脚本首次启动时播种到配置目录 /app/configs。
COPY configs/config.example.yaml configs/hooks.yaml /opt/amdl/
COPY --chmod=755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY --from=build /out/amdl-api /app/amdl-api

# 预建配置与数据目录并交给默认运行用户(uid 1000)。容器以 root 启动
# 入口脚本,由脚本按 PUID/PGID(默认 1000:1000)修正挂载目录属主后降权
# 运行后端;也可用 docker run --user 直接指定非 root 用户,此时跳过
# 降权逻辑。
RUN mkdir -p /app/configs /app/data && chown -R amdl:amdl /app

EXPOSE 18080

# 端口跟随 AMDL_SERVER_LISTEN(环境变量覆盖 server.listen,每次启动生效,
# 默认 :18080)。改监听端口请直接改该环境变量,健康检查从它推导端口。
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD p="${AMDL_SERVER_LISTEN:-:18080}"; wget -qO /dev/null "http://127.0.0.1:${p##*:}/api/v1/health" || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
