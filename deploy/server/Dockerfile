# Build from the repository root, which is the build context:
#   docker build -f deploy/server/Dockerfile .
#
# The Dockerfile uses no BuildKit-only syntax, so the classic builder (a
# docker build without buildx, as on Synology DSM) builds it too, for the host
# platform. Multi-arch images need buildx.

# ---- Build stage -------------------------------------------------------
# Under BuildKit, cross-compilation runs on the build platform, so buildx can
# produce linux/amd64 and linux/arm64 images directly. The classic builder sets
# no BUILDPLATFORM and would fail on an empty --platform; "linux" alone means
# linux on the daemon's own architecture there, and TARGETOS/TARGETARCH stay
# empty, so Go builds for the host.
FROM --platform=${BUILDPLATFORM:-linux} golang:1.27.3-alpine AS build

WORKDIR /src

# Go module proxy; the default is Go's own. Where proxy.golang.org is
# unreachable, pass another, e.g. --build-arg GOPROXY=https://goproxy.cn,direct.
# go.sum pins every module, so the checksum database is not consulted.
ARG GOPROXY=https://proxy.golang.org,direct

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
# Build identity reported by GET /api/v1/system. .dockerignore keeps .git out
# of the context, so these build args are its only source; without them the
# image reports version dev and no commit.
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
# modernc.org/sqlite is pure Go, so CGO_ENABLED=0 produces a static binary.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X amdl/internal/buildinfo.Version=${VERSION} -X amdl/internal/buildinfo.Commit=${COMMIT} -X amdl/internal/buildinfo.BuildDate=${BUILD_DATE}" \
      -o /out/amdl ./apps/amdl

# ---- Runtime stage -----------------------------------------------------
FROM alpine:3.24

# ffmpeg: remux flattening and the optional integrity check (tools.ffmpeg; the
#   default command name works as is).
# ca-certificates: HTTPS requests to the Apple Music API.
# tzdata: time zone support for log and hook timestamps.
# su-exec: the entrypoint runs as root to give the backend's own state to
#   PUID:PGID, then drops to that user to run the backend.
# ALPINE_MIRROR replaces https://dl-cdn.alpinelinux.org/alpine in the package
# repositories (and stays in the image's /etc/apk/repositories); the default
# leaves them unchanged. Where dl-cdn is unreachable, pass a mirror, e.g.
# --build-arg ALPINE_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/alpine.
ARG ALPINE_MIRROR=https://dl-cdn.alpinelinux.org/alpine
RUN sed -i "s#https\{0,1\}://dl-cdn\.alpinelinux\.org/alpine#${ALPINE_MIRROR}#" /etc/apk/repositories \
    && apk add --no-cache ca-certificates ffmpeg tzdata su-exec \
    && addgroup -g 1000 amdl \
    && adduser -D -u 1000 -G amdl amdl

WORKDIR /app

# The example configs are baked into /opt/amdl; the entrypoint copies them
# into the config directory /app/configs as the documentation and the hooks
# template users edit. config.yaml itself the backend creates from the copy of
# the example built into the binary.
COPY configs/config.example.yaml configs/hooks.example.yaml /opt/amdl/
COPY deploy/server/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY --from=build /out/amdl /app/amdl

# Pre-create the config and data directories and hand them to the default
# runtime user (uid 1000). The container starts the entrypoint as root; the
# script gives the backend's own state (config files, database, data/tmp, never
# data/downloads) to PUID:PGID (default 1000:1000) and then drops privileges to
# run the backend. A non-root user can also be set directly with docker run
# --user (compose's user:), in which case the privilege drop is skipped. Any
# such uid must be able to create files in the directories that are not
# mounted, data/tmp above all, so they are world-writable with the sticky bit,
# like /tmp. A mount replaces them with the host directory and its permissions.
# The entrypoint is made executable here rather than with COPY --chmod, which
# needs BuildKit.
RUN chmod 755 /usr/local/bin/docker-entrypoint.sh \
    && mkdir -p /app/configs /app/data/db /app/data/tmp /app/data/downloads \
    && chown -R amdl:amdl /app \
    && chmod 1777 /app/configs /app/data /app/data/db /app/data/tmp /app/data/downloads

EXPOSE 18080

# The port follows AMDL_SERVER_LISTEN (the environment override of
# server.listen, applied on every start, default :18080). To change the listen
# port, change that environment variable; the health check derives its port
# from it.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD p="${AMDL_SERVER_LISTEN:-:18080}"; wget -qO /dev/null "http://127.0.0.1:${p##*:}/api/v1/health" || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
