package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

const rcaSystemPrompt = `You are Coroot's AI-powered Root Cause Analysis engine — an expert SRE that summarizes telemetry data (metrics, logs, traces, deployments) into clear, actionable insights.

Coroot has already collected monitoring data and detected an anomaly. Your task is to:
1. Identify the most likely root cause by analyzing metric patterns, events, traces, and deployments.
2. Trace the issue propagation path across services if multiple services are involved.
3. Provide a concise summary and actionable remediation steps.

Your response MUST be a valid JSON object with these fields:
{
  "short_summary": "Brief one-line summary of the issue (max 100 chars). Example: 'High latency caused by OOM kills in ad service'",
  "root_cause": "Concise markdown description of the identified root cause. Focus on the 'why', not the symptoms. Include:\n- The specific component or service at fault\n- The mechanism (e.g., memory pressure, CPU throttling, network issues, bad deployment)\n- Supporting evidence from the data",
  "immediate_fixes": "Markdown-formatted remediation steps. Be specific and actionable:\n- Configuration changes with exact parameters\n- Rollback instructions if deployment-related\n- Scaling recommendations if resource-related",
  "detailed_root_cause_analysis": "Detailed markdown analysis including:\n## Anomaly Summary\nWhat was observed (latency spikes, errors, etc.)\n## Issue Propagation\nHow the issue spread across services\n## Timeline\nKey events correlated with the anomaly\n## Evidence\nSpecific metrics, logs, or traces that support the conclusion\n## Root Cause\nDetailed explanation of why this happened"
}

Guidelines:
- Base analysis on concrete evidence from the provided data
- Identify root causes, not just symptoms
- If a deployment correlates with the anomaly, highlight it prominently
- If OOM kills, GC pauses, or resource exhaustion are detected, explain the cascade effect
- Use markdown formatting with headers, lists, and code blocks for clarity
- If data is insufficient, state what additional data would help
- ONLY output the JSON object, no additional text`

type RCALogPattern struct {
	ServiceName string
	Severity    model.Severity
	Pattern     string
	Count       uint64
	FirstSeen   time.Time
	LastSeen    time.Time
	Sample      string
}

type RCALogSeverityBucket struct {
	Severity  model.Severity
	Timestamp time.Time
	Count     uint64
}

type RCATraceGroup struct {
	ServiceName   string
	SpanName      string
	StatusCode    string
	TotalCount    uint64
	ErrorCount    uint64
	AvgDurationMs float64
	MaxDurationMs float64
	P99DurationMs float64
	SampleTraceId string
}

type RCAData struct {
	ApplicationID string
	From          time.Time
	To            time.Time
	Metrics       map[string][]*model.MetricValues
	Events        []*model.LogEntry
	ErrorTrace    *model.Trace
	SlowTrace     *model.Trace
	Deployments   map[model.ApplicationId][]*model.ApplicationDeployment

	LogPatterns     []RCALogPattern
	LogTimeline     []RCALogSeverityBucket
	TraceGroups     []RCATraceGroup
	ErrorSpans      []*model.TraceSpan
	CorrelatedLogs  []*model.LogEntry
}

func RunRCA(ctx context.Context, client Client, data RCAData) (*model.RCA, error) {
	prompt := buildRCAPrompt(data)
	klog.Infof("RCA: sending prompt to LLM (%d bytes)", len(prompt))
	response, err := client.Chat(ctx, rcaSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}
	klog.Infof("RCA: received LLM response (%d bytes)", len(response))
	return parseRCAResponse(response)
}

