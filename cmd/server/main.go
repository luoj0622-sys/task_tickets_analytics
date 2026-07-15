package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDataPath     = "config/task5_tickets.json"
	defaultPort         = "18081"
	defaultTimeoutHours = 24.0
	lowSatThreshold     = 2
	recentDays          = 3

	severityHighPriorityWeight     = 25.0
	severityUnresolvedWeight       = 35.0
	severityLowSatisfactionWeight  = 25.0
	severitySatisfactionDropWeight = 15.0
	severityWatchThreshold         = 30.0
	severityWarningThreshold       = 45.0
	severityCriticalThreshold      = 60.0
)

var config = map[string]any{
	// 前端会读取这些配置展示口径，避免页面说明和后端算法各写一套。
	"recent_days":                recentDays,
	"trend_growth_threshold":     1.5,
	"trend_min_recent_count":     2,
	"trend_min_daily_delta":      0.5,
	"low_satisfaction_threshold": lowSatThreshold,
	"low_satisfaction_operator":  "<=",
	"timeout_threshold_hours":    defaultTimeoutHours,
	"severity_weights": map[string]float64{
		"high_priority_rate":     severityHighPriorityWeight,
		"unresolved_rate":        severityUnresolvedWeight,
		"low_satisfaction_rate":  severityLowSatisfactionWeight,
		"satisfaction_drop_rate": severitySatisfactionDropWeight,
	},
	"severity_thresholds": map[string]float64{
		"watch":    severityWatchThreshold,
		"warning":  severityWarningThreshold,
		"critical": severityCriticalThreshold,
	},
	"risk_weights": map[string]any{
		"priority":              map[string]float64{"高": 30, "中": 18, "低": 8},
		"unresolved":            25.0,
		"timeout":               20.0,
		"low_satisfaction":      12.0,
		"very_low_satisfaction": 8.0,
		"duration_per_24h":      4.0,
		"duration_cap":          16.0,
	},
}

var dimensionValues = []map[string]any{
	{
		"dimension":        "时间趋势",
		"metrics":          []string{"每日工单量", "类型日均量", "近期与基线增长倍数"},
		"supervisor_value": "帮助主管发现正在变多的问题，优先确认是否出现系统性故障、活动影响或流程变化。",
	},
	{
		"dimension":        "严重程度",
		"metrics":          []string{"高优先级率", "未解决率", "满意度", "低满意度率"},
		"supervisor_value": "把数量多和风险高分开，避免低量但高投诉、高未解决的问题被淹没。",
	},
	{
		"dimension":        "处理时长",
		"metrics":          []string{"平均处理时长", "中位处理时长", "超时数量", "处理时长异常工单"},
		"supervisor_value": "定位积压、长尾工单和流程瓶颈，判断是否需要升级、补人或跨团队协作。",
	},
	{
		"dimension":        "单票风险",
		"metrics":          []string{"优先级", "未解决", "超时", "低满意度", "长处理时长"},
		"supervisor_value": "生成每日优先处理队列，让主管把精力放在最可能升级或影响体验的具体工单上。",
	},
}

type rawTicket struct {
	TicketID           string  `json:"ticket_id"`
	CreatedAt          string  `json:"created_at"`
	Category           string  `json:"category"`
	Description        string  `json:"description"`
	Priority           string  `json:"priority"`
	ResolutionTimeHour float64 `json:"resolution_time_hours"`
	Satisfaction       int     `json:"satisfaction"`
	Channel            string  `json:"channel"`
	IsResolved         bool    `json:"is_resolved"`
}

type ticket struct {
	rawTicket
	Created time.Time
}

type themeRule struct {
	Theme        string
	Keywords     []string
	Categories   map[string]bool
	WhyItMatters string
}

var themeRules = []themeRule{
	{
		Theme:        "支付扣款与订单状态异常",
		Keywords:     []string{"付款成功", "支付成功", "扣款", "未支付", "待支付", "订单没", "订单显示", "订单取消", "金额不对", "多扣"},
		WhyItMatters: "支付状态和订单状态不一致会直接阻断履约，容易形成重复咨询和资金争议。",
	},
	{
		Theme:        "重复扣款或多扣款",
		Keywords:     []string{"重复扣款", "扣了两次", "两个都扣钱", "多扣", "扣款金额不对", "又出现重复扣款"},
		WhyItMatters: "重复扣款属于资金风险，用户感知强烈，需要优先核对支付流水和退款路径。",
	},
	{
		Theme:        "退款进度与到账",
		Keywords:     []string{"退款", "钱还没退", "什么时候退", "退款还在处理中", "不给退", "七天无理由"},
		WhyItMatters: "退款进度慢会拉低满意度并造成未解决积压，需要售后和财务协同。",
	},
	{
		Theme:        "退货运费与垫付报销",
		Keywords:     []string{"退货运费", "运费", "垫付", "报销"},
		WhyItMatters: "运费垫付和报销规则不清会导致反复沟通，容易从普通售后升级为投诉。",
	},
	{
		Theme:        "物流异常或未收到",
		Keywords:     []string{"物流", "签收", "没收到", "包裹", "快递显示异常", "快递信息", "旧地址"},
		Categories:   map[string]bool{"物流查询": true},
		WhyItMatters: "物流异常通常需要承运商介入，处理链路长，超时和未解决风险较高。",
	},
	{
		Theme:        "客服机器人与接入体验",
		Keywords:     []string{"机器人", "同样的话", "40分钟", "客服接", "客服态度"},
		WhyItMatters: "接入体验问题会放大用户不满，即使数量不高也会影响服务口碑。",
	},
	{
		Theme:        "账号安全与登录",
		Keywords:     []string{"验证码", "冻结", "账号", "绑定手机号", "不是我操作"},
		WhyItMatters: "账号问题可能涉及安全和身份核验，需要避免普通答复掩盖风险。",
	},
}

