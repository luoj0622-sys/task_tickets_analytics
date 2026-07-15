# 工单分析系统

这是一个面向客服主管的前后端分离工单运营 Dashboard。Go 后端从工单 JSON 数据中计算趋势、异常、处理时长压力和单票风险，前端通过 API 获取报告并展示，帮助主管快速判断哪些问题正在变多、为什么值得关注、今天应该优先处理哪些工单。

## 运行方式

默认数据源为 `config/task5_tickets.json`，作为项目配置数据随代码一起管理。

```bash
go run ./cmd/server -port 18081
```

然后打开：

```text
http://127.0.0.1:18081/dashboard/index.html
```

后端 API：

- `GET /api/health`：健康检查
- `GET /api/report?timeout=24`：按指定超时阈值生成结构化分析报告

也可以指定数据源：

```bash
go run ./cmd/server -port 18081 -data /path/to/tickets.json
```

或通过环境变量指定：

```bash
DATA_PATH=/path/to/tickets.json go run ./cmd/server -port 18081
```

## Docker 一键部署

服务器只需要安装并启动 Docker，不需要安装 Go；Go 编译会在 Docker 多阶段构建的 `builder` 阶段内完成。

在已安装 Docker 的服务器上，将项目代码放到服务器后执行：

```bash
./scripts/deploy_server.sh
```

脚本会通过 `Dockerfile` 多阶段构建镜像，停止旧容器，挂载 `config/task5_tickets.json`，并启动新的 Docker 容器。默认镜像名为 `ticket-analysis:latest`，默认容器名为 `ticket-analysis`，默认端口为 `18081`。

`Dockerfile` 使用两阶段构建：

- `builder` 阶段基于 `golang:1.22-alpine`，只负责下载依赖并编译 Go 后端二进制。
- `runtime` 阶段基于 `alpine:3.20`，只保留 `/app/ticket-server`、`/app/dashboard` 和 `/app/config`，默认读取 `/app/config/task5_tickets.json`。

也可以手动构建镜像：

```bash
docker build -t ticket-analysis:latest .
```

常用配置：

```bash
PORT=8080 \
IMAGE_NAME=ticket-analysis:latest \
CONTAINER_NAME=ticket-analysis \
./scripts/deploy_server.sh
```

如需临时使用另一份工单 JSON，可以覆盖默认配置数据：

```bash
DATA_SOURCE=/path/to/tickets.json ./scripts/deploy_server.sh
```

部署后访问：

```text
http://服务器IP:18081/dashboard/index.html
```

服务管理：

```bash
docker ps --filter name=ticket-analysis
docker logs -f ticket-analysis
docker restart ticket-analysis
```

## 分析维度

| 维度 | 指标 | 对主管的价值 |
|---|---|---|
| 时间趋势 | 每日工单量、类型日均量、近期与基线增长倍数 | 发现最近正在变多的问题，判断是否需要排查系统、活动或流程变化 |
| 严重程度 | 高优先级率、未解决率、满意度、低满意度率 | 区分“数量多”和“风险高”，避免低量高风险问题被忽略 |
| 处理时长 | 平均处理时长、中位处理时长、超时数量、处理时长异常工单、大部分处理时间 | 找出积压、长尾工单和流程瓶颈 |
| 单票风险 | 优先级、未解决、超时、低满意度、长处理时长 | 输出主管每日优先处理队列 |

满意度默认规则为：`2` 分及以下的工单标记为低满意度。

页面提供“超时设置”，默认阈值为 `24` 小时。调整后会即时刷新：

- 总揽里的超时工单 KPI
- 异常信号中的积压风险
- 处理时长模块的类别超时数量
- 主管优先处理队列中的风险分和原因标签

默认阈值由前端输入框设置为 `24` 小时，并通过 `/api/report?timeout=24` 传给 Go 后端重新计算。

总揽中的异常信号会列出关联工单证据，包括工单号、类别、信号类型和描述，便于主管从概览直接定位需要查看的具体条目。

主管优先处理队列的风险分计算方式为：优先级权重（高 `30` / 中 `18` / 低 `8`）+ 未解决 `25` + 超时 `20` + 低满意度 `12` + 满意度触底 `8` + 处理时长分（每 `24h` 加 `4`，最高 `16`）。

## 样例数据关键发现

样例共 50 条工单，时间范围为 `2024-06-01` 至 `2024-06-11`。

- 支付问题是趋势异常：最近 3 天日均 `2.33` 单，前 8 天基线日均 `1.12` 单，增长 `2.07x`。
- 退款退货是积压和长尾风险：共 13 单，未解决率 `38%`，平均处理时长约 `45.23h`，按 24 小时阈值超时 6 单。
- 投诉属于低量高风险：只有 4 单，但低满意度率 `100%`，平均满意度 `1`。
- 处理时长异常规则发现 5 张长尾工单，集中在退款退货：`T031`、`T007`、`T047`、`T001`、`T042`。大部分工单处理时间集中在 `3.25h` 至 `24h`，超过 `55.13h` 会被标记为处理时长异常工单。
- 重复主题集中在支付扣款与订单状态异常、退款进度与到账、重复扣款或多扣款、物流异常、退货运费垫付报销、客服接入体验。

## 输出内容

`/api/report` 返回以下结构：

- `summary`：总体 KPI、异常信号、类别/优先级/渠道分布
- `trend`：每日趋势、类别每日数量、近期与基线增长倍数
- `severity`：类别风险指标和风险等级
- `sla_efficiency`：超时阈值、超时统计、大部分处理时间、处理时长异常工单
- `recurring_themes`：重复问题主题、关联工单和判断依据
- `anomalies`：结构化异常信号及证据
- `priority_queue`：按风险分排序的主管优先处理队列

## AI 工具使用情况

AI 用于辅助需求拆解、分析维度设计、Dashboard 信息架构、代码生成和 README 编写。运行时异常识别不依赖 AI 模型调用，所有趋势、处理时长异常、主题匹配和风险分计算都由 Go 后端中的确定性规则完成，便于复核和复现。
