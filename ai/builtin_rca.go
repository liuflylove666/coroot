package ai

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
)

// --- Enriched data structures ---

type finding struct {
	score      float64 // 0..100 composite score (higher = more likely root cause)
	severity   int     // 0=info, 1=warning, 2=critical
	category   string
	title      string
	detail     string
	evidence   string
	changeTime int64 // unix timestamp of the detected change point (0 = unknown)
}

type tsPoint struct {
	t int64
	v float64
}

type enrichedStats struct {
	min, max, avg, last, stddev float64
	count                       int
	changePoint                 int64   // unix timestamp where behaviour shifted
	changeScore                 float64 // 0..1 how dramatic the change was
	zScoreMax                   float64 // how many stddevs the max is from the mean
	trend                       float64 // linear regression slope (per-second)
	burstRatio                  float64 // P99/P50 ratio – measures spikiness
	iqrScore                    float64 // BARO: IQR-normalized deviation (robust to outliers)
	points                      []tsPoint
}

// --- Public entry point ---

func RunBuiltinRCA(data RCAData) *model.RCA {
	var findings []finding

	// Graph-first: dependency map edges (aligns with product "follow the dependency graph").
	findings = append(findings, detectDependencyPropagation(data)...)

	findings = append(findings, detectOOMKills(data.Metrics)...)
	findings = append(findings, detectRestarts(data.Metrics)...)
	findings = append(findings, detectCPUPressure(data.Metrics)...)
	findings = append(findings, detectMemoryPressure(data.Metrics)...)
	findings = append(findings, detectNetworkIssues(data.Metrics)...)
	findings = append(findings, detectHTTPErrors(data.Metrics)...)
	findings = append(findings, detectLatencyDegradation(data.Metrics)...)
	findings = append(findings, detectKubernetesEvents(data.Events)...)
	findings = append(findings, detectTraceAnomalies(data.ErrorTrace, data.SlowTrace)...)
	findings = append(findings, detectDeploymentCorrelation(data.Deployments, data.From, data.To)...)
	findings = append(findings, detectStatisticalAnomalies(data.Metrics)...)

	findings = append(findings, analyzeLogPatterns(data.LogPatterns, data.From, data.To)...)
	findings = append(findings, analyzeLogTimeline(data.LogTimeline, data.From, data.To)...)
	findings = append(findings, analyzeTraceGroups(data.TraceGroups)...)
	findings = append(findings, analyzeErrorSpans(data.ErrorSpans)...)
	findings = append(findings, analyzeCorrelatedLogs(data.CorrelatedLogs)...)

	findings = correlateFindings(findings, data)

	sort.Slice(findings, func(i, j int) bool {
		return findings[i].score > findings[j].score
	})

	if len(findings) == 0 {
		return &model.RCA{
			Status:       "OK",
			ShortSummary: "No significant anomalies detected",
			RootCause:    "The application appears healthy within the selected time window. No statistical anomalies, resource pressure, error traces, or concerning Kubernetes events were found.",
			ImmediateFixes: "- No action required at this time\n" +
				"- If you suspect an issue, try widening the time window\n" +
				"- Verify that monitoring agents are reporting data",
			DetailedRootCause: buildDetailedReport(data, nil),
			DependencyPeers:   data.DependencyPeers,
		}
	}

	rca := &model.RCA{Status: "OK"}
	rca.ShortSummary = synthesizeSummary(findings)
	rca.RootCause = synthesizeRootCause(findings)
	rca.ImmediateFixes = synthesizeFixes(findings)
	rca.DetailedRootCause = buildDetailedReport(data, findings)

	rca.CausalFindings = buildCausalFindings(findings)
	rca.RankedCauses = buildRankedCauses(data.Metrics, findings)
	rca.RelatedLogs = buildRelatedLogs(data)
	rca.RelatedTraces = buildRelatedTraces(data)
	rca.DependencyPeers = data.DependencyPeers

	return rca
}

func buildCausalFindings(findings []finding) []*model.CausalFinding {
	var result []*model.CausalFinding
	if len(findings) == 0 {
		return nil
	}

	maxScore := findings[0].score
	if maxScore <= 0 {
		maxScore = 1
	}

	for i, f := range findings {
		if i >= 6 {
			break
		}
		confidence := math.Min(f.score/maxScore*100, 100)

		services := extractServices(f.title)

		detail := f.detail
		if idx := strings.Index(detail, "\n\n> **Temporal correlation:"); idx > 0 {
			detail = detail[:idx]
		}
		if idx := strings.Index(detail, "\n\n> **Causal link:"); idx > 0 {
			detail = detail[:idx]
		}
		if idx := strings.Index(detail, "\n\n> **Graph correlation:"); idx > 0 {
			detail = detail[:idx]
		}
		if len(detail) > 300 {
			detail = detail[:297] + "..."
		}

		cf := &model.CausalFinding{
			Title:      f.title,
			Category:   f.category,
			Confidence: math.Round(confidence),
			Severity:   f.severity,
			Detail:     detail,
			Evidence:   f.evidence,
			Services:   services,
			Fixes:      findingFixes(f),
		}
		result = append(result, cf)
	}
	return result
}

func findingFixes(f finding) []string {
	switch {
	case f.category == "Memory" && strings.Contains(f.title, "OOM"):
		return []string{"扩容容器内存限制", "分析应用堆内存分配", "调整GC参数 (如 -Xmx, GOMEMLIMIT)"}
	case f.category == "Memory" && strings.Contains(f.title, "leak"):
		return []string{"捕获 Heap Dump 分析", "检查未关闭连接和无限缓存", "考虑滚动重启作为临时方案"}
	case f.category == "Memory":
		return []string{"增大 resources.limits.memory", "启用基于内存的HPA自动扩缩"}
	case f.category == "CPU" && strings.Contains(f.title, "throttl"):
		return []string{"增大CPU限制或移除限制", "分析CPU热点路径", "考虑水平扩展"}
	case f.category == "CPU":
		return []string{"扩展CPU资源配置", "通过HPA添加副本"}
	case f.category == "Stability":
		return []string{"检查容器崩溃日志", "审查资源限制配置", "验证存活/就绪探针配置"}
	case f.category == "Network":
		return []string{"检查上游服务健康状态", "审查NetworkPolicy和DNS", "检查连接池饱和度"}
	case f.category == "HTTP":
		return []string{"检查应用错误日志", "如与部署关联则考虑回滚", "验证数据库/缓存连接"}
	case f.category == "Deployment":
		return []string{"考虑回滚: kubectl rollout undo", "比较新旧版本差异", "检查部署事件"}
	case f.category == "Latency":
		return []string{"排查延迟降级端点", "检查资源争用", "审查连接池大小和超时配置"}
	case f.category == "Dependency":
		return []string{"在服务地图中检查相关服务的健康与资源", "审查与依赖之间的连通性与网络策略", "在异常时间窗内追踪指向该依赖的调用"}
	case f.category == "Traces":
		return []string{"检查瓶颈span的服务", "检查数据库查询性能", "审查下游服务SLI"}
	default:
		return []string{"持续监控相关指标", "关联应用日志分析上下文"}
	}
}