func main() {
	dataPath := flag.String("data", envOrDefault("DATA_PATH", defaultDataPath), "path to ticket JSON data")
	staticDir := flag.String("static", "dashboard", "path to dashboard static files")
	port := flag.String("port", envOrDefault("PORT", defaultPort), "HTTP port")
	flag.Parse()

	absStaticDir, err := filepath.Abs(*staticDir)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		timeoutHours, err := timeoutFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		tickets, err := loadTickets(*dataPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		report, err := buildReport(tickets, *dataPath, timeoutHours)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, report)
	})
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard/", http.FileServer(http.Dir(absStaticDir))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/index.html", http.StatusFound)
	})

	addr := ":" + *port
	log.Printf("ticket analysis server listening on http://127.0.0.1%s/dashboard/index.html", addr)
	log.Printf("data source: %s", *dataPath)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func timeoutFromRequest(r *http.Request) (float64, error) {
	// 超时阈值由页面输入框传入；没有传时使用默认 24 小时。
	raw := r.URL.Query().Get("timeout")
	if raw == "" {
		return defaultTimeoutHours, nil
	}
	// 阈值按小时取整，避免 24.3h 这类输入导致同一页面反复出现细碎口径。
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 1 {
		return 0, errors.New("timeout must be a positive number of hours")
	}
	return math.Round(value), nil
}

func loadTickets(path string) ([]ticket, error) {
	// 先读取原始 JSON，再把 created_at 解析成 time.Time，后续趋势分析都依赖这个时间字段。
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ticket data: %w", err)
	}
	var raw []rawTicket
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse ticket data: %w", err)
	}
	tickets := make([]ticket, 0, len(raw))
	for index, item := range raw {
		created, err := time.ParseInLocation("2006-01-02 15:04", item.CreatedAt, time.Local)
		if err != nil {
			return nil, fmt.Errorf("parse created_at on row %d: %w", index+1, err)
		}
		tickets = append(tickets, ticket{rawTicket: item, Created: created})
	}
	return tickets, nil
}

func buildReport(tickets []ticket, sourcePath string, timeoutHours float64) (map[string]any, error) {
	if len(tickets) == 0 {
		return nil, errors.New("ticket dataset is empty")
	}

	// 报告生成主流程：先按维度计算，再汇总异常和优先队列，最后组装成 API 响应。
	days := calendarDays(tickets)
	trend := buildTrend(tickets, days)
	severity := buildSeverity(tickets)
	duration := buildDuration(tickets, timeoutHours)
	themes := buildThemes(tickets)
	anomalies := buildAnomalies(tickets, trend, severity, duration, themes)
	priorityQueue := buildPriorityQueue(tickets, timeoutHours)
	summary := buildSummary(tickets, days, trend, severity, duration, timeoutHours)
	brief := []string{}
	// 总览摘要只取排序后的前三条异常，给首页快速扫读使用。
	for index, item := range anomalies {
		if index >= 3 {
			break
		}
		brief = append(brief, item["title"].(string))
	}
	summary["executive_brief"] = brief

	reportConfig := cloneConfig(timeoutHours)
	return map[string]any{
		"metadata": map[string]any{
			"generated_at": time.Now().Format(time.RFC3339),
			"source_path":  sourcePath,
			"ticket_count": len(tickets),
			"config":       reportConfig,
			"runtime":      "go",
		},
		"dimension_values": dimensionValues,
		"summary":          summary,
		"trend":            trend,
		"severity":         severity,
		"sla_efficiency":   duration,
		"recurring_themes": themes,
		"anomalies":        anomalies,
		"priority_queue":   priorityQueue,
	}, nil
}

func cloneConfig(timeoutHours float64) map[string]any {
	result := map[string]any{}
	for key, value := range config {
		result[key] = value
	}
	result["timeout_threshold_hours"] = timeoutHours
	return result
}