func buildRCAPrompt(data RCAData) string {
	var sb strings.Builder
	duration := data.To.Sub(data.From)

	sb.WriteString("# Root Cause Analysis Request\n\n")
	sb.WriteString(fmt.Sprintf("**Application under investigation:** `%s`\n", data.ApplicationID))
	sb.WriteString(fmt.Sprintf("**Anomaly window:** %s to %s (duration: %s)\n\n",
		data.From.Format(time.RFC3339), data.To.Format(time.RFC3339), duration.String()))

	sb.WriteString("## Metrics Summary\n\n")
	if len(data.Metrics) == 0 {
		sb.WriteString("No metrics data available.\n\n")
	} else {
		anomalousMetrics := 0
		normalMetrics := 0
		for name, values := range data.Metrics {
			if anomalousMetrics >= 30 {
				break
			}
			hasAnomaly := false
			for _, mv := range values {
				if mv.Values != nil {
					stats := computeStats(mv.Values)
					if stats.max > stats.avg*2 && stats.avg > 0 {
						hasAnomaly = true
						break
					}
				}
			}

			if hasAnomaly {
				sb.WriteString(fmt.Sprintf("### ⚠ %s (anomalous)\n", name))
				anomalousMetrics++
			} else {
				normalMetrics++
				if normalMetrics > 20 {
					continue
				}
				sb.WriteString(fmt.Sprintf("### %s\n", name))
			}
			for i, mv := range values {
				if i >= 5 {
					sb.WriteString(fmt.Sprintf("  ... and %d more series\n", len(values)-5))
					break
				}
				sb.WriteString(fmt.Sprintf("- Labels: %s\n", formatLabels(mv.Labels)))
				if mv.Values != nil {
					stats := computeStats(mv.Values)
					sb.WriteString(fmt.Sprintf("  Min=%.4f, Max=%.4f, Avg=%.4f, Last=%.4f\n",
						stats.min, stats.max, stats.avg, stats.last))
				}
			}
			sb.WriteString("\n")
		}
		if normalMetrics > 20 {
			sb.WriteString(fmt.Sprintf("\n... and %d more normal metric groups omitted\n\n", normalMetrics-20))
		}
	}

	sb.WriteString("## Kubernetes Events\n\n")
	if len(data.Events) == 0 {
		sb.WriteString("No Kubernetes events available.\n\n")
	} else {
		warningCount := 0
		for _, e := range data.Events {
			if e.Severity > 0 {
				warningCount++
			}
		}
		if warningCount > 0 {
			sb.WriteString(fmt.Sprintf("**%d warning/error events detected out of %d total events**\n\n", warningCount, len(data.Events)))
		}
		limit := 50
		if len(data.Events) < limit {
			limit = len(data.Events)
		}
		for i := 0; i < limit; i++ {
			e := data.Events[i]
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n",
				e.Timestamp.Format(time.RFC3339),
				e.Severity.String(),
				e.Body))
		}
		if len(data.Events) > limit {
			sb.WriteString(fmt.Sprintf("... and %d more events\n", len(data.Events)-limit))
		}
		sb.WriteString("\n")
	}

	if data.ErrorTrace != nil && len(data.ErrorTrace.Spans) > 0 {
		sb.WriteString("## Error Trace (requests that failed)\n\n")
		writeTrace(&sb, data.ErrorTrace)
	}

	if data.SlowTrace != nil && len(data.SlowTrace.Spans) > 0 {
		sb.WriteString("## Slow Trace (requests exceeding SLO latency threshold)\n\n")
		writeTrace(&sb, data.SlowTrace)
	}

	sb.WriteString("## Recent Deployments\n\n")
	if len(data.Deployments) == 0 {
		sb.WriteString("No recent deployments detected.\n\n")
	} else {
		for appID, deploys := range data.Deployments {
			for _, d := range deploys {
				startTime := time.Unix(int64(d.StartedAt), 0)
				sb.WriteString(fmt.Sprintf("- App: `%s`, Deployment: `%s`, Started: %s",
					appID.String(), d.Name, startTime.Format(time.RFC3339)))
				if startTime.After(data.From) && startTime.Before(data.To) {
					sb.WriteString(" ⚠ **WITHIN ANOMALY WINDOW**")
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Analyze the data above. Focus on anomalous metrics, error events, and deployments within the anomaly window. Provide the root cause analysis as a JSON object.\n")
	return sb.String()
}

func writeTrace(sb *strings.Builder, trace *model.Trace) {
	limit := 20
	if len(trace.Spans) < limit {
		limit = len(trace.Spans)
	}
	for i := 0; i < limit; i++ {
		span := trace.Spans[i]
		sb.WriteString(fmt.Sprintf("- Service: %s, Name: %s, Duration: %s, Status: %s %s\n",
			span.ServiceName, span.Name, span.Duration,
			span.StatusCode, span.StatusMessage))
	}
	if len(trace.Spans) > limit {
		sb.WriteString(fmt.Sprintf("... and %d more spans\n", len(trace.Spans)-limit))
	}
	sb.WriteString("\n")
}

type metricStats struct {
	min, max, avg, last float64
}

func computeStats(ts *timeseries.TimeSeries) metricStats {
	var stats metricStats
	stats.min = 1e18
	stats.max = -1e18
	count := 0
	sum := 0.0
	iter := ts.Iter()
	for iter.Next() {
		_, v := iter.Value()
		if timeseries.IsNaN(v) {
			continue
		}
		fv := float64(v)
		if fv < stats.min {
			stats.min = fv
		}
		if fv > stats.max {
			stats.max = fv
		}
		sum += fv
		count++
		stats.last = fv
	}
	if count > 0 {
		stats.avg = sum / float64(count)
	}
	if stats.min > 1e17 {
		stats.min = 0
	}
	return stats
}

func formatLabels(labels model.Labels) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func parseRCAResponse(response string) (*model.RCA, error) {
	response = strings.TrimSpace(response)
	jsonStr := extractJSON(response)

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return &model.RCA{
			Status:            "OK",
			RootCause:         response,
			DetailedRootCause: response,
		}, nil
	}

	getString := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := raw[key]; ok {
				switch val := v.(type) {
				case string:
					return val
				case []interface{}:
					parts := make([]string, 0, len(val))
					for _, item := range val {
						switch s := item.(type) {
						case string:
							parts = append(parts, "- "+s)
						case map[string]interface{}:
							b, _ := json.MarshalIndent(s, "", "  ")
							parts = append(parts, string(b))
						}
					}
					return strings.Join(parts, "\n")
				case map[string]interface{}:
					b, _ := json.MarshalIndent(val, "", "  ")
					return string(b)
				}
			}
		}
		return ""
	}

	rca := &model.RCA{
		Status:            "OK",
		ShortSummary:      getString("short_summary", "summary"),
		RootCause:         getString("root_cause", "rootCause", "root_causes"),
		ImmediateFixes:    getString("immediate_fixes", "fixes", "remediation", "recommendations"),
		DetailedRootCause: getString("detailed_root_cause_analysis", "detailed_root_causes_analysis", "detailed_analysis", "analysis"),
	}

	if rca.RootCause == "" && rca.DetailedRootCause == "" {
		rca.RootCause = response
		rca.DetailedRootCause = response
	}
	return rca, nil
}

func extractJSON(s string) string {
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