func buildRankedCauses(metrics map[string][]*model.MetricValues, findings []finding) []*model.RankedCause {
	type metricAnomaly struct {
		name    string
		service string
		score   float64
		detail  string
		points  []tsPoint
	}
	var anomalies []metricAnomaly

	// Collect the primary SLI series (HTTP errors / latency) as the "effect"
	// for Granger causal analysis
	var effectSeries []tsPoint
	for _, name := range []string{"container_http_requests_count", "container_http_requests_latency_total"} {
		for _, mv := range metrics[name] {
			if mv.Values == nil {
				continue
			}
			pts := collectPoints(mv.Values)
			if len(pts) > len(effectSeries) {
				effectSeries = pts
			}
		}
	}

	for name, values := range metrics {
		for _, mv := range values {
			if mv.Values == nil {
				continue
			}
			s := enrichedStatsFrom(mv.Values)
			if s.count < 5 || s.avg <= 0 {
				continue
			}

			score := 0.0
			if s.iqrScore > 1.5 {
				score += math.Min((s.iqrScore-1.5)*8, 30)
			}
			score += s.changeScore * 30
			if s.zScoreMax >= 3 {
				score += math.Min(s.zScoreMax*3, 20)
			}
			if s.burstRatio > 3 {
				score += math.Min((s.burstRatio-3)*2, 10)
			}
			if s.trend != 0 && s.avg > 0 {
				trendPct := math.Abs(s.trend*60) / s.avg * 100
				if trendPct > 5 {
					score += math.Min(trendPct, 10)
				}
			}

			// Granger causal boost: if this metric's changes precede
			// the primary SLI degradation, boost its score
			if len(effectSeries) > 10 && len(s.points) > 10 {
				causalScore := GrangerCausalScore(s.points, effectSeries, 5)
				if causalScore > 0.3 {
					score += causalScore * 20
				}
			}

			if score < 15 {
				continue
			}

			service := labelVal(mv.Labels, "container_id")
			if service == "unknown" {
				service = labelVal(mv.Labels, "destination")
			}

			anomalies = append(anomalies, metricAnomaly{
				name:    name,
				service: service,
				score:   score,
				points:  s.points,
				detail: fmt.Sprintf("IQR-score=%.2f, z-score=%.1f, change=%.0f%%, burst=%.1fx",
					s.iqrScore, s.zScoreMax, s.changeScore*100, s.burstRatio),
			})
		}
	}

	for _, f := range findings {
		if f.category == "Deployment" || f.category == "Kubernetes" || f.category == "Logs" {
			anomalies = append(anomalies, metricAnomaly{
				name:    f.title,
				service: "",
				score:   f.score,
				detail:  f.evidence,
			})
		}
	}

	sort.Slice(anomalies, func(i, j int) bool {
		return anomalies[i].score > anomalies[j].score
	})

	seen := map[string]bool{}
	var result []*model.RankedCause
	maxScore := 0.0
	if len(anomalies) > 0 {
		maxScore = anomalies[0].score
	}
	if maxScore <= 0 {
		maxScore = 1
	}

	for _, a := range anomalies {
		if len(result) >= 8 {
			break
		}
		key := a.name + "|" + a.service
		if seen[key] {
			continue
		}
		seen[key] = true

		confidence := math.Min(a.score/maxScore*100, 100)
		metric := a.name
		if len(metric) > 60 {
			metric = metric[:57] + "..."
		}
		result = append(result, &model.RankedCause{
			Metric:     metric,
			Service:    a.service,
			Confidence: math.Round(confidence),
			Detail:     a.detail,
		})
	}
	return result
}

func buildRelatedLogs(data RCAData) []*model.RCALogEntry {
	var result []*model.RCALogEntry

	if len(data.CorrelatedLogs) > 0 {
		for i, l := range data.CorrelatedLogs {
			if i >= 20 {
				break
			}
			result = append(result, &model.RCALogEntry{
				Timestamp: l.Timestamp.Format("15:04:05"),
				Severity:  l.Severity.String(),
				Service:   l.ServiceName,
				Message:   truncate(l.Body, 200),
			})
		}
	}

	for i, p := range data.LogPatterns {
		if i >= 10 || len(result) >= 30 {
			break
		}
		if p.Severity >= model.SeverityWarning {
			result = append(result, &model.RCALogEntry{
				Timestamp: p.LastSeen.Format("15:04:05"),
				Severity:  p.Severity.String(),
				Service:   p.ServiceName,
				Message:   truncate(p.Sample, 200),
			})
		}
	}
	return result
}

func buildRelatedTraces(data RCAData) []*model.RCATraceEntry {
	var result []*model.RCATraceEntry

	if data.ErrorTrace != nil {
		for i, span := range data.ErrorTrace.Spans {
			if i >= 10 {
				break
			}
			result = append(result, &model.RCATraceEntry{
				TraceID:  truncate(span.TraceId, 12),
				Service:  span.ServiceName,
				Duration: span.Duration.String(),
				Time:     span.Timestamp.Format("15:04:05"),
				Status:   span.StatusCode,
			})
		}
	}

	if data.SlowTrace != nil {
		for i, span := range data.SlowTrace.Spans {
			if i >= 10 || len(result) >= 15 {
				break
			}
			result = append(result, &model.RCATraceEntry{
				TraceID:  truncate(span.TraceId, 12),
				Service:  span.ServiceName,
				Duration: span.Duration.String(),
				Time:     span.Timestamp.Format("15:04:05"),
				Status:   span.StatusCode,
			})
		}
	}

	for _, g := range data.TraceGroups {
		if len(result) >= 20 {
			break
		}
		if g.ErrorCount > 0 || g.P99DurationMs > 1000 {
			result = append(result, &model.RCATraceEntry{
				TraceID:  truncate(g.SampleTraceId, 12),
				Service:  g.ServiceName,
				Duration: fmt.Sprintf("%.0fms", g.P99DurationMs),
				Time:     "",
				Status:   g.StatusCode,
			})
		}
	}
	return result
}

// --- Statistical helpers ---

func collectPoints(ts *timeseries.TimeSeries) []tsPoint {
	if ts == nil {
		return nil
	}
	var pts []tsPoint
	iter := ts.Iter()
	for iter.Next() {
		t, v := iter.Value()
		if timeseries.IsNaN(v) {
			continue
		}
		pts = append(pts, tsPoint{t: int64(t), v: float64(v)})
	}
	return pts
}

func enrichedStatsFrom(ts *timeseries.TimeSeries) enrichedStats {
	pts := collectPoints(ts)
	if len(pts) == 0 {
		return enrichedStats{}
	}

	var s enrichedStats
	s.points = pts
	s.count = len(pts)
	s.min = math.MaxFloat64
	s.max = -math.MaxFloat64
	sum := 0.0

	for _, p := range pts {
		if p.v < s.min {
			s.min = p.v
		}
		if p.v > s.max {
			s.max = p.v
		}
		sum += p.v
	}
	s.avg = sum / float64(s.count)
	s.last = pts[len(pts)-1].v

	// Standard deviation
	if s.count > 1 {
		varSum := 0.0
		for _, p := range pts {
			d := p.v - s.avg
			varSum += d * d
		}
		s.stddev = math.Sqrt(varSum / float64(s.count-1))
	}

	// Z-score of the max
	if s.stddev > 0 {
		s.zScoreMax = (s.max - s.avg) / s.stddev
	}

	// Burst ratio (P99/P50)
	if s.count >= 10 {
		sorted := make([]float64, s.count)
		for i, p := range pts {
			sorted[i] = p.v
		}
		sort.Float64s(sorted)
		p50 := sorted[s.count/2]
		p99 := sorted[int(float64(s.count)*0.99)]
		if p50 > 0 {
			s.burstRatio = p99 / p50
		}
	}

	// Linear regression slope (trend per second)
	if s.count >= 3 {
		s.trend = linearSlope(pts)
	}

	// Change-point detection: BOCPD (Bayesian Online Change Point Detection)
	// from BARO (FSE 2024). Falls back to CUSUM if BOCPD finds nothing.
	s.changePoint, s.changeScore = bocpdDetect(pts, 50)
	if s.changePoint == 0 {
		s.changePoint, s.changeScore = detectChangePoint(pts, s.avg, s.stddev)
	}

	// IQR-based anomaly score (BARO RobustScorer)
	s.iqrScore = robustScoreFromStats(pts)

	if s.min > 1e17 {
		s.min = 0
	}
	return s
}

// detectChangePoint uses a cumulative sum (CUSUM) approach.
// It walks the time series computing cumulative deviation from the overall mean.
// The point of maximum cumulative deviation is the most likely change point.
func detectChangePoint(pts []tsPoint, mean, stddev float64) (int64, float64) {
	if len(pts) < 6 || stddev <= 0 {
		return 0, 0
	}

	// Compute cumulative sum of deviations from mean
	cusum := make([]float64, len(pts))
	cusum[0] = 0
	for i := 1; i < len(pts); i++ {
		cusum[i] = cusum[i-1] + (pts[i].v - mean)
	}

	// Find the index with maximum absolute CUSUM value
	maxIdx := 0
	maxAbsDev := 0.0
	for i, c := range cusum {
		abs := math.Abs(c)
		if abs > maxAbsDev {
			maxAbsDev = abs
			maxIdx = i
		}
	}

	// Compute the mean before and after the change point
	if maxIdx < 2 || maxIdx > len(pts)-3 {
		return 0, 0
	}

	sumBefore, sumAfter := 0.0, 0.0
	for i := 0; i < maxIdx; i++ {
		sumBefore += pts[i].v
	}
	for i := maxIdx; i < len(pts); i++ {
		sumAfter += pts[i].v
	}
	meanBefore := sumBefore / float64(maxIdx)
	meanAfter := sumAfter / float64(len(pts)-maxIdx)

	// Change score = how many stddevs the mean shifted
	shift := math.Abs(meanAfter-meanBefore) / stddev
	if shift < 2.0 {
		return 0, 0
	}

	return pts[maxIdx].t, math.Min(shift/5.0, 1.0) // normalize to 0..1
}