func buildTrend(tickets []ticket, days []time.Time) map[string]any {
	categories := sortedCategories(tickets)
	dailyTotals := map[string]int{}
	categoryDaily := map[string]map[string]int{}

	// 先把完整日期 x 类别矩阵初始化为 0，保证没有工单的日期也能在图表中显示。
	for _, category := range categories {
		categoryDaily[category] = map[string]int{}
	}
	for _, day := range days {
		dayKey := dateKey(day)
		dailyTotals[dayKey] = 0
		for _, category := range categories {
			categoryDaily[category][dayKey] = 0
		}
	}
	for _, item := range tickets {
		dayKey := dateKey(item.Created)
		dailyTotals[dayKey]++
		categoryDaily[item.Category][dayKey]++
	}

	// 最近窗口默认取最后 3 天，其余日期作为基线窗口，用来判断“近期是否变多”。
	recentSize := recentDays
	if recentSize > len(days) {
		recentSize = len(days)
	}
	baselineDays := days[:len(days)-recentSize]
	recentWindowDays := days[len(days)-recentSize:]

	categoryGrowth := []map[string]any{}
	for _, category := range categories {
		baselineCount := sumCategory(categoryDaily[category], baselineDays)
		recentCount := sumCategory(categoryDaily[category], recentWindowDays)
		baselineAvg := rate(float64(baselineCount), float64(len(baselineDays)))
		recentAvg := rate(float64(recentCount), float64(len(recentWindowDays)))
		var growthMultiple any
		growthValue := 0.0
		// 基线为 0 时无法计算增长倍数，用 nil 表示“新增”，前端会显示为新增。
		if baselineAvg == 0 && recentAvg > 0 {
			growthMultiple = nil
		} else {
			growthValue = rate(recentAvg, baselineAvg)
			growthMultiple = round(growthValue)
		}
		// 趋势异常同时要求：近期数量足够、增长倍数达标、日均增量明显，降低小样本误报。
		isAnomaly := recentCount >= 2 && growthMultiple != nil && growthValue >= 1.5 && recentAvg-baselineAvg >= 0.5
		categoryGrowth = append(categoryGrowth, map[string]any{
			"category":           category,
			"total":              totalCategory(categoryDaily[category]),
			"baseline_count":     baselineCount,
			"recent_count":       recentCount,
			"baseline_daily_avg": round(baselineAvg),
			"recent_daily_avg":   round(recentAvg),
			"growth_multiple":    growthMultiple,
			"daily_delta":        round(recentAvg - baselineAvg),
			"is_trend_anomaly":   isAnomaly,
		})
	}
	sort.Slice(categoryGrowth, func(i, j int) bool {
		// 趋势表先展示异常类别，再按增长倍数排序。
		leftAnomaly := categoryGrowth[i]["is_trend_anomaly"].(bool)
		rightAnomaly := categoryGrowth[j]["is_trend_anomaly"].(bool)
		if leftAnomaly != rightAnomaly {
			return leftAnomaly
		}
		return floatFromAny(categoryGrowth[i]["growth_multiple"]) > floatFromAny(categoryGrowth[j]["growth_multiple"])
	})

	daily := []map[string]any{}
	for _, day := range days {
		dayKey := dateKey(day)
		daily = append(daily, map[string]any{"date": dayKey, "count": dailyTotals[dayKey]})
	}

	categoryDailyOutput := []map[string]any{}
	for _, category := range categories {
		rows := []map[string]any{}
		for _, day := range days {
			dayKey := dateKey(day)
			rows = append(rows, map[string]any{"date": dayKey, "count": categoryDaily[category][dayKey]})
		}
		categoryDailyOutput = append(categoryDailyOutput, map[string]any{"category": category, "daily": rows})
	}

	return map[string]any{
		"window": map[string]any{
			"recent_days":        dateKeys(recentWindowDays),
			"baseline_days":      dateKeys(baselineDays),
			"recent_day_count":   len(recentWindowDays),
			"baseline_day_count": len(baselineDays),
		},
		"daily_totals":    daily,
		"category_daily":  categoryDailyOutput,
		"category_growth": categoryGrowth,
	}
}

func buildSeverity(tickets []ticket) map[string]any {
	// 严重程度按“类别”聚合，用来区分“工单数量多”和“业务风险高”。
	groups := ticketsByCategory(tickets)
	categories := []map[string]any{}
	for _, category := range sortedMapKeys(groups) {
		rows := groups[category]
		count := len(rows)
		highCount := 0
		unresolvedCount := 0
		lowSatCount := 0
		satisfactionValues := []float64{}
		for _, item := range rows {
			if item.Priority == "高" {
				highCount++
			}
			if !item.IsResolved {
				unresolvedCount++
			}
			if isLowSatisfaction(item) {
				lowSatCount++
			}
			satisfactionValues = append(satisfactionValues, float64(item.Satisfaction))
		}
		avgSatisfaction := average(satisfactionValues)
		highRate := rate(float64(highCount), float64(count))
		unresolvedRate := rate(float64(unresolvedCount), float64(count))
		lowSatRate := rate(float64(lowSatCount), float64(count))

		// 类别风险分按比例计算，不直接按数量计算；这样低量但高风险的类别也能浮出来。
		// 公式：
		// - 高优先级率最多贡献 severityHighPriorityWeight 分
		// - 未解决率最多贡献 severityUnresolvedWeight 分，未关闭工单对主管最需要动作
		// - 低满意度率最多贡献 severityLowSatisfactionWeight 分，低满意度规则由 isLowSatisfaction 统一控制
		// - 平均满意度下滑最多贡献 severitySatisfactionDropWeight 分；5 分为 0，1 分为满分
		riskScore := highRate*severityHighPriorityWeight +
			unresolvedRate*severityUnresolvedWeight +
			lowSatRate*severityLowSatisfactionWeight +
			((5-avgSatisfaction)/4)*severitySatisfactionDropWeight

		// 风险标签用于页面徽标：稳定 <30，观察 >=30，关注 >=45，严重 >=60。
		riskLevel := "stable"
		if riskScore >= severityCriticalThreshold {
			riskLevel = "critical"
		} else if riskScore >= severityWarningThreshold {
			riskLevel = "warning"
		} else if riskScore >= severityWatchThreshold {
			riskLevel = "watch"
		}
		categories = append(categories, map[string]any{
			"category":               category,
			"count":                  count,
			"high_priority_count":    highCount,
			"high_priority_rate":     round(highRate),
			"unresolved_count":       unresolvedCount,
			"unresolved_rate":        round(unresolvedRate),
			"average_satisfaction":   round(avgSatisfaction),
			"low_satisfaction_count": lowSatCount,
			"low_satisfaction_rate":  round(lowSatRate),
			"risk_score":             round(riskScore),
			"risk_level":             riskLevel,
			// attention_flag 决定该类别是否进入“异常信号”候选：
			// 未解决压力、低满意度压力或综合风险分任一明显偏高，就值得主管关注。
			"attention_flag": unresolvedRate >= 0.2 || lowSatRate >= 0.5 || riskScore >= severityWarningThreshold,
		})
	}
	sort.Slice(categories, func(i, j int) bool {
		// 严重程度表按风险分降序排列；同分时数量多的排前面。
		if floatFromAny(categories[i]["risk_score"]) != floatFromAny(categories[j]["risk_score"]) {
			return floatFromAny(categories[i]["risk_score"]) > floatFromAny(categories[j]["risk_score"])
		}
		return categories[i]["count"].(int) > categories[j]["count"].(int)
	})
	return map[string]any{"categories": categories}
}

