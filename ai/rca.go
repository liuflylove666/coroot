package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coroot/coroot/model"
	"k8s.io/klog"
)

// rcaSystemPrompt is the LLM system message for the two-stage approach:
// Stage 1 (non-LLM): Coroot's builtin analysis has already identified findings, scored them, and collected evidence.
// Stage 2 (LLM): The model receives ONLY the structured findings — not raw metrics — and produces a human-readable summary.
// This mirrors the official product flow: "We avoid relying on LLMs for the actual root cause analysis.
// Instead, we use them for what they do best: explaining complex issues and summarizing results."
const rcaSystemPrompt = `You are Coroot's AI explanation engine — an expert SRE that takes pre-analyzed Root Cause Analysis findings and turns them into clear, actionable summaries.

Coroot has ALREADY performed the root cause analysis using ML algorithms and dependency-graph traversal. The findings below are the output of that analysis. Your job is to:
1. Summarize the findings into a concise narrative that engineers can act on immediately.
2. Explain the issue propagation path if multiple services or dependencies are involved.
3. Suggest specific, actionable remediation steps based on the findings.

You are NOT performing the analysis — it is already done. Focus on EXPLAINING the results clearly.

Your response MUST be a valid JSON object with these fields:
{
  "short_summary": "Brief one-line summary (max 100 chars). Example: 'High latency caused by OOM kills in ad service'",
  "root_cause": "Concise markdown explanation of the root cause. Focus on the 'why'. Include the specific component at fault, the mechanism, and supporting evidence from the findings.",
  "immediate_fixes": "Markdown-formatted remediation steps. Be specific:\n- Configuration changes with exact parameters\n- Rollback instructions if deployment-related\n- Scaling recommendations if resource-related",
  "detailed_root_cause_analysis": "Detailed markdown analysis:\n## Anomaly Summary\n## Issue Propagation\n## Evidence\n## Root Cause\n## Remediation"
}

Guidelines:
- Base your explanation ONLY on the findings provided — do not speculate beyond them
- Identify root causes, not just symptoms
- If a deployment is mentioned, highlight it prominently
- If OOM kills, GC pauses, or resource exhaustion appear, explain the cascade effect
- Use markdown formatting for clarity
- If findings are insufficient, state what additional investigation would help
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

	// DependencyPeers is filled from model.World when available (API / IncidentRCA).
	DependencyPeers []model.RCADependencyPeer

	LogPatterns    []RCALogPattern
	LogTimeline    []RCALogSeverityBucket
	TraceGroups    []RCATraceGroup
	ErrorSpans     []*model.TraceSpan
	CorrelatedLogs []*model.LogEntry
}

// RunRCA implements the two-stage approach:
//
//	Stage 1 – Run builtin detectors (ML/statistical + dependency graph) to produce structured findings.
//	Stage 2 – Send ONLY the findings to the LLM for human-readable explanation and remediation.
//
// If the LLM call fails, the builtin results are returned as a graceful fallback.
func RunRCA(ctx context.Context, client Client, data RCAData) (*model.RCA, error) {
	builtinRCA := RunBuiltinRCA(data)

	prompt := buildFindingsPrompt(builtinRCA, data)
	klog.Infof("RCA two-stage: sending findings prompt to LLM (%d bytes, %d findings)",
		len(prompt), len(builtinRCA.CausalFindings))

	response, err := client.Chat(ctx, rcaSystemPrompt, prompt)
	if err != nil {
		klog.Warningf("RCA: LLM call failed, falling back to builtin results: %v", err)
		return builtinRCA, nil
	}
	klog.Infof("RCA two-stage: received LLM response (%d bytes)", len(response))

	llmRCA, err := parseRCAResponse(response)
	if err != nil {
		klog.Warningf("RCA: failed to parse LLM response, falling back to builtin: %v", err)
		return builtinRCA, nil
	}

	return mergeTwoStageResults(builtinRCA, llmRCA), nil
}

// mergeTwoStageResults combines the LLM's human-readable summaries with the
// builtin engine's structured data (causal findings, ranked causes, logs, traces).
func mergeTwoStageResults(builtin, llm *model.RCA) *model.RCA {
	merged := &model.RCA{
		Status: "OK",

		ShortSummary:      preferNonEmpty(llm.ShortSummary, builtin.ShortSummary),
		RootCause:         preferNonEmpty(llm.RootCause, builtin.RootCause),
		ImmediateFixes:    preferNonEmpty(llm.ImmediateFixes, builtin.ImmediateFixes),
		DetailedRootCause: preferNonEmpty(llm.DetailedRootCause, builtin.DetailedRootCause),

		PropagationMap:  builtin.PropagationMap,
		CausalFindings:  builtin.CausalFindings,
		RankedCauses:    builtin.RankedCauses,
		RelatedLogs:     builtin.RelatedLogs,
		RelatedTraces:   builtin.RelatedTraces,
		DependencyPeers: builtin.DependencyPeers,
	}
	return merged
}

func preferNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// buildFindingsPrompt creates a prompt for Stage 2 (LLM) containing ONLY:
//   - Application context (ID, time window)
//   - Dependency graph edges
//   - Structured causal findings from Stage 1 (builtin analysis)
//   - Related logs and traces summaries
//
// No raw metrics, no raw Kubernetes events, no raw trace spans.
func buildFindingsPrompt(rca *model.RCA, data RCAData) string {
	var sb strings.Builder
	duration := data.To.Sub(data.From)

	sb.WriteString("# Root Cause Analysis — Pre-Analyzed Findings\n\n")
	sb.WriteString(fmt.Sprintf("**Application:** `%s`\n", data.ApplicationID))
	sb.WriteString(fmt.Sprintf("**Anomaly window:** %s to %s (duration: %s)\n\n",
		data.From.Format(time.RFC3339), data.To.Format(time.RFC3339), duration.String()))

	sb.WriteString("The findings below were produced by Coroot's ML/statistical analysis engine ")
	sb.WriteString("(BARO change-point detection, IQR scoring, dependency-graph traversal, and cross-signal correlation). ")
	sb.WriteString("Your task is to explain them clearly and suggest remediation.\n\n")

	if len(rca.DependencyPeers) > 0 {
		sb.WriteString("## Dependency Graph (service map edges)\n\n")
		for _, p := range rca.DependencyPeers {
			line := fmt.Sprintf("- **%s** `%s` — app: %s", p.Direction, p.Name, p.AppStatus)
			if p.ConnectionStatus != "" {
				line += fmt.Sprintf(", connection: %s", p.ConnectionStatus)
			}
			if p.ConnectionHint != "" {
				line += fmt.Sprintf(" (%s)", p.ConnectionHint)
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if len(rca.CausalFindings) > 0 {
		sb.WriteString("## Causal Findings (ranked by confidence)\n\n")
		for i, f := range rca.CausalFindings {
			sevLabel := "info"
			if f.Severity == 1 {
				sevLabel = "warning"
			} else if f.Severity >= 2 {
				sevLabel = "critical"
			}
			sb.WriteString(fmt.Sprintf("### Finding %d — [%s] %s (confidence: %.0f%%, severity: %s)\n\n",
				i+1, f.Category, f.Title, f.Confidence, sevLabel))
			if f.Detail != "" {
				sb.WriteString(f.Detail + "\n\n")
			}
			if f.Evidence != "" {
				sb.WriteString(fmt.Sprintf("Evidence: `%s`\n\n", f.Evidence))
			}
			if len(f.Fixes) > 0 {
				sb.WriteString("Suggested fixes:\n")
				for _, fix := range f.Fixes {
					sb.WriteString(fmt.Sprintf("- %s\n", fix))
				}
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("## Findings\n\nNo significant anomalies were detected by the analysis engine.\n\n")
	}

	if len(rca.RankedCauses) > 0 {
		sb.WriteString("## Ranked Metric Anomalies\n\n")
		for _, rc := range rca.RankedCauses {
			sb.WriteString(fmt.Sprintf("- `%s`", rc.Metric))
			if rc.Service != "" {
				sb.WriteString(fmt.Sprintf(" (service: `%s`)", rc.Service))
			}
			sb.WriteString(fmt.Sprintf(" — confidence: %.0f%%, %s\n", rc.Confidence, rc.Detail))
		}
		sb.WriteString("\n")
	}

	if len(rca.RelatedLogs) > 0 {
		sb.WriteString("## Related Log Entries\n\n")
		limit := 15
		if len(rca.RelatedLogs) < limit {
			limit = len(rca.RelatedLogs)
		}
		for i := 0; i < limit; i++ {
			l := rca.RelatedLogs[i]
			sb.WriteString(fmt.Sprintf("- [%s] **%s** `%s`: %s\n", l.Timestamp, l.Severity, l.Service, l.Message))
		}
		if len(rca.RelatedLogs) > limit {
			sb.WriteString(fmt.Sprintf("... and %d more entries\n", len(rca.RelatedLogs)-limit))
		}
		sb.WriteString("\n")
	}

	if len(rca.RelatedTraces) > 0 {
		sb.WriteString("## Related Traces\n\n")
		limit := 10
		if len(rca.RelatedTraces) < limit {
			limit = len(rca.RelatedTraces)
		}
		for i := 0; i < limit; i++ {
			t := rca.RelatedTraces[i]
			sb.WriteString(fmt.Sprintf("- trace `%s` | service: `%s` | duration: %s | status: %s",
				t.TraceID, t.Service, t.Duration, t.Status))
			if t.Time != "" {
				sb.WriteString(fmt.Sprintf(" | time: %s", t.Time))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Based on the findings above, provide your explanation and remediation as a JSON object.\n")
	return sb.String()
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
