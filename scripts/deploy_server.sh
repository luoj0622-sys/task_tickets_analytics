#!/usr/bin/env bash
set -Eeuo pipefail

# 工单分析系统一键部署脚本。
# 适用场景：在 Linux 服务器上从项目根目录执行，自动构建 Go 二进制、复制前端静态文件、
# 写入 systemd 服务并启动。数据文件通过 DATA_SOURCE 指定，避免使用开发机上的绝对路径。

APP_NAME="${APP_NAME:-ticket-analysis}"
SERVICE_NAME="${SERVICE_NAME:-$APP_NAME}"
APP_USER="${APP_USER:-ticketapp}"
PORT="${PORT:-18081}"
INSTALL_DIR="${INSTALL_DIR:-/opt/$APP_NAME}"
BINARY_NAME="${BINARY_NAME:-ticket-server}"
DATA_PATH="${DATA_PATH:-$INSTALL_DIR/data/task5_tickets.json}"
STATIC_DIR="${STATIC_DIR:-$INSTALL_DIR/dashboard}"
SOURCE_DIR="${SOURCE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DATA_SOURCE="${DATA_SOURCE:-}"
DEFAULT_LOCAL_DATA="/Users/a1-6/Downloads/task5_tickets.json"

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

run_root() {
  if [[ "${EUID}" -eq 0 ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

if [[ "$(uname -s)" != "Linux" ]]; then
  die "该脚本用于 Linux 服务器部署；当前系统为 $(uname -s)。"
fi

need_cmd go
need_cmd install
need_cmd systemctl
if [[ "${EUID}" -ne 0 ]]; then
  need_cmd sudo
fi

[[ -f "$SOURCE_DIR/go.mod" ]] || die "未找到 go.mod，请在项目根目录或设置 SOURCE_DIR 后执行。"
[[ -d "$SOURCE_DIR/dashboard" ]] || die "未找到 dashboard 静态目录。"

if [[ -n "$DATA_SOURCE" ]]; then
  [[ -f "$DATA_SOURCE" ]] || die "DATA_SOURCE 指定的数据文件不存在：$DATA_SOURCE"
elif [[ -f "$DATA_PATH" ]]; then
  log "复用已部署数据文件：$DATA_PATH"
elif [[ -f "$DEFAULT_LOCAL_DATA" ]]; then
  DATA_SOURCE="$DEFAULT_LOCAL_DATA"
else
  die "未找到数据文件。请用 DATA_SOURCE=/path/to/task5_tickets.json $0 指定。"
fi

BUILD_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$BUILD_DIR"
}
trap cleanup EXIT

log "构建 Go 服务"
(
  cd "$SOURCE_DIR"
  go build -trimpath -ldflags="-s -w" -o "$BUILD_DIR/$BINARY_NAME" ./cmd/server
)

log "准备部署目录：$INSTALL_DIR"
run_root install -d -m 0755 "$INSTALL_DIR" "$INSTALL_DIR/bin" "$INSTALL_DIR/data" "$STATIC_DIR"

if ! id -u "$APP_USER" >/dev/null 2>&1; then
  log "创建系统用户：$APP_USER"
  run_root useradd --system --user-group --home-dir "$INSTALL_DIR" --shell /usr/sbin/nologin "$APP_USER"
fi

log "安装二进制和静态资源"
run_root install -m 0755 "$BUILD_DIR/$BINARY_NAME" "$INSTALL_DIR/bin/$BINARY_NAME"
run_root cp -R "$SOURCE_DIR/dashboard/." "$STATIC_DIR/"

if [[ -n "$DATA_SOURCE" ]]; then
  log "复制数据文件：$DATA_SOURCE -> $DATA_PATH"
  run_root install -m 0644 "$DATA_SOURCE" "$DATA_PATH"
fi

run_root chown -R "$APP_USER:$APP_USER" "$INSTALL_DIR"

SERVICE_FILE="$BUILD_DIR/$SERVICE_NAME.service"
cat >"$SERVICE_FILE" <<SERVICE
[Unit]
Description=Ticket Analysis Dashboard
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$APP_USER
Group=$APP_USER
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/bin/$BINARY_NAME -port $PORT -data $DATA_PATH -static $STATIC_DIR
Restart=always
RestartSec=3
Environment=PORT=$PORT
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=$INSTALL_DIR

[Install]
WantedBy=multi-user.target
SERVICE

log "安装 systemd 服务：$SERVICE_NAME"
run_root install -m 0644 "$SERVICE_FILE" "/etc/systemd/system/$SERVICE_NAME.service"
run_root systemctl daemon-reload
run_root systemctl enable "$SERVICE_NAME"
run_root systemctl restart "$SERVICE_NAME"

log "等待服务启动"
if command -v curl >/dev/null 2>&1; then
  for _ in $(seq 1 20); do
    if curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null; then
      log "部署完成：http://127.0.0.1:$PORT/dashboard/index.html"
      log "查看状态：systemctl status $SERVICE_NAME"
      exit 0
    fi
    sleep 1
  done
fi

run_root systemctl status "$SERVICE_NAME" --no-pager || true
die "服务启动后健康检查未通过，请查看：journalctl -u $SERVICE_NAME -n 100 --no-pager"