// linearSlope computes the least-squares regression slope.
func linearSlope(pts []tsPoint) float64 {
	n := float64(len(pts))
	if n < 3 {
		return 0
	}
	t0 := float64(pts[0].t)
	sumX, sumY, sumXY, sumX2 := 0.0, 0.0, 0.0, 0.0
	for _, p := range pts {
		x := float64(p.t) - t0
		sumX += x
		sumY += p.v
		sumXY += x * p.v
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}

// anomalyScore combines multiple signals into a 0-100 composite score.
// Uses both classical z-score and BARO IQR-based scoring for robustness.
func anomalyScore(s enrichedStats, severity int) float64 {
	score := 0.0
	// Z-score contribution (max 20 points) — require ≥3σ to reduce false positives
	if s.zScoreMax > 3 {
		score += math.Min(s.zScoreMax*5, 20)
	}
	// BARO IQR score contribution (max 15 points) — robust to outliers
	if s.iqrScore > 1.5 {
		score += math.Min((s.iqrScore-1.5)*5, 15)
	}
	// Change-point contribution (max 25 points)
	score += s.changeScore * 25
	// Burst ratio contribution (max 10 points) — require ≥5× burst to reduce noise
	if s.burstRatio > 5 {
		score += math.Min((s.burstRatio-5)*3, 10)
	}
	// Severity base (max 30 points)
	score += float64(severity) * 15
	return math.Min(score, 100)
}

// --- Detectors ---

// detectDependencyPropagation uses service-map edges from RCADependencyPeers (when World is available).
// Mirrors the product flow: traverse the dependency graph and surface unhealthy peers before metric-only detectors.
func detectDependencyPropagation(data RCAData) []finding {
	if len(data.DependencyPeers) == 0 {
		return nil
	}
	var out []finding
	for _, p := range data.DependencyPeers {
		sev := 0
		if p.ConnectionStatus == "critical" {
			sev = 2
		}
		if p.AppStatus == "critical" && sev < 2 {
			sev = 2
		}
		if p.AppStatus == "warning" && sev < 1 {
			sev = 1
		}
		if sev == 0 {
			continue
		}
		dir := p.Direction
		if dir == "" {
			dir = "peer"
		}
		name := p.Name
		if name == "" {
			name = p.ApplicationID
		}
		title := fmt.Sprintf("%s service `%s` — dependency graph issue", dir, name)
		detail := fmt.Sprintf("**%s** (`%s`) on the dependency graph shows **%s** application status", dir, name, p.AppStatus)
		if p.ConnectionHint != "" {
			detail += fmt.Sprintf(" with **%s** on the connection (%s).", p.ConnectionHint, p.ConnectionStatus)
		} else {
			detail += "."
		}
		detail += "\n\nThis is evaluated **before** metric anomaly detectors so that upstream/downstream issues are candidates for root cause."
		evidence := fmt.Sprintf("dependency_peer: dir=%s app=%s app_status=%s conn=%s", dir, p.ApplicationID, p.AppStatus, p.ConnectionStatus)
		score := 35.0 + float64(sev)*22
		if p.ConnectionStatus == "critical" {
			score += 15
		}
		out = append(out, finding{
			severity:   sev,
			category:   "Dependency",
			title:      title,
			detail:     detail,
			evidence:   evidence,
			changeTime: 0,
			score:      math.Min(score, 100),
		})
	}
	return out
}

func detectOOMKills(metrics map[string][]*model.MetricValues) []finding {
	var results []finding
	for _, mv := range metrics["container_oom_kills_total"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.max <= 0 {
			continue
		}
		cid := labelVal(mv.Labels, "container_id")
		f := finding{
			severity:   2,
			category:   "Memory",
			title:      fmt.Sprintf("OOM Kill — container `%s`", cid),
			detail:     fmt.Sprintf("Container `%s` was killed by the OOM killer. This indicates the process exceeded its memory limit.\n\n**OOM kills:** %.0f (total counter max)", cid, s.max),
			evidence:   fmt.Sprintf("container_oom_kills_total: max=%.0f, z-score=%.1f", s.max, s.zScoreMax),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, 2) + 20 // OOM is almost always a root cause
		results = append(results, f)
	}
	return results
}

func detectRestarts(metrics map[string][]*model.MetricValues) []finding {
	var results []finding
	for _, mv := range metrics["container_restarts"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		delta := s.last - s.min
		if delta <= 0 {
			continue
		}
		cid := labelVal(mv.Labels, "container_id")
		sev := 1
		if delta >= 3 {
			sev = 2
		}
		f := finding{
			severity:   sev,
			category:   "Stability",
			title:      fmt.Sprintf("Container `%s` restarted %.0f time(s)", cid, delta),
			detail:     fmt.Sprintf("Container `%s` restarted **%.0f** time(s) during the analysis window. Frequent restarts indicate crash loops, OOM kills, or failed health probes.", cid, delta),
			evidence:   fmt.Sprintf("container_restarts: delta=%.0f, trend=%.4f/s", delta, s.trend),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, sev) + delta*5
		results = append(results, f)
	}
	return results
}

func detectCPUPressure(metrics map[string][]*model.MetricValues) []finding {
	var results []finding

	limitMap := buildLimitMap(metrics["container_cpu_limit"])

	for _, mv := range metrics["container_throttled_time"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.avg < 0.10 {
			continue
		}
		cid := labelVal(mv.Labels, "container_id")
		sev := 1
		if s.avg > 0.5 {
			sev = 2
		}
		f := finding{
			severity:   sev,
			category:   "CPU",
			title:      fmt.Sprintf("CPU throttling on `%s`", cid),
			detail:     fmt.Sprintf("Container `%s` is being CPU-throttled **%.0f%%** of the time on average (peak %.0f%%). The process is requesting CPU time that exceeds its cgroup limit.", cid, s.avg*100, s.max*100),
			evidence:   fmt.Sprintf("throttled_time: avg=%.4f, max=%.4f, z-score=%.1f, change_score=%.2f", s.avg, s.max, s.zScoreMax, s.changeScore),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, sev)
		results = append(results, f)
	}

	for _, mv := range metrics["container_cpu_usage"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		cid := labelVal(mv.Labels, "container_id")
		limit := limitMap[cid]
		if limit > 0 {
			utilPct := (s.max / limit) * 100
			if utilPct > 85 {
				sev := 1
				if utilPct > 95 {
					sev = 2
				}
				f := finding{
					severity:   sev,
					category:   "CPU",
					title:      fmt.Sprintf("High CPU utilization on `%s` (%.0f%%)", cid, utilPct),
					detail:     fmt.Sprintf("Container `%s` reached **%.1f%%** CPU utilization (%.3f / %.3f cores). Trend: %+.6f cores/s.", cid, utilPct, s.max, limit, s.trend),
					evidence:   fmt.Sprintf("cpu_usage: max=%.4f, limit=%.4f, z-score=%.1f, trend=%+.6f/s", s.max, limit, s.zScoreMax, s.trend),
					changeTime: s.changePoint,
				}
				f.score = anomalyScore(s, sev)
				results = append(results, f)
			}
		}
	}
	return results
}

func detectMemoryPressure(metrics map[string][]*model.MetricValues) []finding {
	var results []finding

	limitMap := buildLimitMap(metrics["container_memory_limit"])

	for _, mv := range metrics["container_memory_rss"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		cid := labelVal(mv.Labels, "container_id")

		// Memory near limit
		if limit, ok := limitMap[cid]; ok && limit > 0 {
			utilPct := (s.max / limit) * 100
			if utilPct > 80 {
				sev := 1
				if utilPct > 95 {
					sev = 2
				}
				f := finding{
					severity:   sev,
					category:   "Memory",
					title:      fmt.Sprintf("Memory pressure on `%s` (%.0f%% of limit)", cid, utilPct),
					detail:     fmt.Sprintf("Container `%s` memory RSS peaked at **%.1f%%** of its limit (%s / %s). At this level, OOM kills become likely.", cid, utilPct, fmtBytes(s.max), fmtBytes(limit)),
					evidence:   fmt.Sprintf("memory_rss: max=%s, limit=%s, z-score=%.1f", fmtBytes(s.max), fmtBytes(limit), s.zScoreMax),
					changeTime: s.changePoint,
				}
				f.score = anomalyScore(s, sev)
				results = append(results, f)
			}
		}

		// Memory leak detection via trend analysis
		if s.count >= 10 && s.trend > 0 && s.min > 0 {
			growthPct := (s.last - s.min) / s.min * 100
			if growthPct > 30 && (s.last-s.min) > 50*1024*1024 {
				// Project when it would hit the limit
				projection := ""
				if limit, ok := limitMap[cid]; ok && limit > 0 && s.trend > 0 {
					remaining := limit - s.last
					if remaining > 0 {
						secsToOOM := remaining / s.trend
						if secsToOOM < 86400 {
							projection = fmt.Sprintf(" At current rate, OOM in **%.0f minutes**.", secsToOOM/60)
						}
					}
				}
				f := finding{
					severity:   1,
					category:   "Memory",
					title:      fmt.Sprintf("Possible memory leak in `%s` (+%.1f%%)", cid, growthPct),
					detail:     fmt.Sprintf("Container `%s` memory grew **%.1f%%** (%s → %s) with a sustained upward trend of %s/min.%s", cid, growthPct, fmtBytes(s.min), fmtBytes(s.last), fmtBytes(s.trend*60), projection),
					evidence:   fmt.Sprintf("memory_rss: min=%s, last=%s, trend=%+.2f bytes/s, R²=high", fmtBytes(s.min), fmtBytes(s.last), s.trend),
					changeTime: s.changePoint,
				}
				f.score = anomalyScore(s, 1) + 10
				results = append(results, f)
			}
		}
	}
	return results
}

func detectNetworkIssues(metrics map[string][]*model.MetricValues) []finding {
	var results []finding

	for _, mv := range metrics["container_net_tcp_retransmits"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.avg < 1.0 {
			continue
		}
		dest := labelVal(mv.Labels, "destination")
		sev := 1
		if s.avg > 5 {
			sev = 2
		}
		f := finding{
			severity:   sev,
			category:   "Network",
			title:      fmt.Sprintf("TCP retransmissions to `%s`", dest),
			detail:     fmt.Sprintf("Elevated TCP retransmissions to `%s` (avg %.1f/s, peak %.1f/s). This suggests packet loss, network congestion, or an overloaded remote service.", dest, s.avg, s.max),
			evidence:   fmt.Sprintf("tcp_retransmits: avg=%.2f, max=%.2f, z-score=%.1f", s.avg, s.max, s.zScoreMax),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, sev)
		results = append(results, f)
	}

	for _, mv := range metrics["container_net_tcp_failed_connects"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.avg < 0.1 {
			continue
		}
		dest := labelVal(mv.Labels, "destination")
		f := finding{
			severity:   2,
			category:   "Network",
			title:      fmt.Sprintf("Connection failures to `%s`", dest),
			detail:     fmt.Sprintf("TCP connections to `%s` are failing (avg %.2f/s, peak %.2f/s). The upstream service may be unreachable or refusing connections.", dest, s.avg, s.max),
			evidence:   fmt.Sprintf("tcp_failed_connects: avg=%.2f, max=%.2f, z-score=%.1f", s.avg, s.max, s.zScoreMax),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, 2)
		results = append(results, f)
	}

	for _, mv := range metrics["container_net_latency"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.avg < 0.02 {
			continue
		}
		dest := labelVal(mv.Labels, "destination_ip")
		sev := 0
		if s.avg > 0.05 {
			sev = 1
		}
		if s.avg > 0.2 {
			sev = 2
		}
		f := finding{
			severity:   sev,
			category:   "Latency",
			title:      fmt.Sprintf("Network latency to `%s` (avg %.1fms)", dest, s.avg*1000),
			detail:     fmt.Sprintf("Network latency to `%s`: avg **%.1f ms**, peak **%.1f ms**. Burst ratio (P99/P50): **%.1fx**.", dest, s.avg*1000, s.max*1000, s.burstRatio),
			evidence:   fmt.Sprintf("net_latency: avg=%.4fs, max=%.4fs, z-score=%.1f, burst_ratio=%.1f", s.avg, s.max, s.zScoreMax, s.burstRatio),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, sev)
		results = append(results, f)
	}
	return results
}

func detectHTTPErrors(metrics map[string][]*model.MetricValues) []finding {
	var results []finding
	for _, mv := range metrics["container_http_requests_count"] {
		if mv.Values == nil {
			continue
		}
		status := labelVal(mv.Labels, "status")
		if !strings.HasPrefix(status, "5") {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.avg <= 0 {
			continue
		}
		dest := labelVal(mv.Labels, "destination")
		f := finding{
			severity:   2,
			category:   "HTTP",
			title:      fmt.Sprintf("HTTP %s errors to `%s`", status, dest),
			detail:     fmt.Sprintf("Server errors (HTTP %s) to `%s` at **%.1f req/s** average, peak **%.1f req/s**. Change-point confidence: **%.0f%%**.", status, dest, s.avg, s.max, s.changeScore*100),
			evidence:   fmt.Sprintf("http_requests[status=%s]: avg=%.2f, max=%.2f, z-score=%.1f, change=%.2f", status, s.avg, s.max, s.zScoreMax, s.changeScore),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, 2)
		results = append(results, f)
	}
	return results
}

func detectLatencyDegradation(metrics map[string][]*model.MetricValues) []finding {
	var results []finding
	for _, mv := range metrics["container_http_requests_latency_total"] {
		if mv.Values == nil {
			continue
		}
		s := enrichedStatsFrom(mv.Values)
		if s.avg <= 0 || s.zScoreMax < 3 {
			continue
		}
		dest := labelVal(mv.Labels, "destination")
		f := finding{
			severity:   1,
			category:   "Latency",
			title:      fmt.Sprintf("Latency degradation to `%s`", dest),
			detail:     fmt.Sprintf("HTTP request latency to `%s` shows degradation: avg=%.4fs, peak=%.4fs (z-score=%.1f). Burst ratio: %.1fx.", dest, s.avg, s.max, s.zScoreMax, s.burstRatio),
			evidence:   fmt.Sprintf("http_latency: avg=%.4f, max=%.4f, z-score=%.1f, change_score=%.2f", s.avg, s.max, s.zScoreMax, s.changeScore),
			changeTime: s.changePoint,
		}
		f.score = anomalyScore(s, 1)
		results = append(results, f)
	}
	return results
}

func detectKubernetesEvents(events []*model.LogEntry) []finding {
	if len(events) == 0 {
		return nil
	}

	type eventBucket struct {
		msg       string
		count     int
		firstSeen time.Time
		lastSeen  time.Time
		severity  model.Severity
	}

	buckets := make(map[string]*eventBucket)
	for _, e := range events {
		if e.Severity < model.SeverityWarning {
			continue
		}
		key := truncate(e.Body, 120)
		b, ok := buckets[key]
		if !ok {
			b = &eventBucket{msg: key, firstSeen: e.Timestamp, lastSeen: e.Timestamp, severity: e.Severity}
			buckets[key] = b
		}
		b.count++
		if e.Timestamp.Before(b.firstSeen) {
			b.firstSeen = e.Timestamp
		}
		if e.Timestamp.After(b.lastSeen) {
			b.lastSeen = e.Timestamp
		}
		if e.Severity > b.severity {
			b.severity = e.Severity
		}
	}

	if len(buckets) == 0 {
		return nil
	}

	sorted := make([]*eventBucket, 0, len(buckets))
	for _, b := range buckets {
		sorted = append(sorted, b)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].severity != sorted[j].severity {
			return sorted[i].severity > sorted[j].severity
		}
		return sorted[i].count > sorted[j].count
	})

	totalCount := 0
	for _, b := range sorted {
		totalCount += b.count
	}

	var lines []string
	for i, b := range sorted {
		if i >= 5 {
			break
		}
		lines = append(lines, fmt.Sprintf("- **[%s]** `%s` ×%d (first: %s, last: %s)",
			b.severity.String(), b.msg, b.count,
			b.firstSeen.Format("15:04:05"), b.lastSeen.Format("15:04:05")))
	}

	sev := 1
	if totalCount > 20 || sorted[0].severity >= model.SeverityError {
		sev = 2
	}

	score := float64(sev)*15 + math.Min(float64(totalCount)*2, 30) + float64(len(buckets))*3
	return []finding{{
		severity: sev,
		category: "Kubernetes",
		title:    fmt.Sprintf("%d K8s warning/error events (%d patterns)", totalCount, len(buckets)),
		detail:   fmt.Sprintf("**%d** warning/error Kubernetes events across **%d** unique patterns:\n\n%s", totalCount, len(buckets), strings.Join(lines, "\n")),
		evidence: fmt.Sprintf("k8s_events: total=%d, patterns=%d, top_severity=%s", totalCount, len(buckets), sorted[0].severity.String()),
		score:    math.Min(score, 95),
	}}
}