func buildDuration(tickets []ticket, timeoutHours float64) map[string]any {
	// 处理时长维度同时看“是否超过 SLA 阈值”和“是否属于统计上的长尾异常”。
	groups := ticketsByCategory(tickets)
	allDurations := []float64{}
	for _, item := range tickets {
		allDurations = append(allDurations, item.ResolutionTimeHour)
	}
	// IQR 用于描述大部分工单处理时间，并给长尾异常提供可复核阈值。
	iqr := iqrStats(allDurations)
	upperBound := iqr["upper_bound"].(float64)
	outliers := []map[string]any{}
	timeoutTickets := []ticket{}
	for _, item := range tickets {
		// 超过 IQR 上界的票定义为“处理时长异常工单”，区别于普通 SLA 超时。
		if item.ResolutionTimeHour > upperBound {
			outliers = append(outliers, ticketSnapshot(item, timeoutHours))
		}
		if timedOut(item, timeoutHours) {
			timeoutTickets = append(timeoutTickets, item)
		}
	}
	sort.Slice(outliers, func(i, j int) bool {
		return outliers[i]["resolution_time_hours"].(float64) > outliers[j]["resolution_time_hours"].(float64)
	})

	categories := []map[string]any{}
	for _, category := range sortedMapKeys(groups) {
		rows := groups[category]
		durations := []float64{}
		timeoutIDs := []string{}
		// 类别维度统计平均/中位时长与超时数量，用来定位流程瓶颈集中在哪类问题。
		for _, item := range rows {
			durations = append(durations, item.ResolutionTimeHour)
			if timedOut(item, timeoutHours) {
				timeoutIDs = append(timeoutIDs, item.TicketID)
			}
		}
		categories = append(categories, map[string]any{
			"category":                 category,
			"count":                    len(rows),
			"average_resolution_hours": round(average(durations)),
			"median_resolution_hours":  round(median(durations)),
			"timeout_count":            len(timeoutIDs),
			"timeout_rate":             round(rate(float64(len(timeoutIDs)), float64(len(rows)))),
			"timeout_ticket_ids":       timeoutIDs,
			"iqr":                      iqrStats(durations),
		})
	}
	sort.Slice(categories, func(i, j int) bool {
		// 处理时长表优先展示超时数量最多的类别；数量相同时平均处理更久的排前面。
		left := categories[i]
		right := categories[j]
		if left["timeout_count"].(int) != right["timeout_count"].(int) {
			return left["timeout_count"].(int) > right["timeout_count"].(int)
		}
		return floatFromAny(left["average_resolution_hours"]) > floatFromAny(right["average_resolution_hours"])
	})

	return map[string]any{
		"thresholds": map[string]any{"timeout_threshold_hours": timeoutHours},
		"overall": map[string]any{
			"average_resolution_hours": round(average(allDurations)),
			"median_resolution_hours":  round(median(allDurations)),
			"timeout_count":            len(timeoutTickets),
			"timeout_rate":             round(rate(float64(len(timeoutTickets)), float64(len(tickets)))),
			"majority_resolution_hours": map[string]any{
				// Q1-Q3 表示中间 50% 工单的处理时长，页面展示为“大部分处理时间”。
				"from":   iqr["q1"],
				"to":     iqr["q3"],
				"method": "Q1-Q3",
			},
			"outlier_threshold_hours": upperBound,
			"iqr":                     iqr,
		},
		"categories": categories,
		"outliers":   outliers,
	}
}

func buildThemes(tickets []ticket) []map[string]any {
	// 重复问题使用透明关键词规则，不调用模型，便于复核每个主题为什么被命中。
	themes := []map[string]any{}
	for _, rule := range themeRules {
		matches := []ticket{}
		for _, item := range tickets {
			// 部分主题限定类别，避免关键词在无关类别里误触发。
			if len(rule.Categories) > 0 && !rule.Categories[item.Category] {
				continue
			}
			for _, keyword := range rule.Keywords {
				if strings.Contains(item.Description, keyword) {
					matches = append(matches, item)
					break
				}
			}
		}
		if len(matches) < 2 {
			// 至少出现 2 次才认为是可观察的重复主题。
			continue
		}
		categoryCounts := map[string]int{}
		ticketIDs := []string{}
		representatives := []map[string]any{}
		for index, item := range matches {
			categoryCounts[item.Category]++
			ticketIDs = append(ticketIDs, item.TicketID)
			if index < 3 {
				representatives = append(representatives, map[string]any{"ticket_id": item.TicketID, "description": item.Description})
			}
		}
		themes = append(themes, map[string]any{
			"theme":                       rule.Theme,
			"keywords":                    rule.Keywords,
			"count":                       len(matches),
			"ticket_ids":                  ticketIDs,
			"categories":                  counterItems(categoryCounts),
			"representative_descriptions": representatives,
			"why_it_matters":              rule.WhyItMatters,
		})
	}
	sort.Slice(themes, func(i, j int) bool {
		return themes[i]["count"].(int) > themes[j]["count"].(int)
	})
	return themes
}

