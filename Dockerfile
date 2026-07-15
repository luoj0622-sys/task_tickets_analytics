# Docker 多阶段构建。
# 第一阶段：构建 Go 后端二进制。
# 这一层包含 Go 工具链，只用于编译，不会进入最终运行镜像。
FROM golang:1.22-alpine AS builder

WORKDIR /src

# 先复制 go.mod 并下载依赖，方便 Docker 复用依赖缓存。
COPY go.mod ./
RUN go mod download

# 再复制后端源码并编译静态 Linux 二进制。
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ticket-server ./cmd/server

# 第二阶段：运行镜像。
# 这里只保留编译后的二进制和前端静态文件，镜像更小，部署面更清晰。
FROM alpine:3.20 AS runtime

LABEL org.opencontainers.image.title="ticket-analysis"
LABEL org.opencontainers.image.description="工单趋势、异常和处理时长分析 Dashboard"

RUN addgroup -S ticketapp && adduser -S -G ticketapp ticketapp

WORKDIR /app

COPY --from=builder /out/ticket-server /app/ticket-server
COPY dashboard /app/dashboard

# 容器运行时通过只读挂载提供工单 JSON 数据。
RUN mkdir -p /app/data && chown -R ticketapp:ticketapp /app

USER ticketapp

ENV PORT=18081
ENV DATA_PATH=/app/data/task5_tickets.json

EXPOSE 18081

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD sh -c 'wget -q -O - "http://127.0.0.1:${PORT}/api/health" >/dev/null || exit 1'

CMD ["/bin/sh", "-c", "/app/ticket-server -port ${PORT} -data ${DATA_PATH} -static /app/dashboard"]
