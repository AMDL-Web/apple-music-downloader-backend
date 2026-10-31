#!/bin/sh
# Container entrypoint: syncs the example configs baked into the image to the
# config volume, then starts the backend as the target user. config.yaml is
# bootstrapped by the backend on first start from config.example.yaml in the
# same directory and may be rewritten in full by PUT /api/v1/config; this
# script never rewrites it.
#
# Runtime user: by default the image runs this script as root; after seeding
# and re-owning the backend's own state it drops to PUID:PGID (default
# 1000:1000) with su-exec to run the backend. When started as a non-root user
# with docker run --user (or compose's user:), PUID/PGID are ignored, the
# ownership fix is skipped, and the backend runs directly as that user.
set -eu

CONFIG_PATH="${AMDL_CONFIG:-/app/configs/config.yaml}"
CONFIG_DIR="$(dirname "$CONFIG_PATH")"
HOOKS_PATH="${AMDL_HOOKS_CONFIG:-/app/configs/hooks.yaml}"
DIST_DIR="/opt/amdl"
PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# Container defaults: two of the repository defaults do not work inside a
# container, so they are replaced on every start through two of the backend's
# environment overrides:
#   - server.listen   defaults to 127.0.0.1:18080, unreachable from outside
#                     the container; changed to :18080
#   - wrapper.address defaults to http://127.0.0.1:12340, which points at
#                     the container itself; changed to the host
# Environment variables take precedence over config.yaml and are not written
# back to the file.
export AMDL_SERVER_LISTEN="${AMDL_SERVER_LISTEN:-:18080}"
export AMDL_WRAPPER_ADDRESS="${AMDL_WRAPPER_ADDRESS:-http://host.docker.internal:12340}"

mkdir -p "$CONFIG_DIR"

# config.example.yaml is field documentation that changes with each release,
# not user configuration; it is synced from the image on every start so that
# installs upgraded from the old two-file layout do not keep seeing an old
# example that lacks the runtime fields.
cp "$DIST_DIR/config.example.yaml" "$CONFIG_DIR/config.example.yaml"

# Without hooks.yaml the backend merely disables hooks; seed a fully commented
# template to make editing easy.
if [ ! -f "$HOOKS_PATH" ]; then
    mkdir -p "$(dirname "$HOOKS_PATH")"
    cp "$DIST_DIR/hooks.example.yaml" "$HOOKS_PATH"
fi

# own_path PATH...: gives each existing PATH (not its contents) to PUID:PGID,
# skipping one that already has that owner. Symlinks are not followed.
own_path() {
    for p in "$@"; do
        if [ -e "$p" ] || [ -L "$p" ]; then
            find "$p" -maxdepth 0 \( ! -user "$PUID" -o ! -group "$PGID" \) \
                -exec chown -h "$PUID:$PGID" {} +
        fi
    done
}

# own_tree DIR: gives DIR and everything below it to PUID:PGID, skipping
# entries that already have that owner. It stays on DIR's filesystem: -xdev
# keeps find out of other mounts, and mount points below DIR are pruned too,
# since -xdev alone would still re-own the mount point's own directory.
own_tree() {
    [ -d "$1" ] || return 0
    root="$1"
    set --
    for m in $(awk -v d="$root/" 'index($5, d) == 1 { print $5 }' /proc/self/mountinfo); do
        set -- "$@" -path "$m" -prune -o
    done
    find "$root" -xdev "$@" \( ! -user "$PUID" -o ! -group "$PGID" \) \
        -exec chown -h "$PUID:$PGID" {} +
}

# When started as root, re-own the backend's own state and drop privileges;
# when started as a non-root user given by --user, run directly (the mounts
# must then be writable by that user beforehand).
#
# Only paths the backend creates are re-owned: the config directory itself and
# its config files, the database directory and files, and the temp directory
# data/tmp. The downloads directory is never touched: it is usually a
# bind-mounted music library, and it must already be writable by PUID:PGID.
# The backend writes no log file, so there are no logs to re-own.
if [ "$(id -u)" = "0" ]; then
    DOWNLOADS_DIR=/app/data/downloads
    DB_PATH="${AMDL_DATABASE_PATH:-data/db/amdl.db}"
    case "$DB_PATH" in
        /*) ;;
        *) DB_PATH="/app/$DB_PATH" ;;
    esac
    DB_DIR="$(dirname "$DB_PATH")"

    mkdir -p /app/data/tmp "$DB_DIR"
    own_path /app/data "$CONFIG_DIR" "$CONFIG_PATH" \
        "$CONFIG_DIR/config.example.yaml" "$HOOKS_PATH"
    case "$DB_DIR" in
        "$DOWNLOADS_DIR" | "$DOWNLOADS_DIR"/*) ;;
        *) own_path "$DB_DIR" ;;
    esac
    own_path "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm" "$DB_PATH-journal"
    own_tree /app/data/tmp

    if [ -d "$DOWNLOADS_DIR" ] && ! su-exec "$PUID:$PGID" test -w "$DOWNLOADS_DIR"; then
        echo "warning: $DOWNLOADS_DIR is not writable by $PUID:$PGID; give the host directory to that uid/gid (docs/deployment.md, Ownership)" >&2
    fi
    exec su-exec "$PUID:$PGID" /app/amdl serve "$@"
fi

exec /app/amdl serve "$@"