func detectTraceAnomalies(errorTrace, slowTrace *model.Trace) []finding {
	var results []finding

	if errorTrace != nil && len(errorTrace.Spans) > 0 {
		var lines []string
		bottleneck := ""
		var maxDur time.Duration
		for i, span := range errorTrace.Spans {
			if span.Duration > maxDur {
				maxDur = span.Duration
				bottleneck = span.ServiceName + "/" + span.Name
			}
			if i < 5 {
				statusInfo := span.StatusCode
				if span.StatusMessage != "" {
					statusInfo += ": " + span.StatusMessage
				}
				lines = append(lines, fmt.Sprintf("- `%s` → `%s` | %s | %s", span.ServiceName, span.Name, span.Duration, statusInfo))
			}
		}
		if len(errorTrace.Spans) > 5 {
			lines = append(lines, fmt.Sprintf("- ... and %d more spans", len(errorTrace.Spans)-5))
		}

		results = append(results, finding{
			severity: 2,
			category: "Traces",
			title:    fmt.Sprintf("Error trace — %d spans (bottleneck: `%s`)", len(errorTrace.Spans), bottleneck),
			detail:   fmt.Sprintf("A failing request was captured with **%d spans**. The slowest span is `%s` (%s).\n\n%s", len(errorTrace.Spans), bottleneck, maxDur, strings.Join(lines, "\n")),
			evidence: fmt.Sprintf("error_trace: spans=%d, bottleneck=%s(%s)", len(errorTrace.Spans), bottleneck, maxDur),
			score:    75,
		})
	}

	if slowTrace != nil && len(slowTrace.Spans) > 0 {
		var lines []string
		bottleneck := ""
		var maxDur time.Duration
		for i, span := range slowTrace.Spans {
			if span.Duration > maxDur {
				maxDur = span.Duration
				bottleneck = span.ServiceName + "/" + span.Name
			}
			if i < 5 {
				lines = append(lines, fmt.Sprintf("- `%s` → `%s` | %s", span.ServiceName, span.Name, span.Duration))
			}
		}
		results = append(results, finding{
			severity: 1,
			category: "Traces",
			title:    fmt.Sprintf("Slow trace (SLO violation) — bottleneck: `%s`", bottleneck),
			detail:   fmt.Sprintf("A request exceeding the SLO threshold was captured with **%d spans**. The slowest span is `%s` (%s).\n\n%s", len(slowTrace.Spans), bottleneck, maxDur, strings.Join(lines, "\n")),
			evidence: fmt.Sprintf("slow_trace: spans=%d, bottleneck=%s(%s)", len(slowTrace.Spans), bottleneck, maxDur),
			score:    50,
		})
	}
	return results
}

