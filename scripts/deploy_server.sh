#!/usr/bin/env bash
set -Eeuo pipefail

# 工单分析系统 Docker 一键部署脚本。
# 执行步骤：使用 Dockerfile 多阶段构建镜像 -> 停止旧容器 -> 挂载工单数据文件 -> 启动新容器 -> 健康检查。

APP_NAME="${APP_NAME:-ticket-analysis}"
IMAGE_NAME="${IMAGE_NAME:-$APP_NAME:latest}"
CONTAINER_NAME="${CONTAINER_NAME:-$APP_NAME}"
PORT="${PORT:-18081}"
CONTAINER_PORT="${CONTAINER_PORT:-18081}"
CONTAINER_DATA_PATH="${CONTAINER_DATA_PATH:-/app/config/task5_tickets.json}"
SOURCE_DIR="${SOURCE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DATA_SOURCE="${DATA_SOURCE:-}"
DEFAULT_CONFIG_DATA="$SOURCE_DIR/config/task5_tickets.json"

log() {
  printf '\033[1;34m[deploy]\033[0m %s\n' "$*"
}

die() {
  printf '\033[1;31m[deploy:error]\033[0m %s\n' "$*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

log "Docker 部署模式：服务器只需要安装 Docker，不需要安装 Go。"
need_cmd docker

DOCKER_CMD=(docker)
if ! docker info >/dev/null 2>&1; then
  if command -v sudo >/dev/null 2>&1 && sudo docker info >/dev/null 2>&1; then
    DOCKER_CMD=(sudo docker)
  else
    die "当前用户无法连接 Docker，请启动 Docker Desktop/Colima，或将用户加入 docker 组。"
  fi
fi

docker_cmd() {
  "${DOCKER_CMD[@]}" "$@"
}

abs_path() {
  local target="$1"
  local dir
  local base
  dir="$(cd "$(dirname "$target")" && pwd)"
  base="$(basename "$target")"
  printf '%s/%s\n' "$dir" "$base"
}

[[ -f "$SOURCE_DIR/go.mod" ]] || die "未找到 go.mod，请在项目根目录或设置 SOURCE_DIR 后执行。"
[[ -f "$SOURCE_DIR/Dockerfile" ]] || die "未找到 Dockerfile。"
[[ -d "$SOURCE_DIR/dashboard" ]] || die "未找到 dashboard 静态目录。"
[[ -d "$SOURCE_DIR/config" ]] || die "未找到 config 配置目录。"

if [[ -n "$DATA_SOURCE" ]]; then
  [[ -f "$DATA_SOURCE" ]] || die "DATA_SOURCE 指定的数据文件不存在：$DATA_SOURCE"
elif [[ -f "$DEFAULT_CONFIG_DATA" ]]; then
  DATA_SOURCE="$DEFAULT_CONFIG_DATA"
else
  die "未找到默认配置数据：$DEFAULT_CONFIG_DATA。请放入 config/task5_tickets.json，或用 DATA_SOURCE=/path/to/tickets.json $0 指定。"
fi

DATA_SOURCE="$(abs_path "$DATA_SOURCE")"

log "使用 Docker 多阶段构建镜像：$IMAGE_NAME"
docker_cmd build -t "$IMAGE_NAME" -f "$SOURCE_DIR/Dockerfile" "$SOURCE_DIR"

if docker_cmd container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  log "停止并删除旧容器：$CONTAINER_NAME"
  docker_cmd rm -f "$CONTAINER_NAME" >/dev/null
fi

log "启动容器：$CONTAINER_NAME"
docker_cmd run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
  -p "$PORT:$CONTAINER_PORT" \
  -e PORT="$CONTAINER_PORT" \
  -e DATA_PATH="$CONTAINER_DATA_PATH" \
  -v "$DATA_SOURCE:$CONTAINER_DATA_PATH:ro" \
  "$IMAGE_NAME" >/dev/null

log "等待服务健康检查"
if command -v curl >/dev/null 2>&1; then
  for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null; then
      log "部署完成：http://127.0.0.1:$PORT/dashboard/index.html"
      log "查看容器：${DOCKER_CMD[*]} ps --filter name=$CONTAINER_NAME"
      log "查看日志：${DOCKER_CMD[*]} logs -f $CONTAINER_NAME"
      exit 0
    fi
    sleep 1
  done
else
  log "未安装 curl，已跳过健康检查。请访问 http://127.0.0.1:$PORT/dashboard/index.html 验证。"
  exit 0
fi

docker_cmd logs --tail 100 "$CONTAINER_NAME" || true
die "容器启动后健康检查未通过，请查看日志：${DOCKER_CMD[*]} logs -f $CONTAINER_NAME"
