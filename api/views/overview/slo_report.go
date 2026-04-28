package overview

import (
	"fmt"
	"math"
	"sort"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

// SLOReport is the windowed SLO retrospective report:
// it answers "how did each service do over the last N days/weeks/months?"
// against the configured SLO targets, including error-budget burn,
// total time spent in violation, and the underlying SLO incidents.
type SLOReport struct {
	Window       SLOWindow           `json:"window"`
	Summary      SLOReportSummary    `json:"summary"`
	Services     []*SLOServiceRow    `json:"services"`
	DailyTrend   []SLOTrendBucket    `json:"daily_trend"`
	TopViolators []*SLOServiceRow    `json:"top_violators"`
	Violations   []*SLOViolationItem `json:"violations"`
}

type SLOWindow struct {
	From timeseries.Time `json:"from"`
	To   timeseries.Time `json:"to"`
	Days float64         `json:"days"`
}

type SLOReportSummary struct {
	TotalServices           int     `json:"total_services"`
	Compliant               int     `json:"compliant"`
	AtRisk                  int     `json:"at_risk"`
	Violating               int     `json:"violating"`
	AvgAvailability         string  `json:"avg_availability"`
	AvgAvailabilityPct      float64 `json:"avg_availability_pct"`
	AvgLatencyPassRate      string  `json:"avg_latency_pass_rate"`
	AvgLatencyPassRatePct   float64 `json:"avg_latency_pass_rate_pct"`
	TotalViolationMinutes   int64   `json:"total_violation_minutes"`
	IncidentCount           int     `json:"incident_count"`
	AvgErrorBudgetRemaining string  `json:"avg_error_budget_remaining"`
}

type SLOServiceRow struct {
	Id                      model.ApplicationId       `json:"id"`
	Category                model.ApplicationCategory `json:"category"`
	HasAvailability         bool                      `json:"has_availability"`
	Availability            string                    `json:"availability"`
	AvailabilityPct         float64                   `json:"availability_pct"`
	AvailabilityTarget      string                    `json:"availability_target"`
	HasLatency              bool                      `json:"has_latency"`
	LatencyPassRate         string                    `json:"latency_pass_rate"`
	LatencyPassRatePct      float64                   `json:"latency_pass_rate_pct"`
	LatencyTarget           string                    `json:"latency_target"`
	LatencyObjectiveBucket  string                    `json:"latency_objective_bucket"`
	ErrorBudgetConsumedPct  float64                   `json:"error_budget_consumed_pct"`
	ErrorBudgetRemainingPct float64                   `json:"error_budget_remaining_pct"`
	ErrorBudgetRemaining    string                    `json:"error_budget_remaining"`
	TimeToExhaustion        string                    `json:"time_to_exhaustion"`
	SLOStatus               model.Status              `json:"slo_status"`
	ViolationMinutes        int64                     `json:"violation_minutes"`
	IncidentCount           int                       `json:"incident_count"`
	TotalRequests           float64                   `json:"total_requests"`
	FailedRequests          float64                   `json:"failed_requests"`
	Incidents               []*SLOViolationItem       `json:"incidents,omitempty"`
}

type SLOTrendBucket struct {
	Ts                timeseries.Time `json:"ts"`
	Availability      float64         `json:"availability"`
	LatencyPassRate   float64         `json:"latency_pass_rate"`
	ServicesCompliant int             `json:"services_compliant"`
	ServicesTotal     int             `json:"services_total"`
}

type SLOViolationItem struct {
	AppId            model.ApplicationId       `json:"app_id"`
	AppName          string                    `json:"app_name"`
	Category         model.ApplicationCategory `json:"category"`
	IncidentKey      string                    `json:"incident_key"`
	OpenedAt         timeseries.Time           `json:"opened_at"`
	ResolvedAt       timeseries.Time           `json:"resolved_at"`
	DurationMinutes  int64                     `json:"duration_minutes"`
	Severity         model.Status              `json:"severity"`
	Type             string                    `json:"type"` // availability | latency | mixed
	ShortDescription string                    `json:"short_description"`
}

const (
	defaultSLOAvailabilityTarget = 99.0
	defaultSLOLatencyTarget      = 99.0
	atRiskBudgetRemainingPct     = 30.0
)

func renderSLOReport(w *model.World) *SLOReport {
	durationSeconds := int64(w.Ctx.To.Sub(w.Ctx.From))
	if durationSeconds <= 0 {
		durationSeconds = 1
	}

	rep := &SLOReport{
		Window:       SLOWindow{From: w.Ctx.From, To: w.Ctx.To, Days: float64(durationSeconds) / 86400.0},
		Services:     []*SLOServiceRow{},
		DailyTrend:   []SLOTrendBucket{},
		TopViolators: []*SLOServiceRow{},
		Violations:   []*SLOViolationItem{},
	}

	var sumAvailability, sumLatencyPass, sumErrorBudgetRemaining float64
	var availCount, latencyCount, ebrCount int
	var totalViolationMin int64
	incidentCount := 0

	for _, app := range w.Applications {
		if !isRelevantApp(app) {
			continue
		}
		if len(app.AvailabilitySLIs) == 0 && len(app.LatencySLIs) == 0 {
			continue
		}

		row := &SLOServiceRow{Id: app.Id, Category: app.Category}
		var availTarget float64 = defaultSLOAvailabilityTarget
		var latencyTarget float64 = defaultSLOLatencyTarget

		if len(app.AvailabilitySLIs) > 0 {
			sli := app.AvailabilitySLIs[0]
			if sli.Config.ObjectivePercentage > 0 {
				availTarget = float64(sli.Config.ObjectivePercentage)
			}
			total := float64(sli.TotalRequests.Reduce(timeseries.NanSum))
			failed := float64(sli.FailedRequests.Reduce(timeseries.NanSum))
			row.HasAvailability = true
			row.AvailabilityTarget = formatAvailability(availTarget)
			row.TotalRequests = total
			row.FailedRequests = failed
			if total > 0 {
				avail := (total - failed) / total * 100
				row.AvailabilityPct = avail
				row.Availability = formatAvailability(avail)
				sumAvailability += avail
				availCount++
			}
			ebr := computeErrorBudgetRemaining(sli.Config.ObjectivePercentage, total, failed)
			row.ErrorBudgetRemainingPct = ebr
			row.ErrorBudgetConsumedPct = math.Max(100-ebr, 0)
			row.ErrorBudgetRemaining = fmt.Sprintf("%.1f%%", ebr)
			row.TimeToExhaustion = computeTimeToExhaustion(row.ErrorBudgetConsumedPct, durationSeconds)
			sumErrorBudgetRemaining += ebr
			ebrCount++
		}

		if len(app.LatencySLIs) > 0 {
			sli := app.LatencySLIs[0]
			if sli.Config.ObjectivePercentage > 0 {
				latencyTarget = float64(sli.Config.ObjectivePercentage)
			}
			row.HasLatency = true
			row.LatencyTarget = formatAvailability(latencyTarget)
			if sli.Config.ObjectiveBucket > 0 {
				row.LatencyObjectiveBucket = utils.FormatLatency(sli.Config.ObjectiveBucket)
			}
			total, fast := sli.GetTotalAndFast(false)
			if total != nil && fast != nil {
				t := float64(total.Reduce(timeseries.NanSum))
				f := float64(fast.Reduce(timeseries.NanSum))
				if t > 0 {
					pass := f / t * 100
					row.LatencyPassRatePct = pass
					row.LatencyPassRate = formatAvailability(pass)
					sumLatencyPass += pass
					latencyCount++
				}
			}
		}

		// Violation classification (union of two definitions, per product decision):
		//   1. incident-based: any ApplicationIncident in window with non-empty BurnRates
		//   2. rate-based:     measured availability/latency pass rate < target,
		//                      or remaining error budget at/under zero
		hasViolationByRate := false
		hasAtRiskByRate := false
		if row.HasAvailability {
			if row.ErrorBudgetRemainingPct <= 0 {
				hasViolationByRate = true
			} else if row.ErrorBudgetRemainingPct < atRiskBudgetRemainingPct {
				hasAtRiskByRate = true
			}
			if row.AvailabilityPct > 0 && row.AvailabilityPct < availTarget {
				hasViolationByRate = true
			}
		}
		if row.HasLatency && row.LatencyPassRatePct > 0 {
			if row.LatencyPassRatePct < latencyTarget {
				hasViolationByRate = true
			} else if row.LatencyPassRatePct < latencyTarget+0.5 {
				hasAtRiskByRate = true
			}
		}

		var appViolMinutes int64
		for _, inc := range app.Incidents {
			if len(inc.Details.AvailabilityBurnRates) == 0 && len(inc.Details.LatencyBurnRates) == 0 {
				continue
			}
			if inc.OpenedAt.Before(w.Ctx.From) && (inc.Resolved() && inc.ResolvedAt.Before(w.Ctx.From)) {
				continue
			}
			incidentCount++
			row.IncidentCount++
			start := inc.OpenedAt
			if start.Before(w.Ctx.From) {
				start = w.Ctx.From
			}
			end := inc.ResolvedAt
			if end.IsZero() || end.After(w.Ctx.To) {
				end = w.Ctx.To
			}
			durMin := int64(end.Sub(start)) / 60
			if durMin < 0 {
				durMin = 0
			}
			appViolMinutes += durMin

			typ := "availability"
			switch {
			case len(inc.Details.AvailabilityBurnRates) > 0 && len(inc.Details.LatencyBurnRates) > 0:
				typ = "mixed"
			case len(inc.Details.LatencyBurnRates) > 0:
				typ = "latency"
			}
			v := &SLOViolationItem{
				AppId:            app.Id,
				AppName:          app.Id.Name,
				Category:         app.Category,
				IncidentKey:      inc.Key,
				OpenedAt:         inc.OpenedAt,
				ResolvedAt:       inc.ResolvedAt,
				DurationMinutes:  durMin,
				Severity:         inc.Severity,
				Type:             typ,
				ShortDescription: inc.ShortDescription(),
			}
			row.Incidents = append(row.Incidents, v)
			rep.Violations = append(rep.Violations, v)
		}
		row.ViolationMinutes = appViolMinutes
		totalViolationMin += appViolMinutes

		switch {
		case row.IncidentCount > 0 || hasViolationByRate:
			row.SLOStatus = model.CRITICAL
		case hasAtRiskByRate:
			row.SLOStatus = model.WARNING
		case row.HasAvailability || row.HasLatency:
			row.SLOStatus = model.OK
		default:
			row.SLOStatus = model.UNKNOWN
		}

		rep.Services = append(rep.Services, row)
	}

	rep.Summary.TotalServices = len(rep.Services)
	for _, s := range rep.Services {
		switch s.SLOStatus {
		case model.CRITICAL:
			rep.Summary.Violating++
		case model.WARNING:
			rep.Summary.AtRisk++
		case model.OK:
			rep.Summary.Compliant++
		}
	}
	if availCount > 0 {
		rep.Summary.AvgAvailabilityPct = sumAvailability / float64(availCount)
		rep.Summary.AvgAvailability = formatAvailability(rep.Summary.AvgAvailabilityPct)
	} else {
		rep.Summary.AvgAvailability = "-"
	}
	if latencyCount > 0 {
		rep.Summary.AvgLatencyPassRatePct = sumLatencyPass / float64(latencyCount)
		rep.Summary.AvgLatencyPassRate = formatAvailability(rep.Summary.AvgLatencyPassRatePct)
	} else {
		rep.Summary.AvgLatencyPassRate = "-"
	}
	if ebrCount > 0 {
		rep.Summary.AvgErrorBudgetRemaining = fmt.Sprintf("%.1f%%", sumErrorBudgetRemaining/float64(ebrCount))
	} else {
		rep.Summary.AvgErrorBudgetRemaining = "-"
	}
	rep.Summary.TotalViolationMinutes = totalViolationMin
	rep.Summary.IncidentCount = incidentCount

	sort.Slice(rep.Services, func(i, j int) bool {
		if rep.Services[i].SLOStatus != rep.Services[j].SLOStatus {
			return rep.Services[i].SLOStatus > rep.Services[j].SLOStatus
		}
		if rep.Services[i].ViolationMinutes != rep.Services[j].ViolationMinutes {
			return rep.Services[i].ViolationMinutes > rep.Services[j].ViolationMinutes
		}
		return rep.Services[i].TotalRequests > rep.Services[j].TotalRequests
	})
	sort.Slice(rep.Violations, func(i, j int) bool {
		return rep.Violations[i].OpenedAt > rep.Violations[j].OpenedAt
	})

	const topN = 10
	violators := make([]*SLOServiceRow, 0, len(rep.Services))
	for _, s := range rep.Services {
		if s.ViolationMinutes > 0 || s.SLOStatus == model.CRITICAL {
			violators = append(violators, s)
		}
	}
	sort.Slice(violators, func(i, j int) bool {
		return violators[i].ViolationMinutes > violators[j].ViolationMinutes
	})
	if len(violators) > topN {
		violators = violators[:topN]
	}
	rep.TopViolators = violators

	rep.DailyTrend = computeSLOTrend(w)
	return rep
}

// computeTimeToExhaustion projects when the budget will be exhausted at the
// observed average burn rate. It works on percentages, so it doesn't care
// whether the SLI series are stored as rates or counts.
func computeTimeToExhaustion(consumedPct float64, durationSeconds int64) string {
	if consumedPct <= 0 {
		return "∞"
	}
	if consumedPct >= 100 {
		return "exhausted"
	}
	remainingPct := 100 - consumedPct
	secondsLeft := remainingPct * float64(durationSeconds) / consumedPct
	if secondsLeft <= 0 {
		return "exhausted"
	}
	if secondsLeft > 365*86400 {
		return ">1y"
	}
	return utils.FormatDuration(timeseries.Duration(secondsLeft)*timeseries.Second, 2)
}

func computeSLOTrend(w *model.World) []SLOTrendBucket {
	duration := int64(w.Ctx.To.Sub(w.Ctx.From))
	if duration <= 0 || w.Ctx.Step <= 0 {
		return nil
	}
	bucketSize := timeseries.Day
	if duration < int64(timeseries.Day*2) {
		bucketSize = timeseries.Hour
	}
	bucketCount := int((duration + int64(bucketSize) - 1) / int64(bucketSize))
	if bucketCount <= 0 {
		return nil
	}

	type sysBucket struct {
		total, failed             float64
		latencyTotal, latencyFast float64
	}
	sys := make([]sysBucket, bucketCount)
	servicesCompliant := make([]map[model.ApplicationId]bool, bucketCount)
	servicesTotal := make([]map[model.ApplicationId]bool, bucketCount)
	for i := range servicesCompliant {
		servicesCompliant[i] = map[model.ApplicationId]bool{}
		servicesTotal[i] = map[model.ApplicationId]bool{}
	}
	stepF := float64(w.Ctx.Step)

	for _, app := range w.Applications {
		if !isRelevantApp(app) || (len(app.AvailabilitySLIs) == 0 && len(app.LatencySLIs) == 0) {
			continue
		}
		type appBucket struct {
			t, f, lt, lf float64
		}
		appBuckets := make([]appBucket, bucketCount)

		if len(app.AvailabilitySLIs) > 0 {
			sli := app.AvailabilitySLIs[0]
			tIter := sli.TotalRequests.Iter()
			fIter := sli.FailedRequests.Iter()
			for tIter.Next() {
				_ = fIter.Next()
				ts, v := tIter.Value()
				_, fv := fIter.Value()
				if timeseries.IsNaN(v) {
					continue
				}
				idx := int(int64(ts.Sub(w.Ctx.From)) / int64(bucketSize))
				if idx < 0 || idx >= bucketCount {
					continue
				}
				appBuckets[idx].t += float64(v) * stepF
				if !timeseries.IsNaN(fv) {
					appBuckets[idx].f += float64(fv) * stepF
				}
			}
		}

		if len(app.LatencySLIs) > 0 {
			sli := app.LatencySLIs[0]
			total, fast := sli.GetTotalAndFast(false)
			if total != nil && fast != nil {
				ti := total.Iter()
				fi := fast.Iter()
				for ti.Next() {
					_ = fi.Next()
					ts, v := ti.Value()
					_, fv := fi.Value()
					if timeseries.IsNaN(v) {
						continue
					}
					idx := int(int64(ts.Sub(w.Ctx.From)) / int64(bucketSize))
					if idx < 0 || idx >= bucketCount {
						continue
					}
					appBuckets[idx].lt += float64(v) * stepF
					if !timeseries.IsNaN(fv) {
						appBuckets[idx].lf += float64(fv) * stepF
					}
				}
			}
		}

		var availTarget float64 = defaultSLOAvailabilityTarget
		var latencyTarget float64 = defaultSLOLatencyTarget
		if len(app.AvailabilitySLIs) > 0 && app.AvailabilitySLIs[0].Config.ObjectivePercentage > 0 {
			availTarget = float64(app.AvailabilitySLIs[0].Config.ObjectivePercentage)
		}
		if len(app.LatencySLIs) > 0 && app.LatencySLIs[0].Config.ObjectivePercentage > 0 {
			latencyTarget = float64(app.LatencySLIs[0].Config.ObjectivePercentage)
		}

		for i := 0; i < bucketCount; i++ {
			ab := appBuckets[i]
			if ab.t == 0 && ab.lt == 0 {
				continue
			}
			sys[i].total += ab.t
			sys[i].failed += ab.f
			sys[i].latencyTotal += ab.lt
			sys[i].latencyFast += ab.lf
			servicesTotal[i][app.Id] = true

			compliant := true
			if ab.t > 0 {
				if (ab.t-ab.f)/ab.t*100 < availTarget {
					compliant = false
				}
			}
			if compliant && ab.lt > 0 {
				if ab.lf/ab.lt*100 < latencyTarget {
					compliant = false
				}
			}
			if compliant {
				servicesCompliant[i][app.Id] = true
			}
		}
	}

	res := make([]SLOTrendBucket, bucketCount)
	for i := 0; i < bucketCount; i++ {
		b := SLOTrendBucket{
			Ts:                w.Ctx.From.Add(timeseries.Duration(int64(bucketSize) * int64(i))),
			ServicesTotal:     len(servicesTotal[i]),
			ServicesCompliant: len(servicesCompliant[i]),
		}
		if sys[i].total > 0 {
			b.Availability = (sys[i].total - sys[i].failed) / sys[i].total * 100
		}
		if sys[i].latencyTotal > 0 {
			b.LatencyPassRate = sys[i].latencyFast / sys[i].latencyTotal * 100
		}
		res[i] = b
	}
	return res
}