func detectDeploymentCorrelation(deployments map[model.ApplicationId][]*model.ApplicationDeployment, from, to time.Time) []finding {
	var results []finding
	for appID, deploys := range deployments {
		for _, d := range deploys {
			startTime := time.Unix(int64(d.StartedAt), 0)
			if !startTime.After(from) || !startTime.Before(to) {
				continue
			}
			// Score higher if deployment is closer to the start of the anomaly window
			windowDuration := to.Sub(from).Seconds()
			deployOffset := startTime.Sub(from).Seconds()
			proximityScore := 1.0 - (deployOffset / windowDuration)

			f := finding{
				severity:   2,
				category:   "Deployment",
				title:      fmt.Sprintf("Deployment `%s` correlated with anomaly", d.Name),
				detail:     fmt.Sprintf("Application `%s` was deployed (`%s`) at **%s**, which falls **within the anomaly window**. Deployments are a leading cause of incidents — verify the changes in this release.", appID.String(), d.Name, startTime.Format("15:04:05")),
				evidence:   fmt.Sprintf("deployment: %s at %s, proximity_score=%.2f", d.Name, startTime.Format(time.RFC3339), proximityScore),
				changeTime: startTime.Unix(),
				score:      40 + proximityScore*40,
			}
			results = append(results, f)
		}
	}
	return results
}

// detectStatisticalAnomalies uses the BARO algorithm (FSE 2024) for robust
// anomaly detection.  It combines BOCPD change-point detection with IQR-based
// scoring (RobustScorer), both ported from github.com/phamquiluan/baro.
//
// Compared to the previous z-score-only approach:
//   - BOCPD provides principled Bayesian change-point detection
//   - IQR scoring is robust to outliers (uses median/IQR not mean/stddev)
//   - Composite scoring weights IQR, change-point, and z-score together
func detectStatisticalAnomalies(metrics map[string][]*model.MetricValues) []finding {
	covered := map[string]bool{
		"container_oom_kills_total": true, "container_restarts": true,
		"container_cpu_usage": true, "container_cpu_limit": true,
		"container_throttled_time": true, "container_memory_rss": true,
		"container_memory_limit": true, "container_net_tcp_retransmits": true,
		"container_net_tcp_failed_connects": true, "container_http_requests_count": true,
		"container_net_latency": true, "container_http_requests_latency_total": true,
		"container_memory_cache": true, "container_memory_pressure": true,
	}

	type scoredAnomaly struct {
		name   string
		labels string
		stats  enrichedStats
		score  float64
	}
	var anomalies []scoredAnomaly

	for name, values := range metrics {
		if covered[name] {
			continue
		}
		for _, mv := range values {
			if mv.Values == nil {
				continue
			}
			s := enrichedStatsFrom(mv.Values)
			if s.count < 5 || s.avg <= 0 || s.stddev <= 0 {
				continue
			}

			// BARO-style composite: IQR score (primary) + change-point + z-score (secondary)
			score := 0.0

			// IQR contribution (max 25) — robust to outliers
			if s.iqrScore > 2.0 {
				score += math.Min((s.iqrScore-2.0)*5, 25)
			}

			// BOCPD change-point contribution (max 20)
			score += s.changeScore * 20

			// Z-score as secondary signal (max 10)
			if s.zScoreMax >= 4 {
				score += math.Min(s.zScoreMax*2, 10)
			}

			if s.count < 10 {
				score *= 0.3
			}

			if score < 20 {
				continue
			}

			anomalies = append(anomalies, scoredAnomaly{
				name:   name,
				labels: formatLabels(mv.Labels),
				stats:  s,
				score:  score,
			})
		}
	}

	sort.Slice(anomalies, func(i, j int) bool {
		return anomalies[i].score > anomalies[j].score
	})

	seenMetric := map[string]bool{}
	var results []finding
	for _, a := range anomalies {
		if len(results) >= 5 {
			break
		}
		if seenMetric[a.name] {
			continue
		}
		seenMetric[a.name] = true
		sev := 0
		if a.score > 30 {
			sev = 1
		}
		if a.score > 55 {
			sev = 2
		}

		cpInfo := ""
		if a.stats.changePoint > 0 {
			cpInfo = fmt.Sprintf(" Change detected at **%s** (BOCPD).", time.Unix(a.stats.changePoint, 0).Format("15:04:05"))
		}

		cappedScore := math.Min(a.score, 50)
		results = append(results, finding{
			severity:   sev,
			category:   "Anomaly",
			title:      fmt.Sprintf("Statistical anomaly (BARO): `%s`", a.name),
			detail:     fmt.Sprintf("Metric `%s` %s: IQR-score=**%.2f**, z-score=**%.1f**, change confidence=**%.0f%%**.%s", a.name, a.labels, a.stats.iqrScore, a.stats.zScoreMax, a.stats.changeScore*100, cpInfo),
			evidence:   fmt.Sprintf("%s: min=%.4g, avg=%.4g, max=%.4g, IQR-score=%.2f, z=%.1f, change=%.2f, trend=%+.4g/s", a.name, a.stats.min, a.stats.avg, a.stats.max, a.stats.iqrScore, a.stats.zScoreMax, a.stats.changeScore, a.stats.trend),
			changeTime: a.stats.changePoint,
			score:      cappedScore,
		})
	}
	return results
}

// --- ClickHouse-powered deep analysis ---

