package model

import (
	"fmt"

	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

type Impact struct {
	AffectedRequestPercentage float32 `json:"percentage"`
}

type IncidentDetails struct {
	AvailabilityBurnRates []BurnRate `json:"availability_burn_rates"`
	LatencyBurnRates      []BurnRate `json:"latency_burn_rates"`
	AvailabilityImpact    Impact     `json:"availability_impact"`
	LatencyImpact         Impact     `json:"latency_impact"`
}

// RCADependencyPeer is one upstream/downstream edge from the service map used for graph-aware RCA.
type RCADependencyPeer struct {
	ApplicationID    string `json:"application_id"`
	Name             string `json:"name"`
	Direction        string `json:"direction"` // "upstream" | "downstream"
	Hop              int    `json:"hop,omitempty"`
	Path             string `json:"path,omitempty"`
	AppStatus        string `json:"app_status,omitempty"`
	ConnectionStatus string `json:"connection_status,omitempty"`
	ConnectionHint   string `json:"connection_hint,omitempty"`
}

type RCA struct {
	Status            string          `json:"status"`
	Error             string          `json:"error"`
	ShortSummary      string          `json:"short_summary"`
	RootCause         string          `json:"root_cause"`
	ImmediateFixes    string          `json:"immediate_fixes"`
	DetailedRootCause string          `json:"detailed_root_cause_analysis"`
	PropagationMap    *PropagationMap `json:"propagation_map"`
	Widgets           []*Widget       `json:"widgets"`

	CausalFindings  []*CausalFinding    `json:"causal_findings,omitempty"`
	RankedCauses    []*RankedCause      `json:"ranked_causes,omitempty"`
	RelatedLogs     []*RCALogEntry      `json:"related_logs,omitempty"`
	RelatedTraces   []*RCATraceEntry    `json:"related_traces,omitempty"`
	DependencyPeers []RCADependencyPeer `json:"dependency_peers,omitempty"`
	Events          []*RCAEvent         `json:"events,omitempty"`
	Problem         *RCAProblem         `json:"problem,omitempty"`
}

type CausalFinding struct {
	Title      string   `json:"title"`
	Category   string   `json:"category"`
	Confidence float64  `json:"confidence"`
	Severity   int      `json:"severity"`
	Detail     string   `json:"detail"`
	Evidence   string   `json:"evidence"`
	Services   []string `json:"services,omitempty"`
	Fixes      []string `json:"fixes,omitempty"`
}

type RankedCause struct {
	Metric     string  `json:"metric"`
	Service    string  `json:"service"`
	Confidence float64 `json:"confidence"`
	Detail     string  `json:"detail"`
}

type RCAEvent struct {
	Fingerprint     string   `json:"fingerprint"`
	Source          string   `json:"source"`
	Entity          string   `json:"entity"`
	Category        string   `json:"category"`
	Title           string   `json:"title"`
	Severity        int      `json:"severity"`
	Confidence      float64  `json:"confidence"`
	RootCauseScore  float64  `json:"root_cause_score,omitempty"`
	ScoreFactors    []string `json:"score_factors,omitempty"`
	FirstSeen       int64    `json:"first_seen,omitempty"`
	LastSeen        int64    `json:"last_seen,omitempty"`
	Count           int      `json:"count"`
	Role            string   `json:"role,omitempty"`
	CausalParent    string   `json:"causal_parent,omitempty"`
	PropagationPath string   `json:"propagation_path,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
}

type RCAProblem struct {
	Title            string      `json:"title"`
	Status           string      `json:"status,omitempty"`
	OpenedAt         int64       `json:"opened_at,omitempty"`
	UpdatedAt        int64       `json:"updated_at,omitempty"`
	Revision         int         `json:"revision,omitempty"`
	UpdateCount      int         `json:"update_count,omitempty"`
	RootCause        *RCAEvent   `json:"root_cause,omitempty"`
	Contributors     []*RCAEvent `json:"contributors,omitempty"`
	ImpactedEntities []string    `json:"impacted_entities,omitempty"`
	Impact           *RCAImpact  `json:"impact,omitempty"`
	Evidence         []string    `json:"evidence,omitempty"`
	Timeline         []*RCAEvent `json:"timeline,omitempty"`
}

type RCAImpact struct {
	AffectedEntities int      `json:"affected_entities"`
	EntryPoints      []string `json:"entry_points,omitempty"`
	PropagationPaths []string `json:"propagation_paths,omitempty"`
	Severity         string   `json:"severity"`
	Summary          string   `json:"summary"`
}

type RCALogEntry struct {
	Timestamp string `json:"timestamp"`
	Severity  string `json:"severity"`
	Service   string `json:"service"`
	Message   string `json:"message"`
}

type RCATraceEntry struct {
	TraceID  string `json:"trace_id"`
	Service  string `json:"service"`
	Duration string `json:"duration"`
	Time     string `json:"time"`
	Status   string `json:"status"`
}

type PropagationMap struct {
	Applications []*PropagationMapApplication `json:"applications"`
}

type PropagationMapApplication struct {
	Id     ApplicationId `json:"id"`
	Icon   string        `json:"icon"`
	Labels Labels        `json:"labels"`
	Status Status        `json:"status"`
	Issues []string      `json:"issues,omitempty"`

	Upstreams   []*PropagationMapApplicationLink `json:"upstreams"`
	Downstreams []*PropagationMapApplicationLink `json:"downstreams"`
}

func (app *PropagationMapApplication) Issue(format string, a ...any) {
	issue := fmt.Sprintf(format, a...)
	for _, i := range app.Issues {
		if i == issue {
			return
		}
	}
	app.Issues = append(app.Issues, issue)
}

type PropagationMapApplicationLink struct {
	Id     ApplicationId    `json:"id"`
	Status Status           `json:"status"`
	Stats  *utils.StringSet `json:"stats"`
}

func (l *PropagationMapApplicationLink) AddIssues(issues ...string) {
	l.Status = CRITICAL
	l.Stats.Add(issues...)
}

type ApplicationIncident struct {
	ApplicationId ApplicationId   `json:"application_id"`
	Key           string          `json:"key"`
	OpenedAt      timeseries.Time `json:"opened_at"`
	ResolvedAt    timeseries.Time `json:"resolved_at"`
	Severity      Status          `json:"severity"`
	Details       IncidentDetails `json:"details"`
	RCA           *RCA            `json:"rca"`
}

func (i *ApplicationIncident) Resolved() bool {
	return !i.ResolvedAt.IsZero()
}

func (i *ApplicationIncident) ShortDescription() string {
	var (
		a, l bool
	)

	if i.RCA != nil && i.RCA.ShortSummary != "" {
		return i.RCA.ShortSummary
	}

	if i.Details.AvailabilityImpact.AffectedRequestPercentage > 0 {
		a = true
	}
	if i.Details.LatencyImpact.AffectedRequestPercentage > 0 {
		l = true
	}
	switch {
	case a && l:
		return "High latency and errors"
	case l:
		return "High latency"
	case a:
		return "Elevated error rate"
	}
	return "SLO violation"
}