func buildPriorityQueue(tickets []ticket, timeoutHours float64) []map[string]any {
	// 单票风险队列把每张工单都打分，并给出可展示的原因标签和建议动作。
	queue := []map[string]any{}
	for _, item := range tickets {
		score, reasons := riskScore(item, timeoutHours)
		row := ticketSnapshot(item, timeoutHours)
		row["risk_score"] = score
		row["reason_labels"] = reasons
		row["suggested_action"] = suggestedAction(item.Category)
		queue = append(queue, row)
	}
	sort.Slice(queue, func(i, j int) bool {
		// 先按风险分降序；同分时处理时长更久的票优先，方便主管先处理长尾积压。
		if floatFromAny(queue[i]["risk_score"]) != floatFromAny(queue[j]["risk_score"]) {
			return floatFromAny(queue[i]["risk_score"]) > floatFromAny(queue[j]["risk_score"])
		}
		return floatFromAny(queue[i]["resolution_time_hours"]) > floatFromAny(queue[j]["resolution_time_hours"])
	})
	for index := range queue {
		queue[index]["rank"] = index + 1
	}
	return queue
}

func buildAnomalies(tickets []ticket, trend, severity, duration map[string]any, themes []map[string]any) []map[string]any {
	// 异常报告把各维度的异常统一成同一结构，前端可以用同一种卡片渲染。
	anomalies := []map[string]any{}
	for _, raw := range trend["category_growth"].([]map[string]any) {
		if !raw["is_trend_anomaly"].(bool) {
			continue
		}
		// 趋势异常：某类别近期日均相对基线明显升高。
		category := raw["category"].(string)
		anomalies = append(anomalies, map[string]any{
			"id":       "trend-" + category,
			"type":     "趋势增长",
			"level":    "warning",
			"title":    fmt.Sprintf("%s近期增长 %.2fx", category, floatFromAny(raw["growth_multiple"])),
			"affected": category,
			"evidence": map[string]any{
				"baseline_daily_avg": raw["baseline_daily_avg"],
				"recent_daily_avg":   raw["recent_daily_avg"],
				"growth_multiple":    raw["growth_multiple"],
				"baseline_count":     raw["baseline_count"],
				"recent_count":       raw["recent_count"],
			},
			"representative_tickets": anomalyTicketIDs(tickets, category, defaultTimeoutHours),
			"why_it_matters":         "近期日均明显高于基线，可能代表系统性问题或流程变化，主管应先确认是否需要跨团队排查。",
		})
	}

	for _, raw := range severity["categories"].([]map[string]any) {
		if !raw["attention_flag"].(bool) {
			continue
		}
		// 严重程度异常：数量未必最多，但高优先级、未解决或低满意度风险集中。
		category := raw["category"].(string)
		level := "warning"
		if raw["risk_level"] == "critical" {
			level = "critical"
		}
		anomalies = append(anomalies, map[string]any{
			"id":       "severity-" + category,
			"type":     "严重程度",
			"level":    level,
			"title":    fmt.Sprintf("%s风险分 %.2g", category, floatFromAny(raw["risk_score"])),
			"affected": category,
			"evidence": map[string]any{
				"count":                  raw["count"],
				"high_priority_count":    raw["high_priority_count"],
				"high_priority_rate":     raw["high_priority_rate"],
				"low_satisfaction_count": raw["low_satisfaction_count"],
				"unresolved_rate":        raw["unresolved_rate"],
				"average_satisfaction":   raw["average_satisfaction"],
				"low_satisfaction_rate":  raw["low_satisfaction_rate"],
			},
			"representative_tickets": anomalyTicketIDs(tickets, category, defaultTimeoutHours),
			"why_it_matters":         severityRationale(category, raw),
		})
	}

	for _, raw := range duration["categories"].([]map[string]any) {
		timeoutCount := raw["timeout_count"].(int)
		timeoutRate := floatFromAny(raw["timeout_rate"])
		// 处理时长异常：至少有一定数量或比例超时，才作为流程瓶颈提示。
		if timeoutCount < 2 && timeoutRate < 0.25 {
			continue
		}
		category := raw["category"].(string)
		anomalies = append(anomalies, map[string]any{
			"id":       "duration-" + category,
			"type":     "处理时长",
			"level":    "warning",
			"title":    fmt.Sprintf("%s超时 %d 单", category, timeoutCount),
			"affected": category,
			"evidence": map[string]any{
				"timeout_count":            timeoutCount,
				"timeout_rate":             raw["timeout_rate"],
				"average_resolution_hours": raw["average_resolution_hours"],
				"median_resolution_hours":  raw["median_resolution_hours"],
				"timeout_ticket_ids":       raw["timeout_ticket_ids"],
			},
			"representative_tickets": firstStrings(raw["timeout_ticket_ids"].([]string), 5),
			"why_it_matters":         "超时集中说明链路可能有积压或外部依赖，需要主管协调资源或调整处理优先级。",
		})
	}

	outliers := duration["outliers"].([]map[string]any)
	if len(outliers) > 0 {
		// 长尾异常：超过全量处理时长 IQR 上界的工单，通常需要主管追卡点。
		ids := []string{}
		for _, item := range outliers {
			ids = append(ids, item["ticket_id"].(string))
		}
		anomalies = append(anomalies, map[string]any{
			"id":       "outlier-long-duration",
			"type":     "处理时长异常工单",
			"level":    "critical",
			"title":    fmt.Sprintf("发现 %d 张处理时长异常工单", len(outliers)),
			"affected": "处理时长",
			"evidence": map[string]any{
				"majority_resolution_hours": duration["overall"].(map[string]any)["majority_resolution_hours"],
				"outlier_threshold_hours":   duration["overall"].(map[string]any)["outlier_threshold_hours"],
				"outlier_ticket_ids":        ids,
			},
			"representative_tickets": firstStrings(ids, 5),
			"why_it_matters":         "长尾工单会拖累体验和积压指标，尤其需要确认卡在退款、财务或物流哪个环节。",
		})
	}

	for _, theme := range themes {
		count := theme["count"].(int)
		// 重复主题通常至少 3 次才提示；重复扣款金额敏感，2 次以上也保留关注。
		if count < 3 && theme["theme"] != "重复扣款或多扣款" {
			continue
		}
		themeName := theme["theme"].(string)
		anomalies = append(anomalies, map[string]any{
			"id":                     "theme-" + themeName,
			"type":                   "重复问题",
			"level":                  "warning",
			"title":                  fmt.Sprintf("%s重复出现 %d 次", themeName, count),
			"affected":               themeName,
			"evidence":               map[string]any{"count": count, "ticket_ids": theme["ticket_ids"], "categories": theme["categories"]},
			"representative_tickets": firstStrings(theme["ticket_ids"].([]string), 5),
			"why_it_matters":         theme["why_it_matters"],
		})
	}

	levelWeight := map[string]int{"critical": 2, "warning": 1, "watch": 0}
	sort.Slice(anomalies, func(i, j int) bool {
		// 异常列表先按严重级别排序，再按类型稳定排序，保证首页摘要顺序可预期。
		if levelWeight[anomalies[i]["level"].(string)] != levelWeight[anomalies[j]["level"].(string)] {
			return levelWeight[anomalies[i]["level"].(string)] > levelWeight[anomalies[j]["level"].(string)]
		}
		return anomalies[i]["type"].(string) > anomalies[j]["type"].(string)
	})
	return anomalies
}