func analyzeLogPatterns(patterns []RCALogPattern, from, to time.Time) []finding {
	if len(patterns) == 0 {
		return nil
	}

	var results []finding
	totalErrors := uint64(0)
	errorServices := map[string]uint64{}

	for _, p := range patterns {
		if p.Severity >= model.SeverityWarning {
			totalErrors += p.Count
			errorServices[p.ServiceName] += p.Count
		}
	}

	if totalErrors == 0 {
		return nil
	}

	// Group by service to find the noisiest
	type svcErrors struct {
		svc   string
		count uint64
	}
	var svcList []svcErrors
	for svc, cnt := range errorServices {
		svcList = append(svcList, svcErrors{svc, cnt})
	}
	sort.Slice(svcList, func(i, j int) bool { return svcList[i].count > svcList[j].count })

	// Top error patterns
	var topPatterns []string
	for i, p := range patterns {
		if i >= 5 {
			break
		}
		sample := truncate(p.Sample, 150)
		topPatterns = append(topPatterns, fmt.Sprintf("- **[%s]** `%s` ×%d (service: `%s`, %s → %s)",
			p.Severity.String(), truncate(p.Pattern, 80), p.Count, p.ServiceName,
			p.FirstSeen.Format("15:04:05"), p.LastSeen.Format("15:04:05")))
		if sample != truncate(p.Pattern, 150) {
			topPatterns = append(topPatterns, fmt.Sprintf("  Sample: `%s`", sample))
		}
	}

	sev := 1
	if totalErrors > 100 {
		sev = 2
	}

	score := math.Min(float64(sev)*15+math.Log10(float64(totalErrors+1))*15+float64(len(errorServices))*5, 85)

	svcSummary := ""
	for i, s := range svcList {
		if i >= 3 {
			break
		}
		if i > 0 {
			svcSummary += ", "
		}
		svcSummary += fmt.Sprintf("`%s` (%d)", s.svc, s.count)
	}

	results = append(results, finding{
		severity: sev,
		category: "Logs",
		title:    fmt.Sprintf("%d error/warning log entries across %d services", totalErrors, len(errorServices)),
		detail: fmt.Sprintf("ClickHouse log analysis found **%d** error/warning log entries from **%d** services during the anomaly window.\n\n"+
			"**Top error services:** %s\n\n**Top error patterns:**\n\n%s",
			totalErrors, len(errorServices), svcSummary, strings.Join(topPatterns, "\n")),
		evidence: fmt.Sprintf("otel_logs: errors=%d, services=%d, patterns=%d", totalErrors, len(errorServices), len(patterns)),
		score:    score,
	})

	return results
}

func analyzeLogTimeline(buckets []RCALogSeverityBucket, from, to time.Time) []finding {
	if len(buckets) == 0 {
		return nil
	}

	// Build per-minute error counts to detect spikes
	type minuteBucket struct {
		minute time.Time
		count  uint64
	}
	errorsByMinute := map[int64]uint64{}
	for _, b := range buckets {
		if b.Severity >= model.SeverityWarning {
			key := b.Timestamp.Unix() / 60 * 60
			errorsByMinute[key] += b.Count
		}
	}

	if len(errorsByMinute) < 3 {
		return nil
	}

	// Compute stats on per-minute error counts
	var counts []float64
	var times []int64
	for t, c := range errorsByMinute {
		counts = append(counts, float64(c))
		times = append(times, t)
	}

	avg := 0.0
	for _, c := range counts {
		avg += c
	}
	avg /= float64(len(counts))

	stddev := 0.0
	for _, c := range counts {
		d := c - avg
		stddev += d * d
	}
	stddev = math.Sqrt(stddev / float64(len(counts)))

	// Find spike minutes (z > 3) — raised to reduce false positives
	var spikes []string
	spikeCount := 0
	if stddev > 0 {
		type spike struct {
			t     int64
			count float64
			z     float64
		}
		var spikeList []spike
		for i, c := range counts {
			z := (c - avg) / stddev
			if z > 3 {
				spikeList = append(spikeList, spike{t: times[i], count: c, z: z})
			}
		}
		sort.Slice(spikeList, func(i, j int) bool { return spikeList[i].z > spikeList[j].z })
		for i, s := range spikeList {
			if i >= 3 {
				break
			}
			spikes = append(spikes, fmt.Sprintf("- **%s** — %.0f errors (z-score: %.1f, avg: %.1f)",
				time.Unix(s.t, 0).Format("15:04"), s.count, s.z, avg))
			spikeCount++
		}
	}

	if spikeCount == 0 {
		return nil
	}

	return []finding{{
		severity: 1,
		category: "Logs",
		title:    fmt.Sprintf("%d log error spikes detected", spikeCount),
		detail: fmt.Sprintf("Temporal analysis of error logs reveals **%d** spike(s) significantly above baseline (avg %.1f errors/min, σ=%.1f):\n\n%s",
			spikeCount, avg, stddev, strings.Join(spikes, "\n")),
		evidence: fmt.Sprintf("log_timeline: avg=%.1f/min, stddev=%.1f, spikes=%d", avg, stddev, spikeCount),
		score:    math.Min(float64(spikeCount)*15+20, 60),
	}}
}

func analyzeTraceGroups(groups []RCATraceGroup) []finding {
	if len(groups) == 0 {
		return nil
	}

	var results []finding
	totalRequests := uint64(0)
	totalErrors := uint64(0)
	var slowEndpoints []string
	var errorEndpoints []string

	for _, g := range groups {
		totalRequests += g.TotalCount
		totalErrors += g.ErrorCount
	}

	// Find error-heavy endpoints
	for _, g := range groups {
		if g.ErrorCount == 0 {
			continue
		}
		errorRate := float64(g.ErrorCount) / float64(g.TotalCount) * 100
		if errorRate < 1 && g.ErrorCount < 5 {
			continue
		}
		errorEndpoints = append(errorEndpoints, fmt.Sprintf("- `%s/%s` — **%.1f%% error rate** (%d/%d requests, avg %.1fms, P99 %.1fms)",
			g.ServiceName, g.SpanName, errorRate, g.ErrorCount, g.TotalCount, g.AvgDurationMs, g.P99DurationMs))
		if len(errorEndpoints) >= 5 {
			break
		}
	}

	// Find slow endpoints (P99 > 1s)
	for _, g := range groups {
		if g.P99DurationMs < 1000 || g.TotalCount < 3 {
			continue
		}
		slowEndpoints = append(slowEndpoints, fmt.Sprintf("- `%s/%s` — P99 **%.0fms**, avg %.0fms (%d requests)",
			g.ServiceName, g.SpanName, g.P99DurationMs, g.AvgDurationMs, g.TotalCount))
		if len(slowEndpoints) >= 5 {
			break
		}
	}

	if len(errorEndpoints) > 0 {
		overallErrorRate := float64(totalErrors) / float64(totalRequests+1) * 100
		sev := 1
		if overallErrorRate > 5 || totalErrors > 50 {
			sev = 2
		}
		results = append(results, finding{
			severity: sev,
			category: "Traces",
			title:    fmt.Sprintf("Trace error analysis: %d errors across %d endpoints", totalErrors, len(errorEndpoints)),
			detail: fmt.Sprintf("ClickHouse trace analysis of **%d** total requests found **%d errors** (%.1f%% overall error rate).\n\n"+
				"**Error-heavy endpoints:**\n\n%s",
				totalRequests, totalErrors, overallErrorRate, strings.Join(errorEndpoints, "\n")),
			evidence: fmt.Sprintf("otel_traces: total=%d, errors=%d, error_rate=%.1f%%, endpoints=%d", totalRequests, totalErrors, overallErrorRate, len(errorEndpoints)),
			score:    math.Min(float64(sev)*20+math.Log10(float64(totalErrors+1))*10+float64(len(errorEndpoints))*5, 80),
		})
	}

	if len(slowEndpoints) > 0 {
		results = append(results, finding{
			severity: 1,
			category: "Traces",
			title:    fmt.Sprintf("%d slow endpoints detected (P99 > 1s)", len(slowEndpoints)),
			detail:   fmt.Sprintf("**%d** endpoints have P99 latency exceeding 1 second:\n\n%s", len(slowEndpoints), strings.Join(slowEndpoints, "\n")),
			evidence: fmt.Sprintf("otel_traces: slow_endpoints=%d", len(slowEndpoints)),
			score:    math.Min(float64(len(slowEndpoints))*10+20, 55),
		})
	}

	return results
}

func analyzeErrorSpans(spans []*model.TraceSpan) []finding {
	if len(spans) == 0 {
		return nil
	}

	// Group error spans by service+name+statusMessage to find common failures
	type errorGroup struct {
		service string
		span    string
		message string
		count   int
		sample  *model.TraceSpan
	}
	groups := map[string]*errorGroup{}

	for _, s := range spans {
		key := s.ServiceName + "|" + s.Name + "|" + s.StatusMessage
		g, ok := groups[key]
		if !ok {
			g = &errorGroup{service: s.ServiceName, span: s.Name, message: s.StatusMessage, sample: s}
			groups[key] = g
		}
		g.count++
	}

	type sortedGroup struct {
		key   string
		group *errorGroup
	}
	var sorted []sortedGroup
	for k, g := range groups {
		sorted = append(sorted, sortedGroup{k, g})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].group.count > sorted[j].group.count })

	var lines []string
	for i, sg := range sorted {
		if i >= 5 {
			break
		}
		g := sg.group
		msg := truncate(g.message, 100)
		if msg == "" {
			msg = "(no message)"
		}
		lines = append(lines, fmt.Sprintf("- `%s/%s` — **%s** ×%d (trace: `%s`)",
			g.service, g.span, msg, g.count, truncate(g.sample.TraceId, 16)))
	}

	return []finding{{
		severity: 2,
		category: "Traces",
		title:    fmt.Sprintf("%d error spans across %d failure patterns", len(spans), len(groups)),
		detail: fmt.Sprintf("Sampled **%d** error spans from ClickHouse, classified into **%d** distinct failure patterns:\n\n%s",
			len(spans), len(groups), strings.Join(lines, "\n")),
		evidence: fmt.Sprintf("error_spans: total=%d, patterns=%d", len(spans), len(groups)),
		score:    math.Min(float64(len(spans))*2+float64(len(groups))*5+30, 80),
	}}
}

