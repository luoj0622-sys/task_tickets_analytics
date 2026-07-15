(function () {
  const DEFAULT_TIMEOUT_HOURS = 24;
  const $ = (selector) => document.querySelector(selector);

  let report = null;
  let timeoutThreshold = DEFAULT_TIMEOUT_HOURS;
  let requestCounter = 0;
  let debounceTimer = null;

  function percent(value) {
    return `${Math.round((value || 0) * 100)}%`;
  }

  function hours(value) {
    return `${Number(value || 0).toFixed(Number.isInteger(value) ? 0 : 1)}h`;
  }

  function lowSatisfactionShortText() {
    const config = report?.metadata?.config || {};
    const threshold = config.low_satisfaction_threshold ?? 2;
    const operator = config.low_satisfaction_operator || "<=";
    if (operator === "<") return `低于${threshold}分`;
    if (operator === "<=") return `${threshold}分及以下`;
    return `${operator}${threshold}分`;
  }

  function lowSatisfactionRuleText() {
    return `默认${lowSatisfactionShortText()}为低满意度`;
  }

  function compactNumber(value) {
    return Number(value || 0).toFixed(2).replace(/\.?0+$/, "");
  }

  function riskFormulaText() {
    const weights = report?.metadata?.config?.risk_weights || {};
    const priority = weights.priority || {};
    return [
      `风险分=优先级（高${priority["高"] ?? 30}/中${priority["中"] ?? 18}/低${priority["低"] ?? 8}）`,
      `未解决+${weights.unresolved ?? 25}`,
      `超时+${weights.timeout ?? 20}`,
      `低满意度（${lowSatisfactionShortText()}）+${weights.low_satisfaction ?? 12}`,
      `满意度触底+${weights.very_low_satisfaction ?? 8}`,
      `处理时长每24h+${weights.duration_per_24h ?? 4}（最高${weights.duration_cap ?? 16}）`,
    ].join("；");
  }

  function severityFormulaText() {
    const weights = report?.metadata?.config?.severity_weights || {};
    const thresholds = report?.metadata?.config?.severity_thresholds || {};
    return {
      formula: [
        `高优先级率×${weights.high_priority_rate ?? 25}`,
        `未解决率×${weights.unresolved_rate ?? 35}`,
        `低满意度率×${weights.low_satisfaction_rate ?? 25}`,
        `满意度下滑×${weights.satisfaction_drop_rate ?? 15}`,
      ].join(" + "),
      thresholds: `稳定 < ${thresholds.watch ?? 30}，观察 ≥ ${thresholds.watch ?? 30}，关注 ≥ ${thresholds.warning ?? 45}，严重 ≥ ${thresholds.critical ?? 60}`,
    };
  }

  function levelText(level) {
    const map = {
      critical: "严重",
      warning: "关注",
      watch: "观察",
      stable: "稳定",
    };
    return map[level] || "关注";
  }

  function levelClass(level) {
    if (level === "critical") return "badge-critical";
    if (level === "warning") return "badge-warning";
    if (level === "watch") return "badge-watch";
    if (level === "stable") return "badge-stable";
    return "badge-info";
  }

  function escapeHtml(value) {
    return String(value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;");
  }

  function setLoading(isLoading) {
    const input = $("#timeoutThreshold");
    if (input) input.disabled = isLoading;
    document.body.classList.toggle("is-loading", isLoading);
  }

  function showError(message) {
    document.body.innerHTML = `
      <main class="main">
        <section class="dashboard-section">
          <div class="empty-state">${escapeHtml(message)}</div>
        </section>
      </main>
    `;
  }

  async function loadReport(nextTimeout) {
    const requestId = ++requestCounter;
    timeoutThreshold = nextTimeout;
    setLoading(true);

    try {
      const response = await fetch(`/api/report?timeout=${encodeURIComponent(nextTimeout)}`);
      if (!response.ok) {
        const error = await response.json().catch(() => ({}));
        throw new Error(error.error || `报告接口返回 ${response.status}`);
      }
      const nextReport = await response.json();
      if (requestId !== requestCounter) return;
      report = nextReport;
      timeoutThreshold = Number(report.metadata.config.timeout_threshold_hours || nextTimeout);
      renderAll();
    } catch (error) {
      if (requestId === requestCounter) {
        showError(`加载报告失败：${error.message}`);
      }
    } finally {
      if (requestId === requestCounter) setLoading(false);
    }
  }

  function renderKpis() {
    const kpis = report.summary.kpis;
    const cards = [
      ["总工单", kpis.total_tickets, `${report.summary.period.day_count} 天样本`],
      ["未解决", kpis.unresolved_count, `占比 ${percent(kpis.unresolved_rate)}`],
      ["高优先级", kpis.high_priority_count, `占比 ${percent(kpis.high_priority_rate)}`],
      ["超时工单", kpis.timeout_count, `阈值 ${timeoutThreshold}h · 占比 ${percent(kpis.timeout_rate)}`],
      ["满意度", kpis.average_satisfaction, `${lowSatisfactionRuleText()} · ${kpis.low_satisfaction_count} 单`],
      ["处理时长", hours(kpis.average_resolution_hours), `中位 ${hours(kpis.median_resolution_hours)}`],
    ];

    $("#kpiGrid").innerHTML = cards.map(([label, value, note]) => `
      <article class="kpi-card">
        <div class="kpi-label">${label}</div>
        <div class="kpi-value">${value}</div>
        <div class="kpi-note">${note}</div>
      </article>
    `).join("");
  }

  function renderHealthSignals() {
    $("#healthSignals").innerHTML = report.summary.health_signals.map((signal) => `
      <article class="signal">
        <span class="badge ${levelClass(signal.level)}">${levelText(signal.level)}</span>
        <div>
          <strong>${escapeHtml(signal.headline)}</strong>
          <p>${escapeHtml(signal.detail)}</p>
          ${renderSignalTickets(signal)}
        </div>
      </article>
    `).join("");
  }

  function renderSignalTickets(signal) {
    const tickets = signal.related_tickets || [];
    if (!tickets.length) return "";
    return `
      <div class="signal-evidence" aria-label="${escapeHtml(signal.name)}关联工单">
        ${tickets.map((ticket) => `
          <div class="signal-ticket">
            <div class="signal-ticket-meta">
              <span>${escapeHtml(ticket.ticket_id)}</span>
              <span>${escapeHtml(ticket.category)}</span>
              <span>${escapeHtml(ticket.type)}</span>
            </div>
            <p>${escapeHtml(ticket.description)}</p>
          </div>
        `).join("")}
      </div>
    `;
  }

  function renderTopAnomalies() {
    $("#topAnomalies").innerHTML = report.anomalies.slice(0, 4).map((anomaly) => `
      <article class="anomaly">
        <span class="badge ${levelClass(anomaly.level)}">${escapeHtml(anomaly.type)}</span>
        <strong>${escapeHtml(anomaly.title)}</strong>
        <p>${escapeHtml(anomaly.why_it_matters)}</p>
      </article>
    `).join("");
  }

  function renderDailyTrend() {
    const data = report.trend.daily_totals;
    const max = Math.max(...data.map((item) => item.count), 1);
    $("#dailyTrendChart").innerHTML = data.map((item) => {
      const height = Math.max((item.count / max) * 100, 8);
      const label = item.date.slice(5);
      return `
        <div class="bar-column">
          <div class="bar-fill" style="height:${height}%"><span>${item.count}</span></div>
          <div class="bar-label">${label}</div>
        </div>
      `;
    }).join("");
  }

  function renderTrendSummary() {
    const growth = report.trend.category_growth;
    const window = report.trend.window;
    const recentTotal = growth.reduce((sum, item) => sum + Number(item.recent_count || 0), 0);
    const recentDailyAvg = recentTotal / Math.max(window.recent_day_count || 1, 1);
    const top = growth.find((item) => item.is_trend_anomaly) || growth[0];
    if (!top) {
      $("#trendSummary").innerHTML = "<strong>趋势总结</strong><p>暂无可用于趋势判断的类别数据。</p>";
      return;
    }
    const growthText = top.growth_multiple === null ? "新增" : `${top.growth_multiple}x`;
    const judgement = top.is_trend_anomaly ? "上升趋势明显" : "未达到异常增长阈值";
    $("#trendSummary").innerHTML = `
      <strong>趋势总结</strong>
      <p>最近 ${window.recent_day_count} 天共 ${recentTotal} 单，日均 ${compactNumber(recentDailyAvg)} 单；${escapeHtml(top.category)}近期日均 ${compactNumber(top.recent_daily_avg)} 单，对比基线 ${compactNumber(top.baseline_daily_avg)} 单，增长 ${growthText}，${judgement}。</p>
    `;
  }

  function renderGrowthTable() {
    $("#growthTable").innerHTML = report.trend.category_growth.map((item) => `
      <tr>
        <td><strong>${escapeHtml(item.category)}</strong>${item.is_trend_anomaly ? ' <span class="badge badge-warning">增长</span>' : ""}</td>
        <td>${item.baseline_daily_avg}</td>
        <td>${item.recent_daily_avg}</td>
        <td>${item.growth_multiple === null ? "新增" : `${item.growth_multiple}x`}</td>
      </tr>
    `).join("");
  }

  function renderSeverityTable() {
    $("#severityRuleNote").textContent = `区分数量多和风险高，${lowSatisfactionRuleText()}`;
    $("#lowSatisfactionHeader").textContent = `低满意度率（${lowSatisfactionShortText()}）`;
    const severityFormula = severityFormulaText();
    $("#severityFormulaNote").innerHTML = `
      <strong>风险标注计算</strong>
      <p>风险分=${severityFormula.formula}；其中满意度下滑=(5-平均满意度)/4，表格里的百分比按小数计算，例如 75% 按 0.75 参与计算。标注规则：${severityFormula.thresholds}。</p>
    `;
    $("#severityTable").innerHTML = report.severity.categories.map((item) => {
      const width = Math.min(item.risk_score, 100);
      return `
        <tr>
          <td><strong>${escapeHtml(item.category)}</strong></td>
          <td>${item.count}</td>
          <td>
            <div class="risk-bar">
              <span class="badge ${levelClass(item.risk_level)}">${item.risk_score}</span>
              <span class="risk-track"><span class="risk-fill" style="width:${width}%"></span></span>
            </div>
          </td>
          <td>${percent(item.high_priority_rate)}</td>
          <td>${percent(item.unresolved_rate)}</td>
          <td>${percent(item.low_satisfaction_rate)}</td>
          <td>${item.average_satisfaction}</td>
        </tr>
      `;
    }).join("");
  }

  function renderDurationTable() {
    $("#timeoutHeader").textContent = `超时（>${timeoutThreshold}h）`;
    $("#slaTable").innerHTML = report.sla_efficiency.categories.map((item) => `
      <tr>
        <td><strong>${escapeHtml(item.category)}</strong></td>
        <td>${hours(item.average_resolution_hours)}</td>
        <td>${hours(item.median_resolution_hours)}</td>
        <td>${item.timeout_count} <span class="chip">${percent(item.timeout_rate)}</span></td>
      </tr>
    `).join("");

    const overall = report.sla_efficiency.overall;
    const majority = overall.majority_resolution_hours || {};
    const iqr = overall.iqr || {};
    const majorityFrom = majority.from ?? iqr.q1 ?? 0;
    const majorityTo = majority.to ?? iqr.q3 ?? 0;
    const outlierThreshold = overall.outlier_threshold_hours ?? iqr.upper_bound ?? 0;
    const majorityNote = `
      <article class="duration-note">
        <strong>大部分处理时间：${hours(majorityFrom)}-${hours(majorityTo)}</strong>
        <p>按 ${escapeHtml(majority.method || "Q1-Q3")} 计算，超过 ${hours(outlierThreshold)} 的工单标记为处理时长异常工单。</p>
      </article>
    `;
    const outliers = report.sla_efficiency.outliers;
    const outlierCards = outliers.length ? outliers.map((ticket) => `
      <article class="outlier-card">
        <strong>${ticket.ticket_id} · ${escapeHtml(ticket.category)} · ${hours(ticket.resolution_time_hours)}</strong>
        <p>${escapeHtml(ticket.description)}</p>
      </article>
    `).join("") : '<div class="empty-state">未发现处理时长异常工单。</div>';
    $("#outlierList").innerHTML = majorityNote + outlierCards;
  }

  function renderThemes() {
    $("#themeGrid").innerHTML = report.recurring_themes.map((theme) => {
      const tickets = theme.ticket_ids.slice(0, 6).map((id) => `<span class="chip">${id}</span>`).join("");
      const categories = theme.categories.map((item) => `<span class="chip">${escapeHtml(item.name)} ${item.count}</span>`).join("");
      return `
        <article class="theme-card">
          <strong>${escapeHtml(theme.theme)} · ${theme.count}次</strong>
          <p>${escapeHtml(theme.why_it_matters)}</p>
          <div class="theme-meta">${categories}</div>
          <div class="theme-meta">${tickets}</div>
        </article>
      `;
    }).join("");
  }

  function renderQueue() {
    $("#queueRiskNote").textContent = riskFormulaText();
    $("#queueTable").innerHTML = report.priority_queue.slice(0, 12).map((ticket) => {
      const reasons = ticket.reason_labels.map((reason) => `<span class="chip">${escapeHtml(reason)}</span>`).join("");
      return `
        <tr>
          <td>${ticket.rank}</td>
          <td>
            <strong>${ticket.ticket_id}</strong>
            <div class="kpi-note">${escapeHtml(ticket.description)}</div>
          </td>
          <td>${escapeHtml(ticket.category)}</td>
          <td><span class="badge badge-critical">${ticket.risk_score}</span></td>
          <td><div class="queue-reasons">${reasons}</div></td>
          <td>${escapeHtml(ticket.suggested_action)}</td>
        </tr>
      `;
    }).join("");
  }

  function renderChrome() {
    const period = report.summary.period;
    $("#periodText").textContent = `${period.start} 至 ${period.end}，最近 ${report.trend.window.recent_day_count} 天对比前 ${report.trend.window.baseline_day_count} 天基线`;
    $("#ticketCount").textContent = `${report.metadata.ticket_count} 条工单`;
  }

  function renderAll() {
    renderChrome();
    renderKpis();
    renderHealthSignals();
    renderTopAnomalies();
    renderDailyTrend();
    renderTrendSummary();
    renderGrowthTable();
    renderSeverityTable();
    renderDurationTable();
    renderThemes();
    renderQueue();
  }

  function bindTimeoutSetting() {
    const input = $("#timeoutThreshold");
    input.value = String(DEFAULT_TIMEOUT_HOURS);
    input.addEventListener("input", () => {
      const nextValue = Number(input.value);
      if (!Number.isFinite(nextValue) || nextValue < 1) return;
      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        loadReport(Math.round(nextValue));
      }, 180);
    });
  }

  bindTimeoutSetting();
  loadReport(DEFAULT_TIMEOUT_HOURS);
})();