func buildSummary(tickets []ticket, days []time.Time, trend, severity, duration map[string]any, timeoutHours float64) map[string]any {
	// 首页总览聚合最常用的 KPI，目标是让主管先判断整体状态，再下钻模块。
	durations := []float64{}
	unresolvedCount := 0
	highCount := 0
	lowSatCount := 0
	satisfactionValues := []float64{}
	for _, item := range tickets {
		durations = append(durations, item.ResolutionTimeHour)
		satisfactionValues = append(satisfactionValues, float64(item.Satisfaction))
		if !item.IsResolved {
			unresolvedCount++
		}
		if item.Priority == "高" {
			highCount++
		}
		if isLowSatisfaction(item) {
			lowSatCount++
		}
	}
	timeoutCount := duration["overall"].(map[string]any)["timeout_count"].(int)
	return map[string]any{
		"period": map[string]any{
			"start":     dateKey(days[0]),
			"end":       dateKey(days[len(days)-1]),
			"day_count": len(days),
		},
		"kpis": map[string]any{
			"total_tickets":            len(tickets),
			"unresolved_count":         unresolvedCount,
			"unresolved_rate":          round(rate(float64(unresolvedCount), float64(len(tickets)))),
			"high_priority_count":      highCount,
			"high_priority_rate":       round(rate(float64(highCount), float64(len(tickets)))),
			"timeout_count":            timeoutCount,
			"timeout_rate":             round(rate(float64(timeoutCount), float64(len(tickets)))),
			"average_satisfaction":     round(average(satisfactionValues)),
			"low_satisfaction_count":   lowSatCount,
			"low_satisfaction_rate":    round(rate(float64(lowSatCount), float64(len(tickets)))),
			"average_resolution_hours": round(average(durations)),
			"median_resolution_hours":  round(median(durations)),
		},
		"category_counts": counterItems(countBy(tickets, func(item ticket) string { return item.Category })),
		"priority_counts": counterItems(countBy(tickets, func(item ticket) string { return item.Priority })),
		"channel_counts":  counterItems(countBy(tickets, func(item ticket) string { return item.Channel })),
		"health_signals":  buildHealthSignals(tickets, trend, severity, duration, timeoutHours),
	}
}