func analyzeCorrelatedLogs(logs []*model.LogEntry) []finding {
	if len(logs) == 0 {
		return nil
	}

	errorLogs := 0
	var samples []string
	for _, l := range logs {
		if l.Severity >= model.SeverityWarning {
			errorLogs++
		}
		if len(samples) < 5 {
			samples = append(samples, fmt.Sprintf("- [%s] **%s** `%s`: %s",
				l.Timestamp.Format("15:04:05"), l.Severity.String(), l.ServiceName, truncate(l.Body, 120)))
		}
	}

	if errorLogs == 0 && len(logs) < 5 {
		return nil
	}

	sev := 0
	if errorLogs > 0 {
		sev = 1
	}
	if errorLogs > 5 {
		sev = 2
	}

	return []finding{{
		severity: sev,
		category: "Logs",
		title:    fmt.Sprintf("%d logs correlated with error traces", len(logs)),
		detail: fmt.Sprintf("Found **%d** log entries linked to error traces via TraceId (**%d** at warning/error level). These logs provide context for the trace failures:\n\n%s",
			len(logs), errorLogs, strings.Join(samples, "\n")),
		evidence: fmt.Sprintf("correlated_logs: total=%d, errors=%d", len(logs), errorLogs),
		score:    math.Min(float64(sev)*15+float64(errorLogs)*3+15, 60),
	}}
}

// --- Cross-signal correlation ---

func correlateFindings(findings []finding, data RCAData) []finding {
	if len(findings) <= 1 {
		return findings
	}

	// Temporal clustering: findings with change points within 60s of each other
	// are likely causally related → boost the earliest one (probable root cause)
	const correlationWindow = 60 // seconds
	for i := range findings {
		if findings[i].changeTime == 0 {
			continue
		}
		correlated := 0
		for j := range findings {
			if i == j || findings[j].changeTime == 0 {
				continue
			}
			diff := math.Abs(float64(findings[i].changeTime - findings[j].changeTime))
			if diff <= correlationWindow {
				correlated++
			}
		}
		if correlated > 0 {
			findings[i].score += float64(correlated) * 5
			findings[i].detail += fmt.Sprintf("\n\n> **Temporal correlation:** %d other anomalies detected within ±60s of this change point, suggesting a common cause.", correlated)
		}
	}

	// Causal chain: OOM → restart, deployment → errors
	hasOOM := false
	hasDeployment := false
	for _, f := range findings {
		if f.category == "Memory" && strings.Contains(f.title, "OOM") {
			hasOOM = true
		}
		if f.category == "Deployment" {
			hasDeployment = true
		}
	}
	for i := range findings {
		if hasOOM && findings[i].category == "Stability" && strings.Contains(findings[i].title, "restart") {
			findings[i].detail += "\n\n> **Causal link:** OOM kills were detected — restarts are likely caused by memory exhaustion."
			// Don't boost restart score — OOM is the root cause
		}
		if hasDeployment && (findings[i].category == "HTTP" || findings[i].category == "Traces") {
			findings[i].detail += "\n\n> **Causal link:** A deployment was detected in the anomaly window — this may have introduced the errors."
		}
	}

	// Graph–symptom: unhealthy dependencies often explain HTTP/Latency on this service
	hasDepIssue := false
	for _, f := range findings {
		if f.category == "Dependency" {
			hasDepIssue = true
			break
		}
	}
	if hasDepIssue {
		for i := range findings {
			if findings[i].category == "HTTP" || findings[i].category == "Latency" {
				findings[i].detail += "\n\n> **Graph correlation:** The dependency map shows an unhealthy related service — consider whether errors or latency are **cascading** from upstream/downstream rather than local misconfiguration."
				findings[i].score += 8
				break
			}
		}
	}

	// Cap scores to 100
	for i := range findings {
		if findings[i].score > 100 {
			findings[i].score = 100
		}
	}
	return findings
}

// --- Report builders ---

func synthesizeSummary(findings []finding) string {
	if len(findings) == 0 {
		return "No anomalies detected"
	}
	top := findings[0]
	summary := top.title
	if len(summary) > 85 {
		summary = summary[:82] + "..."
	}
	others := 0
	for _, f := range findings[1:] {
		if f.severity >= 1 {
			others++
		}
	}
	if others > 0 {
		summary += fmt.Sprintf(" (+%d related issues)", others)
	}
	return summary
}

func synthesizeRootCause(findings []finding) string {
	var sb strings.Builder

	// Top root cause
	if len(findings) > 0 {
		top := findings[0]
		sb.WriteString(fmt.Sprintf("### Most Likely Root Cause (score: %.0f/100)\n\n", top.score))
		sb.WriteString(fmt.Sprintf("**[%s]** %s\n\n", top.category, top.title))
		sb.WriteString(top.detail + "\n\n")
	}

	// Contributing factors
	contributing := 0
	for _, f := range findings[1:] {
		if f.severity >= 1 && contributing < 5 {
			if contributing == 0 {
				sb.WriteString("### Contributing Factors\n\n")
			}
			sb.WriteString(fmt.Sprintf("- **[%s]** %s (score: %.0f)\n", f.category, f.title, f.score))
			contributing++
		}
	}
	return sb.String()
}

func synthesizeFixes(findings []finding) string {
	if len(findings) == 0 {
		return "No action required."
	}

	var fixes []string
	seen := map[string]bool{}

	for _, f := range findings {
		if f.severity < 1 {
			continue
		}
		var fix string
		switch {
		case f.category == "Memory" && strings.Contains(f.title, "OOM"):
			fix = "1. **Increase memory limits** for the affected container (`resources.limits.memory`)\n2. Profile the application's heap usage to identify excessive allocations\n3. If using JVM/Go, tune GC settings (`-Xmx`, `GOMEMLIMIT`)"
		case f.category == "Memory" && strings.Contains(f.title, "leak"):
			fix = "1. Capture a heap dump and analyze with a profiler\n2. Check for unclosed connections, caches without eviction, or unbounded buffers\n3. Consider a rolling restart as a short-term mitigation"
		case f.category == "Memory":
			fix = "1. Increase `resources.limits.memory` or reduce application memory footprint\n2. Enable memory-based HPA for auto-scaling"
		case f.category == "CPU" && strings.Contains(f.title, "throttl"):
			fix = "1. Increase CPU limits (`resources.limits.cpu`) or remove them entirely for burstable QoS\n2. Profile the application for CPU-hot-paths\n3. Consider horizontal scaling if CPU demand is legitimate"
		case f.category == "CPU":
			fix = "1. Scale CPU resources (`resources.requests.cpu` / `limits.cpu`)\n2. Add replicas via HPA for horizontal scaling"
		case f.category == "Stability":
			fix = "1. Check container logs (`kubectl logs`) for the crash reason\n2. Review resource limits — OOM kills often cause CrashLoopBackOff\n3. Verify liveness/readiness probe timeouts and thresholds"
		case f.category == "Network":
			fix = "1. Check upstream service health and connectivity\n2. Review NetworkPolicies and DNS resolution\n3. Look for port exhaustion or connection pool saturation"
		case f.category == "HTTP":
			fix = "1. Check application error logs for stack traces\n2. If correlated with a deployment, consider rollback (`kubectl rollout undo`)\n3. Verify database/cache connectivity from the application"
		case f.category == "Deployment":
			fix = "1. **Consider rolling back:** `kubectl rollout undo deployment/<name>`\n2. Compare the new image/config with the previous version\n3. Check deployment events: `kubectl describe deployment/<name>`"
		case f.category == "Kubernetes":
			fix = "1. Investigate events: `kubectl get events --sort-by='.lastTimestamp'`\n2. Check node resource availability and pod scheduling\n3. Review PVC/PV bindings if storage-related events are present"
		case f.category == "Traces":
			fix = "1. Examine the bottleneck span's service for errors or slow dependencies\n2. Check database query performance (slow queries, lock contention)\n3. Review downstream service SLIs"
		case f.category == "Latency":
			fix = "1. Investigate the endpoint experiencing latency degradation\n2. Check for resource contention (CPU throttling, memory pressure)\n3. Review connection pool sizing and timeout configurations"
		case f.category == "Dependency":
			fix = "1. Open the **Service map** and inspect the related service's health and resources\n2. Verify connectivity and policies between this service and the dependency\n3. Review traces for calls to that dependency during the incident window"
		default:
			fix = "1. Monitor the affected metrics for further changes\n2. Correlate with application logs for context"
		}
		if !seen[fix] {
			fixes = append(fixes, fix)
			seen[fix] = true
		}
		if len(fixes) >= 4 {
			break
		}
	}
	return strings.Join(fixes, "\n\n")
}

