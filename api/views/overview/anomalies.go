package overview

import (
	"sort"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

type AnomalyApplication struct {
	Id       model.ApplicationId       `json:"id"`
	Category model.ApplicationCategory `json:"category"`
	RPS      float32                   `json:"rps"`
	Latency  *AnomalyChart             `json:"latency"`
	Errors   *AnomalyChart             `json:"errors"`
	Incident *AnomalyIncident          `json:"incident"`
}

type AnomalyChart struct {
	Msg   string    `json:"msg"`
	Chart []float32 `json:"chart"`
}

type AnomalyIncident struct {
	Key        string          `json:"key"`
	Severity   model.Status    `json:"severity"`
	ResolvedAt timeseries.Time `json:"resolved_at"`
}

func renderAnomalies(w *model.World) []*AnomalyApplication {
	var res []*AnomalyApplication

	for _, app := range w.Applications {
		switch {
		case app.IsK8s():
		case app.Id.Kind == model.ApplicationKindNomadJobGroup:
		case !app.IsStandalone():
		default:
			continue
		}

		hasLatencySLI := len(app.LatencySLIs) > 0
		hasAvailabilitySLI := len(app.AvailabilitySLIs) > 0
		if !hasLatencySLI && !hasAvailabilitySLI {
			continue
		}

		a := &AnomalyApplication{
			Id:       app.Id,
			Category: app.Category,
		}

		if hasAvailabilitySLI {
			sli := app.AvailabilitySLIs[0]
			totalSum := sli.TotalRequests.Reduce(timeseries.NanSum)
			failedSum := sli.FailedRequests.Reduce(timeseries.NanSum)
			duration := float32(w.Ctx.To-w.Ctx.From) / float32(timeseries.Second)
			if duration > 0 && totalSum > 0 {
				a.RPS = totalSum / duration
			}

			chart := &AnomalyChart{}
			if sli.FailedRequests != nil {
				mapped := sli.FailedRequests.Map(timeseries.NanToZero)
				if mapped != nil {
					iter := mapped.Iter()
					for iter.Next() {
						_, v := iter.Value()
						chart.Chart = append(chart.Chart, v)
					}
				}
			}

			if totalSum > 0 && failedSum > 0 {
				pct := failedSum * 100 / totalSum
				for _, r := range app.Reports {
					for _, ch := range r.Checks {
						if ch.Id == model.Checks.SLOAvailability.Id && ch.Status >= model.WARNING {
							chart.Msg = formatPercent(pct) + " errors"
						}
					}
				}
			}
			a.Errors = chart
		}

		if hasLatencySLI {
			sli := app.LatencySLIs[0]
			chart := &AnomalyChart{}
			latency := quantile(sli.Histogram, sli.Config.ObjectivePercentage/100)
			if latency > 0 {
				for _, r := range app.Reports {
					for _, ch := range r.Checks {
						if ch.Id == model.Checks.SLOLatency.Id && ch.Status >= model.WARNING {
							chart.Msg = utils.FormatLatency(latency)
						}
					}
				}
			}

			if len(sli.Histogram) > 0 {
				last := sli.Histogram[len(sli.Histogram)-1]
				if last.TimeSeries != nil {
					mapped := last.TimeSeries.Map(timeseries.NanToZero)
					if mapped != nil {
						iter := mapped.Iter()
						for iter.Next() {
							_, v := iter.Value()
							chart.Chart = append(chart.Chart, v)
						}
					}
				}
			}
			a.Latency = chart
		}

		if len(app.Incidents) > 0 {
			last := app.Incidents[len(app.Incidents)-1]
			a.Incident = &AnomalyIncident{
				Key:        last.Key,
				Severity:   last.Severity,
				ResolvedAt: last.ResolvedAt,
			}
		}

		res = append(res, a)
	}

	sort.Slice(res, func(i, j int) bool {
		iHasAnomaly := (res[i].Errors != nil && res[i].Errors.Msg != "") || (res[i].Latency != nil && res[i].Latency.Msg != "")
		jHasAnomaly := (res[j].Errors != nil && res[j].Errors.Msg != "") || (res[j].Latency != nil && res[j].Latency.Msg != "")
		if iHasAnomaly != jHasAnomaly {
			return iHasAnomaly
		}
		return res[i].Id.Name < res[j].Id.Name
	})

	return res
}