func buildHealthSignals(tickets []ticket, trend, severity, duration map[string]any, timeoutHours float64) []map[string]any {
	// 异常信号固定输出趋势、服务、积压三类，让首页结构稳定。
	signals := []map[string]any{}
	for _, item := range trend["category_growth"].([]map[string]any) {
		if item["is_trend_anomaly"].(bool) {
			// 只取排序后的第一个趋势异常作为首页信号，详细列表在趋势模块和异常列表中展示。
			category := item["category"].(string)
			signals = append(signals, map[string]any{
				"name":     "趋势风险",
				"level":    "warning",
				"headline": fmt.Sprintf("%s近期增长 %.2fx", category, floatFromAny(item["growth_multiple"])),
				"detail":   fmt.Sprintf("近期日均 %.2f 单，基线日均 %.2f 单。", floatFromAny(item["recent_daily_avg"]), floatFromAny(item["baseline_daily_avg"])),
				"related_tickets": signalTickets(tickets, "趋势增长", timeoutHours, func(item ticket) bool {
					return item.Category == category
				}, 3),
			})
			break
		}
	}
	if len(signals) == 0 {
		signals = append(signals, map[string]any{
			"name":            "趋势风险",
			"level":           "stable",
			"headline":        "未发现显著增长类别",
			"detail":          "所有类别近期日均相对基线未达到异常阈值。",
			"related_tickets": []map[string]any{},
		})
	}
	severityCategories := severity["categories"].([]map[string]any)
	if len(severityCategories) > 0 {
		// 服务风险取严重程度最高的类别，同时附上代表工单，方便从总览直接定位。
		top := severityCategories[0]
		category := top["category"].(string)
		signals = append(signals, map[string]any{
			"name":     "服务风险",
			"level":    top["risk_level"],
			"headline": severityHeadline(top),
			"detail":   topSeverityDetail(top),
			"related_tickets": signalTickets(tickets, "低满意风险", timeoutHours, func(item ticket) bool {
				return item.Category == category && (isLowSatisfaction(item) || item.Priority == "高" || !item.IsResolved)
			}, 3),
		})
	}
	durationCategories := duration["categories"].([]map[string]any)
	if len(durationCategories) > 0 {
		// 积压风险取超时最集中的类别，反映流程或外部依赖卡点。
		top := durationCategories[0]
		category := top["category"].(string)
		level := "stable"
		if top["timeout_count"].(int) > 0 {
			level = "warning"
		}
		signals = append(signals, map[string]any{
			"name":     "积压风险",
			"level":    level,
			"headline": fmt.Sprintf("%s超时最多", category),
			"detail":   fmt.Sprintf("阈值 %.0fh，超时 %d 单，平均处理 %.2fh。", timeoutHours, top["timeout_count"].(int), floatFromAny(top["average_resolution_hours"])),
			"related_tickets": signalTickets(tickets, "处理超时", timeoutHours, func(item ticket) bool {
				return item.Category == category && timedOut(item, timeoutHours)
			}, 3),
		})
	}
	return signals
}

func severityHeadline(category map[string]any) string {
	if floatFromAny(category["low_satisfaction_rate"]) >= 0.8 {
		return "服务体验低满意风险突出"
	}
	return fmt.Sprintf("%s服务风险最高", category["category"])
}

func topSeverityDetail(category map[string]any) string {
	return fmt.Sprintf(
		"该类工单共 %d 单，低满意度率 %.0f%%、高优先级率 %.0f%%、未解决率 %.0f%%，平均满意度 %.2g 分；数量不一定最高，但风险信号更集中。",
		category["count"].(int),
		floatFromAny(category["low_satisfaction_rate"])*100,
		floatFromAny(category["high_priority_rate"])*100,
		floatFromAny(category["unresolved_rate"])*100,
		floatFromAny(category["average_satisfaction"]),
	)
}

func severityRationale(category string, item map[string]any) string {
	if category == "投诉" {
		return fmt.Sprintf(
			"该类工单只有 %d 单，但其中 %d 单低满意度、%d 单高优先级，低满意度率 %.0f%%、平均满意度 %.2g 分；这是低量高风险信号，容易影响服务口碑，需要主管介入回访并确认是否存在客服接入、态度或处理流程问题。",
			item["count"].(int),
			item["low_satisfaction_count"].(int),
			item["high_priority_count"].(int),
			floatFromAny(item["low_satisfaction_rate"])*100,
			floatFromAny(item["average_satisfaction"]),
		)
	}
	return "该类别并不只体现数量，还伴随高优先级、未解决或低满意度信号，需要主管判断是否升级。"
}

func signalTickets(tickets []ticket, signalType string, timeoutHours float64, include func(ticket) bool, limit int) []map[string]any {
	// 首页异常信号下面列代表工单：先筛选相关票，再按单票风险排序取前几条。
	related := []ticket{}
	for _, item := range tickets {
		if include(item) {
			related = append(related, item)
		}
	}
	sort.Slice(related, func(i, j int) bool {
		left, _ := riskScore(related[i], timeoutHours)
		right, _ := riskScore(related[j], timeoutHours)
		if left != right {
			return left > right
		}
		return related[i].Created.After(related[j].Created)
	})

	output := []map[string]any{}
	for index, item := range related {
		if index >= limit {
			break
		}
		output = append(output, map[string]any{
			"ticket_id":    item.TicketID,
			"category":     item.Category,
			"type":         signalType,
			"description":  item.Description,
			"priority":     item.Priority,
			"satisfaction": item.Satisfaction,
			"is_resolved":  item.IsResolved,
		})
	}
	return output
}

func ticketSnapshot(item ticket, timeoutHours float64) map[string]any {
	// 统一输出工单快照，避免各模块重复拼字段导致口径不一致。
	return map[string]any{
		"ticket_id":             item.TicketID,
		"created_at":            item.CreatedAt,
		"date":                  dateKey(item.Created),
		"category":              item.Category,
		"description":           item.Description,
		"priority":              item.Priority,
		"resolution_time_hours": item.ResolutionTimeHour,
		"satisfaction":          item.Satisfaction,
		"channel":               item.Channel,
		"is_resolved":           item.IsResolved,
		"is_timeout":            timedOut(item, timeoutHours),
	}
}

func isLowSatisfaction(item ticket) bool {
	// 低满意度口径集中放在这里；当前规则是 2 分及以下。
	return item.Satisfaction <= lowSatThreshold
}