func buildDetailedReport(data RCAData, findings []finding) string {
	var sb strings.Builder

	sb.WriteString("## Analysis Overview\n\n")
	sb.WriteString(fmt.Sprintf("| Parameter | Value |\n|---|---|\n"))
	sb.WriteString(fmt.Sprintf("| Application | `%s` |\n", data.ApplicationID))
	sb.WriteString(fmt.Sprintf("| Time Window | %s → %s |\n", data.From.Format("2006-01-02 15:04:05"), data.To.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("| Duration | %s |\n", data.To.Sub(data.From).Round(time.Second)))
	sb.WriteString(fmt.Sprintf("| Metric Groups | %d |\n", len(data.Metrics)))
	sb.WriteString(fmt.Sprintf("| K8s Events | %d |\n", len(data.Events)))

	hasErrorTrace := data.ErrorTrace != nil && len(data.ErrorTrace.Spans) > 0
	hasSlowTrace := data.SlowTrace != nil && len(data.SlowTrace.Spans) > 0
	if hasErrorTrace {
		sb.WriteString(fmt.Sprintf("| Error Trace | %d spans |\n", len(data.ErrorTrace.Spans)))
	}
	if hasSlowTrace {
		sb.WriteString(fmt.Sprintf("| Slow Trace | %d spans |\n", len(data.SlowTrace.Spans)))
	}
	if len(data.LogPatterns) > 0 {
		sb.WriteString(fmt.Sprintf("| Log Patterns (CH) | %d |\n", len(data.LogPatterns)))
	}
	if len(data.TraceGroups) > 0 {
		sb.WriteString(fmt.Sprintf("| Trace Groups (CH) | %d |\n", len(data.TraceGroups)))
	}
	if len(data.ErrorSpans) > 0 {
		sb.WriteString(fmt.Sprintf("| Error Spans (CH) | %d |\n", len(data.ErrorSpans)))
	}
	if len(data.CorrelatedLogs) > 0 {
		sb.WriteString(fmt.Sprintf("| Trace-Correlated Logs (CH) | %d |\n", len(data.CorrelatedLogs)))
	}
	sb.WriteString("\n")

	if findings == nil || len(findings) == 0 {
		sb.WriteString("### Conclusion\n\nNo statistically significant anomalies were detected. The application metrics are within normal variance.\n")
		return sb.String()
	}

	// Methodology
	sb.WriteString("## Methodology\n\n")
	sb.WriteString("This analysis combines metrics, logs, and traces using multiple techniques:\n\n")
	sb.WriteString("**Metrics Analysis (Prometheus cache + BARO):**\n")
	sb.WriteString("- BOCPD (Bayesian Online Change Point Detection) — principled Bayesian method to detect behavior shifts\n")
	sb.WriteString("- RobustScorer (IQR-based) — outlier-resistant scoring using median and interquartile range\n")
	sb.WriteString("- Z-score analysis — identifies values that deviate significantly from the mean (>3σ)\n")
	sb.WriteString("- Burst ratio (P99/P50) — measures spikiness independent of absolute values\n")
	sb.WriteString("- Trend analysis — linear regression detects sustained growth (e.g., memory leaks)\n\n")
	sb.WriteString("**Log Analysis (ClickHouse):**\n")
	sb.WriteString("- Error/warning log pattern aggregation and frequency analysis\n")
	sb.WriteString("- Temporal spike detection on per-minute error counts\n")
	sb.WriteString("- Trace-correlated log retrieval for root cause context\n\n")
	sb.WriteString("**Trace Analysis (ClickHouse):**\n")
	sb.WriteString("- Endpoint-level error rate and latency distribution (P99/avg)\n")
	sb.WriteString("- Error span classification into failure patterns\n")
	sb.WriteString("- Bottleneck span identification across service boundaries\n\n")
	sb.WriteString("**Cross-Signal Correlation:**\n")
	sb.WriteString("- Temporal clustering — groups anomalies that occur within ±60s to identify common causes\n")
	sb.WriteString("- Causal chain inference — links known patterns (e.g., OOM → restart → errors)\n\n")

	// Findings by score
	critical := filterBySeverity(findings, 2)
	warnings := filterBySeverity(findings, 1)
	info := filterBySeverity(findings, 0)
	sb.WriteString(fmt.Sprintf("## Findings Summary: %d critical, %d warning, %d info\n\n", len(critical), len(warnings), len(info)))

	for i, f := range findings {
		if i >= 12 {
			sb.WriteString(fmt.Sprintf("\n*... and %d more findings omitted*\n", len(findings)-12))
			break
		}
		icon := "ℹ️"
		if f.severity == 1 {
			icon = "⚠️"
		}
		if f.severity == 2 {
			icon = "🔴"
		}
		sb.WriteString(fmt.Sprintf("### %s %s (score: %.0f/100)\n\n", icon, f.title, f.score))
		sb.WriteString(f.detail + "\n\n")
		sb.WriteString(fmt.Sprintf("*Evidence:* `%s`\n\n", f.evidence))
	}

	// Timeline
	var timedFindings []finding
	for _, f := range findings {
		if f.changeTime > 0 {
			timedFindings = append(timedFindings, f)
		}
	}
	if len(timedFindings) > 0 {
		sort.Slice(timedFindings, func(i, j int) bool {
			return timedFindings[i].changeTime < timedFindings[j].changeTime
		})
		sb.WriteString("## Timeline of Detected Changes\n\n")
		for _, f := range timedFindings {
			t := time.Unix(f.changeTime, 0).Format("15:04:05")
			sb.WriteString(fmt.Sprintf("- **%s** — %s [%s]\n", t, f.title, f.category))
		}
		sb.WriteString("\n")
	}

	// Deployments
	if len(data.Deployments) > 0 {
		sb.WriteString("## Deployments\n\n")
		for appID, deploys := range data.Deployments {
			for _, d := range deploys {
				startTime := time.Unix(int64(d.StartedAt), 0)
				marker := ""
				if startTime.After(data.From) && startTime.Before(data.To) {
					marker = " **⚠ WITHIN ANOMALY WINDOW**"
				}
				sb.WriteString(fmt.Sprintf("- `%s` → `%s` at %s%s\n", appID.String(), d.Name, startTime.Format("15:04:05"), marker))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// --- Utility ---

func buildLimitMap(limitValues []*model.MetricValues) map[string]float64 {
	m := make(map[string]float64)
	for _, mv := range limitValues {
		if mv.Values != nil {
			s := enrichedStatsFrom(mv.Values)
			if s.last > 0 {
				m[labelVal(mv.Labels, "container_id")] = s.last
			}
		}
	}
	return m
}

func filterBySeverity(findings []finding, severity int) []finding {
	var r []finding
	for _, f := range findings {
		if f.severity == severity {
			r = append(r, f)
		}
	}
	return r
}

func labelVal(labels model.Labels, key string) string {
	if v, ok := labels[key]; ok && v != "" {
		return v
	}
	return "unknown"
}

func fmtBytes(b float64) string {
	abs := math.Abs(b)
	switch {
	case abs >= 1<<30:
		return fmt.Sprintf("%.1f GiB", b/(1<<30))
	case abs >= 1<<20:
		return fmt.Sprintf("%.1f MiB", b/(1<<20))
	case abs >= 1<<10:
		return fmt.Sprintf("%.1f KiB", b/(1<<10))
	default:
		return fmt.Sprintf("%.0f B", b)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func extractServices(title string) []string {
	var services []string
	parts := strings.Split(title, "`")
	for i := 1; i < len(parts); i += 2 {
		svc := strings.TrimSpace(parts[i])
		if svc != "" {
			services = append(services, svc)
		}
	}
	return services
}
