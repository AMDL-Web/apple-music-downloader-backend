#!/bin/sh
# 容器入口:把镜像内置的示例配置播种到配置卷,然后以目标用户启动后端。
# 只在目标文件不存在时写入,已有配置(含 PUT /api/v1/config 重写的
# config.yaml)永远不会被覆盖。config.yaml 本身由后端首次启动时从同目录
# 的 config.example.yaml 引导生成,入口脚本不再改写它。
#
# 运行用户:镜像默认以 root 启动本脚本,播种并修正卷属主后通过 su-exec
# 降权到 PUID:PGID(默认 1000:1000)运行后端。用 docker run --user(或
# compose 的 user:)以非 root 启动时,PUID/PGID 被忽略,跳过属主修正,
# 直接以该用户运行。
set -eu

CONFIG_PATH="${AMDL_CONFIG:-/app/configs/config.yaml}"
CONFIG_DIR="$(dirname "$CONFIG_PATH")"
HOOKS_PATH="${AMDL_HOOKS_CONFIG:-/app/configs/hooks.yaml}"
DIST_DIR="/opt/amdl"
PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# 容器默认值:两个仓库默认值在容器里不可用,通过后端的
# AMDL_<大写段名>_<大写键名> 环境变量覆盖机制在每次启动时改写:
#   - server.listen   默认 127.0.0.1:18080,容器外无法访问,改为 :18080
#   - wrapper.address 默认 127.0.0.1:8080,指向容器自身,改为宿主机
# 环境变量优先于 config.yaml 且不写回文件。
export AMDL_SERVER_LISTEN="${AMDL_SERVER_LISTEN:-:18080}"
export AMDL_WRAPPER_ADDRESS="${AMDL_WRAPPER_ADDRESS:-host.docker.internal:8080}"

mkdir -p "$CONFIG_DIR"

# 后端首次启动的 bootstrap 逻辑要求 config.example.yaml 与 config.yaml
# 同目录,所以示例文件也要进配置卷(同时充当字段文档)。
if [ ! -f "$CONFIG_DIR/config.example.yaml" ]; then
    cp "$DIST_DIR/config.example.yaml" "$CONFIG_DIR/config.example.yaml"
fi

# hooks.yaml 缺失时后端只是禁用 hooks,播种一份注释完整的模板方便编辑。
if [ ! -f "$HOOKS_PATH" ]; then
    mkdir -p "$(dirname "$HOOKS_PATH")"
    cp "$DIST_DIR/hooks.yaml" "$HOOKS_PATH"
fi

# 以 root 启动时修正卷属主并降权;以 --user 指定的非 root 用户启动时
# 直接运行(此时卷属主需自行保证可写)。
if [ "$(id -u)" = "0" ]; then
    # 配置目录很小,每次启动整体修正,PUID/PGID 变更后立即生效。
    chown -R "$PUID:$PGID" "$CONFIG_DIR"
    if [ -f "$HOOKS_PATH" ]; then
        chown "$PUID:$PGID" "$HOOKS_PATH"
    fi
    # 数据目录逐个挂载点检查:绑定挂载的宿主机目录首次由 Docker 创建时
    # 属主是 root,而下载目录可能包含大量文件,所以只在顶层属主不匹配时
    # 递归修正一次(对应首次挂载或 PUID/PGID 变更的情况)。
    for dir in /app/data /app/data/db /app/data/logs /app/data/downloads; do
        mkdir -p "$dir"
        if [ "$(stat -c '%u:%g' "$dir")" != "$PUID:$PGID" ]; then
            echo "fixing ownership of $dir to $PUID:$PGID"
            chown -R "$PUID:$PGID" "$dir"
        fi
    done
    exec su-exec "$PUID:$PGID" /app/amdl-api "$@"
fi

exec /app/amdl-api "$@"