func riskScore(item ticket, timeoutHours float64) (float64, []string) {
	// 单票风险分用于“主管优先处理队列”，分数越高越应该先处理。
	// 分数组成：优先级基础分 + 未解决 + 超时 + 低满意度 + 满意度触底 + 处理时长加分。
	priorityWeights := map[string]float64{"高": 30, "中": 18, "低": 8}
	score := priorityWeights[item.Priority]
	reasons := []string{item.Priority + "优先级"}
	if !item.IsResolved {
		score += 25
		reasons = append(reasons, "未解决")
	}
	if timedOut(item, timeoutHours) {
		score += 20
		reasons = append(reasons, fmt.Sprintf("处理超时>%.0fh", timeoutHours))
	}
	if isLowSatisfaction(item) {
		score += 12
		reasons = append(reasons, fmt.Sprintf("低满意度%d分", item.Satisfaction))
	}
	if item.Satisfaction <= 1 {
		score += 8
		reasons = append(reasons, "满意度触底")
	}
	// 处理越久风险越高，每 24 小时加 4 分，但最多加 16 分，避免超长单无限拉高。
	score += math.Min((item.ResolutionTimeHour/24)*4, 16)
	if item.ResolutionTimeHour >= 48 {
		reasons = append(reasons, fmt.Sprintf("处理时长%gh", item.ResolutionTimeHour))
	}
	return round(score), reasons
}

func suggestedAction(category string) string {
	// 根据类别给主管一个操作方向，不直接修改工单状态。
	switch category {
	case "支付问题":
		return "优先核对支付流水、订单状态和退款路径，避免资金争议继续扩大。"
	case "退款退货":
		return "拉通售后和财务确认退款进度、运费报销与用户回访口径。"
	case "物流查询":
		return "联系承运商确认轨迹异常或签收争议，并同步用户预计处理时间。"
	case "投诉":
		return "由主管介入回访，确认服务体验问题是否需要升级处理。"
	case "账号问题":
		return "核验账号安全和身份信息，避免登录或风控问题扩大。"
	default:
		return "补充标准答复并确认用户问题是否已关闭。"
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func timedOut(item ticket, timeoutHours float64) bool {
	// 超时采用严格大于阈值：24h 阈值下，24h 本身不算超时，超过 24h 才算。
	return item.ResolutionTimeHour > timeoutHours
}

func calendarDays(tickets []ticket) []time.Time {
	// 生成样本覆盖的完整日期序列，趋势图需要连续日期而不是只展示有工单的日期。
	start := startOfDay(tickets[0].Created)
	end := start
	for _, item := range tickets {
		day := startOfDay(item.Created)
		if day.Before(start) {
			start = day
		}
		if day.After(end) {
			end = day
		}
	}
	days := []time.Time{}
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		days = append(days, day)
	}
	return days
}

func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func dateKey(value time.Time) string {
	return value.Format("2006-01-02")
}

func dateKeys(days []time.Time) []string {
	keys := []string{}
	for _, day := range days {
		keys = append(keys, dateKey(day))
	}
	return keys
}

func sortedCategories(tickets []ticket) []string {
	seen := map[string]bool{}
	for _, item := range tickets {
		seen[item.Category] = true
	}
	return sortedMapKeysBool(seen)
}

func sortedMapKeysBool(values map[string]bool) []string {
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys(values map[string][]ticket) []string {
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ticketsByCategory(tickets []ticket) map[string][]ticket {
	groups := map[string][]ticket{}
	for _, item := range tickets {
		groups[item.Category] = append(groups[item.Category], item)
	}
	return groups
}

func sumCategory(counts map[string]int, days []time.Time) int {
	total := 0
	for _, day := range days {
		total += counts[dateKey(day)]
	}
	return total
}

func totalCategory(counts map[string]int) int {
	total := 0
	for _, value := range counts {
		total += value
	}
	return total
}

func rate(numerator, denominator float64) float64 {
	// 比率统一入口，分母为 0 时返回 0，避免小样本窗口导致除零。
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func iqrStats(values []float64) map[string]any {
	// IQR = Q3 - Q1；upper_bound = Q3 + 1.5*IQR。
	// 这里用它标记处理时长长尾异常，同时把 Q1-Q3 展示为“大部分处理时间”。
	if len(values) == 0 {
		return map[string]any{"q1": 0.0, "q3": 0.0, "iqr": 0.0, "upper_bound": 0.0}
	}
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	q1 := percentile(sorted, 0.25)
	q3 := percentile(sorted, 0.75)
	iqr := q3 - q1
	return map[string]any{"q1": round(q1), "q3": round(q3), "iqr": round(iqr), "upper_bound": round(q3 + 1.5*iqr)}
}

func percentile(sorted []float64, p float64) float64 {
	// 百分位使用线性插值，样本不大时比简单取整更平滑。
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := p * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func round(value float64) float64 {
	return math.Round(value*100) / 100
}

func floatFromAny(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

func countBy(tickets []ticket, getKey func(ticket) string) map[string]int {
	counts := map[string]int{}
	for _, item := range tickets {
		counts[getKey(item)]++
	}
	return counts
}

func counterItems(counts map[string]int) []map[string]any {
	// 把 map 计数转成按数量降序的数组，方便前端直接渲染分布列表。
	items := []map[string]any{}
	for key, count := range counts {
		items = append(items, map[string]any{"name": key, "count": count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i]["count"].(int) != items[j]["count"].(int) {
			return items[i]["count"].(int) > items[j]["count"].(int)
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})
	return items
}

func anomalyTicketIDs(tickets []ticket, category string, timeoutHours float64) []string {
	// 异常代表工单按单票风险排序，优先展示最值得主管查看的几张。
	related := []ticket{}
	for _, item := range tickets {
		if item.Category == category {
			related = append(related, item)
		}
	}
	sort.Slice(related, func(i, j int) bool {
		left, _ := riskScore(related[i], timeoutHours)
		right, _ := riskScore(related[j], timeoutHours)
		return left > right
	})
	ids := []string{}
	for index, item := range related {
		if index >= 5 {
			break
		}
		ids = append(ids, item.TicketID)
	}
	return ids
}

func firstStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}
