package overview

import (
	"fmt"
	"math"
	"sort"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

type Stability struct {
	NorthStar  *NorthStarMetrics  `json:"north_star"`
	Compliance *ComplianceOverview `json:"compliance"`
	Leading    *LeadingIndicators  `json:"leading"`
	Services   []*ServiceStability `json:"services"`
}

type NorthStarMetrics struct {
	Availability       string       `json:"availability"`
	AvailabilityStatus model.Status `json:"availability_status"`
	Latency            string       `json:"latency"`
	LatencyStatus      model.Status `json:"latency_status"`
	TotalRequests      float64      `json:"total_requests"`
	FailedRequests     float64      `json:"failed_requests"`
	ServiceCount       int          `json:"service_count"`
	HealthyCount       int          `json:"healthy_count"`
}

type ComplianceOverview struct {
	Total     int    `json:"total"`
	Compliant int    `json:"compliant"`
	Warning   int    `json:"warning"`
	Critical  int    `json:"critical"`
	Rate      string `json:"rate"`
}

type LeadingIndicators struct {
	DeploymentCount int    `json:"deployment_count"`
	IncidentCount   int    `json:"incident_count"`
	OpenIncidents   int    `json:"open_incidents"`
	AvgMTTR         string `json:"avg_mttr"`
	SLOViolations   int    `json:"slo_violations"`
}

type ServiceStability struct {
	Id                 model.ApplicationId       `json:"id"`
	Category           model.ApplicationCategory `json:"category"`
	Availability       string                    `json:"availability"`
	AvailabilityStatus model.Status              `json:"availability_status"`
	Latency            string                    `json:"latency"`
	LatencyStatus      model.Status              `json:"latency_status"`
	SLOStatus          model.Status              `json:"slo_status"`
	ErrorBudget        string                    `json:"error_budget"`
	HasIncident        bool                      `json:"has_incident"`
	Deployments        int                       `json:"deployments"`
	TotalRequests      float64                   `json:"total_requests"`
}

func renderStability(w *model.World) *Stability {
	s := &Stability{
		NorthStar:  &NorthStarMetrics{},
		Compliance: &ComplianceOverview{},
		Leading:    &LeadingIndicators{},
	}

	var totalReqs, failedReqs float64
	var weightedLatencySum, latencyWeightSum float64

	for _, app := range w.Applications {
		if !isRelevantApp(app) {
			continue
		}

		hasAnySLI := len(app.AvailabilitySLIs) > 0 || len(app.LatencySLIs) > 0
		if !hasAnySLI {
			continue
		}

		svc := &ServiceStability{
			Id:       app.Id,
			Category: app.Category,
		}

		var svcTotal, svcFailed float64
		if len(app.AvailabilitySLIs) > 0 {
			sli := app.AvailabilitySLIs[0]
			total := float64(sli.TotalRequests.Reduce(timeseries.NanSum))
			failed := float64(sli.FailedRequests.Reduce(timeseries.NanSum))
			svcTotal = total
			svcFailed = failed
			if total > 0 {
				avail := (total - failed) / total * 100
				svc.Availability = formatAvailability(avail)
				svc.TotalRequests = total
				totalReqs += total
				failedReqs += failed
			}
			remaining := computeErrorBudgetRemaining(sli.Config.ObjectivePercentage, total, failed)
			svc.ErrorBudget = fmt.Sprintf("%.1f%%", remaining)
		}

		if len(app.LatencySLIs) > 0 {
			sli := app.LatencySLIs[0]
			lat := quantile(sli.Histogram, sli.Config.ObjectivePercentage/100)
			if lat > 0 {
				svc.Latency = utils.FormatLatency(lat)
				if svcTotal > 0 {
					weightedLatencySum += float64(lat) * svcTotal
					latencyWeightSum += svcTotal
				}
			}
		}

		svc.AvailabilityStatus, svc.LatencyStatus = extractSLOStatuses(app)
		svc.SLOStatus = maxStatus(svc.AvailabilityStatus, svc.LatencyStatus)

		svc.HasIncident = hasOpenIncident(app, w.Ctx.To)

		svc.Deployments = len(app.Deployments)
		s.Leading.DeploymentCount += svc.Deployments

		s.Services = append(s.Services, svc)
		s.NorthStar.ServiceCount++

		switch {
		case svc.SLOStatus >= model.CRITICAL:
			s.Compliance.Critical++
			s.Leading.SLOViolations++
		case svc.SLOStatus >= model.WARNING:
			s.Compliance.Warning++
		default:
			s.NorthStar.HealthyCount++
			s.Compliance.Compliant++
		}

		incidentCount, openIncidents, totalMTTR := aggregateIncidents(app, svcTotal, svcFailed)
		s.Leading.IncidentCount += incidentCount
		s.Leading.OpenIncidents += openIncidents
		if totalMTTR > 0 && incidentCount > 0 {
			s.Leading.AvgMTTR = utils.FormatDuration(timeseries.Duration(totalMTTR/int64(incidentCount))*timeseries.Second, 1)
		}
	}

	s.Compliance.Total = s.NorthStar.ServiceCount
	if s.Compliance.Total > 0 {
		rate := float64(s.Compliance.Compliant) / float64(s.Compliance.Total) * 100
		s.Compliance.Rate = fmt.Sprintf("%.1f%%", rate)
	} else {
		s.Compliance.Rate = "-"
	}

	s.NorthStar.TotalRequests = totalReqs
	s.NorthStar.FailedRequests = failedReqs
	if totalReqs > 0 {
		systemAvail := (totalReqs - failedReqs) / totalReqs * 100
		s.NorthStar.Availability = formatAvailability(systemAvail)
		if systemAvail < 99.0 {
			s.NorthStar.AvailabilityStatus = model.CRITICAL
		} else if systemAvail < 99.9 {
			s.NorthStar.AvailabilityStatus = model.WARNING
		} else {
			s.NorthStar.AvailabilityStatus = model.OK
		}
	} else {
		s.NorthStar.Availability = "-"
	}
	if latencyWeightSum > 0 {
		systemLatency := float32(weightedLatencySum / latencyWeightSum)
		s.NorthStar.Latency = utils.FormatLatency(systemLatency)
		s.NorthStar.LatencyStatus = model.OK
	} else {
		s.NorthStar.Latency = "-"
	}

	computeAverageMTTR(w, s)

	sort.Slice(s.Services, func(i, j int) bool {
		if s.Services[i].SLOStatus != s.Services[j].SLOStatus {
			return s.Services[i].SLOStatus > s.Services[j].SLOStatus
		}
		return s.Services[i].TotalRequests > s.Services[j].TotalRequests
	})

	return s
}

func isRelevantApp(app *model.Application) bool {
	switch {
	case app.IsK8s():
		return true
	case app.Id.Kind == model.ApplicationKindNomadJobGroup:
		return true
	case !app.IsStandalone():
		return true
	}
	return false
}

func extractSLOStatuses(app *model.Application) (avail, latency model.Status) {
	for _, r := range app.Reports {
		for _, ch := range r.Checks {
			switch ch.Id {
			case model.Checks.SLOAvailability.Id:
				avail = ch.Status
			case model.Checks.SLOLatency.Id:
				latency = ch.Status
			}
		}
	}
	return
}

func maxStatus(a, b model.Status) model.Status {
	if a > b {
		return a
	}
	return b
}

func hasOpenIncident(app *model.Application, now timeseries.Time) bool {
	if len(app.Incidents) == 0 {
		return false
	}
	last := app.Incidents[len(app.Incidents)-1]
	return !last.Resolved() || last.ResolvedAt.After(now)
}

func computeErrorBudgetRemaining(objectivePct float32, total, failed float64) float64 {
	if total == 0 || objectivePct <= 0 {
		return 100
	}
	allowedFailureRate := (100 - float64(objectivePct)) / 100
	totalBudget := total * allowedFailureRate
	if totalBudget <= 0 {
		return 0
	}
	consumed := failed / totalBudget * 100
	remaining := 100 - consumed
	return math.Max(remaining, 0)
}

func aggregateIncidents(app *model.Application, _, _ float64) (count, open int, totalMTTRSeconds int64) {
	for _, inc := range app.Incidents {
		count++
		if !inc.Resolved() {
			open++
		} else {
			mttr := int64(inc.ResolvedAt-inc.OpenedAt) / int64(timeseries.Second)
			totalMTTRSeconds += mttr
		}
	}
	return
}

func computeAverageMTTR(w *model.World, s *Stability) {
	var resolvedCount int
	var totalSeconds int64
	for _, app := range w.Applications {
		if !isRelevantApp(app) {
			continue
		}
		for _, inc := range app.Incidents {
			if inc.Resolved() {
				resolvedCount++
				totalSeconds += int64(inc.ResolvedAt-inc.OpenedAt) / int64(timeseries.Second)
			}
		}
	}
	if resolvedCount > 0 {
		avg := totalSeconds / int64(resolvedCount)
		s.Leading.AvgMTTR = utils.FormatDuration(timeseries.Duration(avg)*timeseries.Second, 1)
	} else {
		s.Leading.AvgMTTR = "-"
	}
}

func formatAvailability(pct float64) string {
	if pct >= 99.99 {
		return fmt.Sprintf("%.3f%%", pct)
	}
	if pct >= 99.0 {
		return fmt.Sprintf("%.2f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}
