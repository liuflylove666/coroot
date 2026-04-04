package api

import (
	"context"
	"net/http"
	"time"

	"github.com/coroot/coroot/ai"
	"github.com/coroot/coroot/auditor"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/cloud"
	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/gorilla/mux"
	"k8s.io/klog"
)

type rcaResponse struct {
	LatencyChart         *model.Chart `json:"latency_chart,omitempty"`
	ErrorsChart          *model.Chart `json:"errors_chart,omitempty"`
	Summary              *model.RCA   `json:"summary,omitempty"`
	AIIntegrationEnabled bool         `json:"ai_integration_enabled"`
	Error                string       `json:"error,omitempty"`
}

func (api *Api) RCA(w http.ResponseWriter, r *http.Request, u *db.User) {
	rca := &model.RCA{}
	projectId := db.ProjectId(mux.Vars(r)["project"])
	from, to, incident, _ := api.getTimeContext(r)
	withSummary := r.URL.Query().Get("withSummary") == "true"
	var sliCharts struct {
		latency *model.Chart
		errors  *model.Chart
	}

	defer func() {
		if incident != nil {
			if err := api.db.UpdateIncidentRCA(projectId, incident, rca); err != nil {
				klog.Errorln(err)
			}
		} else {
		resp := rcaResponse{
			LatencyChart:         sliCharts.latency,
			ErrorsChart:          sliCharts.errors,
			AIIntegrationEnabled: true,
		}
		if rca != nil && rca.Status == "OK" {
			resp.Summary = rca
		} else if rca != nil && rca.Error != "" {
			resp.Error = rca.Error
		}
		utils.WriteJson(w, resp)
		}
	}()

	aiCfg, err := ai.GetConfig(api.db)
	if err != nil {
		klog.Errorln(err)
	}
	useLocalAI := aiCfg.IsEnabled()

	useCloudAI := false
	if !useLocalAI {
		cloudAPI := cloud.API(api.db, api.deploymentUuid, api.instanceUuid, r.Referer())
		if status, _ := cloudAPI.RCAStatus(r.Context(), false); status == "OK" {
			useCloudAI = true
		}
	}

	project, err := api.db.GetProject(projectId)
	if err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}

	if project.Multicluster() {
		klog.Errorln("RCA is not supported for multi-cluster projects")
		rca.Status = "Failed"
		rca.Error = "RCA is not supported for multi-cluster projects"
		return
	}

	appId, err := GetApplicationId(r)
	if err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}

	if incident == nil {
		world, _, _, werr := api.LoadWorldByRequest(r)
		if werr == nil && world != nil {
			app := world.GetApplication(appId)
			if app != nil {
				auditor.Audit(world, project, app, nil)
				ctx := world.Ctx
				if len(app.LatencySLIs) > 0 {
					sliCharts.latency = model.NewChart(ctx, "Latency, seconds").
						PercentilesFrom(app.LatencySLIs[0].Histogram, 0.5, 0.95, 0.99)
				}
				if len(app.AvailabilitySLIs) > 0 {
					sliCharts.errors = model.NewChart(ctx, "Errors, per second").
						AddSeries("errors", app.AvailabilitySLIs[0].FailedRequests.Map(timeseries.NanToZero), "black").
						Stacked()
				}
			}
		}

		if !withSummary {
			return
		}
	}

	cacheClient := api.cache.GetCacheClient(project.Id)
	cacheTo, err := cacheClient.GetTo()
	if err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}
	if cacheTo.IsZero() || cacheTo.Before(from) {
		rca.Status = "Failed"
		rca.Error = "Metric cache is empty"
		return
	}
	cacheStep, err := cacheClient.GetStep(from, to)
	if err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}
	if cacheStep == 0 {
		rca.Status = "Failed"
		rca.Error = "Metric cache is empty"
		return
	}
	if cacheTo.Before(to) {
		to = cacheTo
	}
	step := increaseStepForBigDurations(from, to, cacheStep)

	rcaRequest := cloud.RCARequest{
		Ctx:                         timeseries.NewContext(from, to, step),
		ApplicationId:               appId,
		ApplicationCategorySettings: project.Settings.ApplicationCategorySettings,
		CustomApplications:          project.Settings.CustomApplications,
		CustomCloudPricing:          project.Settings.CustomCloudPricing,
	}
	rcaRequest.Ctx.RawStep = cacheStep
	if incident != nil {
		rcaRequest.Ctx.From, rcaRequest.Ctx.To = api.IncidentTimeContext(projectId, incident, to)
	}

	if rcaRequest.CheckConfigs, err = api.db.GetCheckConfigs(project.Id); err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}
	if rcaRequest.ApplicationDeployments, err = api.db.GetApplicationDeployments(project.Id); err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}

	ctr := constructor.New(api.db, project, map[db.ProjectId]constructor.Cache{project.Id: cacheClient}, api.pricing)
	if rcaRequest.Metrics, err = ctr.QueryCache(r.Context(), cacheClient, project, rcaRequest.Ctx.From, rcaRequest.Ctx.To, rcaRequest.Ctx.Step); err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}

	var world *model.World
	var ch *clickhouse.Client
	if ch, err = api.GetClickhouseClient(project, ""); err != nil {
		klog.Errorln(err)
	}
	if ch != nil {
		defer ch.Close()
		rcaRequest.KubernetesEvents, err = ch.GetKubernetesEvents(r.Context(), from, to, 1000)
		if err != nil {
			klog.Errorln(err)
		}

		func() {
			world, _, _, err = api.LoadWorldByRequest(r)
			if err != nil {
				klog.Errorln(err)
				return
			}
			app := world.GetApplication(appId)
			if app == nil {
				return
			}
			rcaRequest.ErrorTrace, rcaRequest.SlowTrace, err = ch.GetTracesViolatingSLOs(r.Context(), rcaRequest.Ctx.From, rcaRequest.Ctx.To, world, app)
			if err != nil {
				klog.Errorln(err)
				return
			}
		}()
	}

	if useLocalAI {
		aiClient, err := ai.NewClient(aiCfg)
		if err != nil {
			klog.Errorln(err)
			rca.Status = "Failed"
			rca.Error = err.Error()
			return
		}
		rcaData := ai.RCAData{
			ApplicationID: appId.String(),
			From:          time.Unix(int64(rcaRequest.Ctx.From), 0),
			To:            time.Unix(int64(rcaRequest.Ctx.To), 0),
			Metrics:       rcaRequest.Metrics,
			Events:        rcaRequest.KubernetesEvents,
			ErrorTrace:    rcaRequest.ErrorTrace,
			SlowTrace:     rcaRequest.SlowTrace,
			Deployments:   rcaRequest.ApplicationDeployments,
		}
		aiCtx, aiCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer aiCancel()
		rcaResponse, err := ai.RunRCA(aiCtx, aiClient, rcaData)
		if err != nil {
			klog.Errorln(err)
			rca.Status = "Failed"
			rca.Error = err.Error()
			return
		}
		rca = rcaResponse
		rca.Status = "OK"

		if world != nil {
			rca.PropagationMap = buildPropagationMap(world, appId)
		}
		return
	}

	if useCloudAI {
		cloudAPI := cloud.API(api.db, api.deploymentUuid, api.instanceUuid, r.Referer())
		rcaResponse, err := cloudAPI.RCA(r.Context(), rcaRequest)
		if err != nil {
			klog.Errorln(err)
			rca.Status = "Failed"
			rca.Error = err.Error()
			return
		}
		rca = rcaResponse
		rca.Status = "OK"
		return
	}

	klog.Infof("RCA: using built-in analysis for %s (no AI configured)", appId)
	rcaData := ai.RCAData{
		ApplicationID: appId.String(),
		From:          time.Unix(int64(rcaRequest.Ctx.From), 0),
		To:            time.Unix(int64(rcaRequest.Ctx.To), 0),
		Metrics:       rcaRequest.Metrics,
		Events:        rcaRequest.KubernetesEvents,
		ErrorTrace:    rcaRequest.ErrorTrace,
		SlowTrace:     rcaRequest.SlowTrace,
		Deployments:   rcaRequest.ApplicationDeployments,
	}
	enrichRCAFromClickHouse(r.Context(), ch, rcaRequest.Ctx.From, rcaRequest.Ctx.To, rcaRequest.Ctx.Step, appId, &rcaData)
	rca = ai.RunBuiltinRCA(rcaData)
	if world != nil {
		rca.PropagationMap = buildPropagationMap(world, appId)
	}
}

func (api *Api) IncidentRCA(ctx context.Context, project *db.Project, world *model.World, incident *model.ApplicationIncident) {
	rca := incident.RCA
	if rca != nil && (rca.Status == "OK" || rca.Status == "Failed") {
		return
	}
	if rca == nil {
		rca = &model.RCA{}
	}
	defer func() {
		if err := api.db.UpdateIncidentRCA(project.Id, incident, rca); err != nil {
			klog.Errorln(err)
		}
	}()

	aiCfg, err := ai.GetConfig(api.db)
	if err != nil {
		klog.Errorln(err)
	}
	useLocalAI := aiCfg.IsEnabled()

	useCloudAI := false
	if !useLocalAI {
		cloudAPI := cloud.API(api.db, api.deploymentUuid, api.instanceUuid, "")
		if status, _ := cloudAPI.RCAStatus(ctx, true); status == "OK" {
			useCloudAI = true
		}
	}

	if incident.RCA == nil {
		if err := api.db.UpdateIncidentRCA(project.Id, incident, &model.RCA{Status: "In progress"}); err != nil {
			klog.Errorln(err)
			return
		}
	}

	if project.Multicluster() {
		klog.Errorln("RCA is not supported for mult-cluster projects")
		rca.Status = "Failed"
		rca.Error = "RCA is not supported for mult-cluster projects"
		return
	}

	app := world.GetApplication(incident.ApplicationId)
	if app == nil {
		klog.Errorln("application not found")
		rca.Status = "Failed"
		rca.Error = "application not found"
		return
	}

	rcaRequest := cloud.RCARequest{
		Ctx:                         world.Ctx,
		ApplicationId:               app.Id,
		CheckConfigs:                world.CheckConfigs,
		ApplicationCategorySettings: project.Settings.ApplicationCategorySettings,
		CustomApplications:          project.Settings.CustomApplications,
		CustomCloudPricing:          project.Settings.CustomCloudPricing,
	}
	rcaRequest.Ctx.From, rcaRequest.Ctx.To = api.IncidentTimeContext(project.Id, incident, world.Ctx.To)

	if rcaRequest.ApplicationDeployments, err = api.db.GetApplicationDeployments(project.Id); err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}

	cacheClient := api.cache.GetCacheClient(project.Id)
	ctr := constructor.New(api.db, project, map[db.ProjectId]constructor.Cache{project.Id: cacheClient}, api.pricing)
	if rcaRequest.Metrics, err = ctr.QueryCache(ctx, cacheClient, project, rcaRequest.Ctx.From, rcaRequest.Ctx.To, rcaRequest.Ctx.Step); err != nil {
		klog.Errorln(err)
		rca.Status = "Failed"
		rca.Error = err.Error()
		return
	}

	var ch *clickhouse.Client
	if ch, err = api.GetClickhouseClient(project, ""); err != nil {
		klog.Errorln(err)
	}
	if ch != nil {
		defer ch.Close()
		rcaRequest.KubernetesEvents, err = ch.GetKubernetesEvents(ctx, rcaRequest.Ctx.From, rcaRequest.Ctx.To, 1000)
		if err != nil {
			klog.Errorln(err)
		}
		rcaRequest.ErrorTrace, rcaRequest.SlowTrace, err = ch.GetTracesViolatingSLOs(ctx, rcaRequest.Ctx.From, rcaRequest.Ctx.To, world, app)
		if err != nil {
			klog.Errorln(err)
		}
	}

	if useLocalAI {
		aiClient, clientErr := ai.NewClient(aiCfg)
		if clientErr != nil {
			klog.Errorln(clientErr)
			rca.Status = "Failed"
			rca.Error = clientErr.Error()
			return
		}
		rcaData := ai.RCAData{
			ApplicationID: app.Id.String(),
			From:          time.Unix(int64(rcaRequest.Ctx.From), 0),
			To:            time.Unix(int64(rcaRequest.Ctx.To), 0),
			Metrics:       rcaRequest.Metrics,
			Events:        rcaRequest.KubernetesEvents,
			ErrorTrace:    rcaRequest.ErrorTrace,
			SlowTrace:     rcaRequest.SlowTrace,
			Deployments:   rcaRequest.ApplicationDeployments,
		}
		aiCtx, aiCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer aiCancel()
		rcaResponse, rcaErr := ai.RunRCA(aiCtx, aiClient, rcaData)
		if rcaErr != nil {
			klog.Errorln(rcaErr)
			rca.Status = "Failed"
			rca.Error = rcaErr.Error()
			return
		}
		rca = rcaResponse
		rca.Status = "OK"
		rca.PropagationMap = buildPropagationMap(world, app.Id)
		return
	}

	if useCloudAI {
		cloudAPI := cloud.API(api.db, api.deploymentUuid, api.instanceUuid, "")
		rcaResponse, err := cloudAPI.RCA(ctx, rcaRequest)
		if err != nil {
			klog.Errorln(err)
			rca.Status = "Failed"
			rca.Error = err.Error()
			return
		}
		rca = rcaResponse
		rca.Status = "OK"
		return
	}

	klog.Infof("IncidentRCA: using built-in analysis for %s (no AI configured)", app.Id)
	rcaData := ai.RCAData{
		ApplicationID: app.Id.String(),
		From:          time.Unix(int64(rcaRequest.Ctx.From), 0),
		To:            time.Unix(int64(rcaRequest.Ctx.To), 0),
		Metrics:       rcaRequest.Metrics,
		Events:        rcaRequest.KubernetesEvents,
		ErrorTrace:    rcaRequest.ErrorTrace,
		SlowTrace:     rcaRequest.SlowTrace,
		Deployments:   rcaRequest.ApplicationDeployments,
	}
	enrichRCAFromClickHouse(ctx, ch, rcaRequest.Ctx.From, rcaRequest.Ctx.To, rcaRequest.Ctx.Step, app.Id, &rcaData)
	rca = ai.RunBuiltinRCA(rcaData)
	rca.PropagationMap = buildPropagationMap(world, app.Id)
}

func buildPropagationMap(world *model.World, targetAppId model.ApplicationId) *model.PropagationMap {
	pm := &model.PropagationMap{}
	app := world.GetApplication(targetAppId)
	if app == nil {
		return nil
	}

	seen := map[model.ApplicationId]bool{}

	addApp := func(a *model.Application, status model.Status) *model.PropagationMapApplication {
		if seen[a.Id] {
			return nil
		}
		seen[a.Id] = true
		pma := &model.PropagationMapApplication{
			Id:     a.Id,
			Labels: a.Labels(),
			Status: status,
		}
		pm.Applications = append(pm.Applications, pma)
		return pma
	}

	targetPma := addApp(app, model.WARNING)
	if targetPma == nil {
		return nil
	}

	for _, u := range app.Upstreams {
		if u.RemoteApplication == nil || seen[u.RemoteApplication.Id] {
			continue
		}
		upApp := u.RemoteApplication
		pma := addApp(upApp, upApp.Status)
		if pma != nil {
			targetPma.Upstreams = append(targetPma.Upstreams, &model.PropagationMapApplicationLink{
				Id:     upApp.Id,
				Status: upApp.Status,
			})
		}
	}

	for _, d := range app.Downstreams {
		if d.RemoteApplication == nil || seen[d.RemoteApplication.Id] {
			continue
		}
		downApp := d.RemoteApplication
		pma := addApp(downApp, downApp.Status)
		if pma != nil {
			targetPma.Downstreams = append(targetPma.Downstreams, &model.PropagationMapApplicationLink{
				Id:     downApp.Id,
				Status: downApp.Status,
			})
		}
	}

	if len(pm.Applications) <= 1 {
		return nil
	}
	return pm
}

func enrichRCAFromClickHouse(ctx context.Context, ch *clickhouse.Client, from, to timeseries.Time, step timeseries.Duration, appId model.ApplicationId, data *ai.RCAData) {
	if ch == nil {
		return
	}
	if step == 0 {
		step = 15
	}

	var services []string
	if appId.Name != "" {
		services = append(services, "/docker/"+appId.Name)
		if appId.Namespace != "" && appId.Namespace != "_" {
			services = append(services, appId.Namespace+"/"+appId.Name)
		}
	}

	logPatterns, err := ch.GetRCALogPatterns(ctx, from, to, services, 50)
	if err != nil {
		klog.Warningf("RCA: failed to query log patterns: %v", err)
	} else {
		for _, p := range logPatterns {
			data.LogPatterns = append(data.LogPatterns, ai.RCALogPattern{
				ServiceName: p.ServiceName,
				Severity:    p.Severity,
				Pattern:     p.Pattern,
				Count:       p.Count,
				FirstSeen:   p.FirstSeen,
				LastSeen:    p.LastSeen,
				Sample:      p.Sample,
			})
		}
	}

	logTimeline, err := ch.GetRCALogTimeline(ctx, from, to, services, step)
	if err != nil {
		klog.Warningf("RCA: failed to query log timeline: %v", err)
	} else {
		for _, b := range logTimeline {
			data.LogTimeline = append(data.LogTimeline, ai.RCALogSeverityBucket{
				Severity:  b.Severity,
				Timestamp: b.Timestamp,
				Count:     b.Count,
			})
		}
	}

	traceGroups, err := ch.GetRCATraceAnalysis(ctx, from, to, services, 50)
	if err != nil {
		klog.Warningf("RCA: failed to query trace analysis: %v", err)
	} else {
		for _, g := range traceGroups {
			data.TraceGroups = append(data.TraceGroups, ai.RCATraceGroup{
				ServiceName:   g.ServiceName,
				SpanName:      g.SpanName,
				StatusCode:    g.StatusCode,
				TotalCount:    g.TotalCount,
				ErrorCount:    g.ErrorCount,
				AvgDurationMs: g.AvgDurationMs,
				MaxDurationMs: g.MaxDurationMs,
				P99DurationMs: g.P99DurationMs,
				SampleTraceId: g.SampleTraceId,
			})
		}
	}

	errorSpans, err := ch.GetRCATraceErrorSamples(ctx, from, to, services, 20)
	if err != nil {
		klog.Warningf("RCA: failed to query error spans: %v", err)
	} else {
		data.ErrorSpans = errorSpans
	}

	if len(errorSpans) > 0 {
		traceId := errorSpans[0].TraceId
		if traceId != "" {
			correlatedLogs, err := ch.GetRCALogsByTraceId(ctx, traceId, from, to)
			if err != nil {
				klog.Warningf("RCA: failed to query correlated logs: %v", err)
			} else {
				data.CorrelatedLogs = correlatedLogs
			}
		}
	}
}

func (api *Api) IncidentTimeContext(projectId db.ProjectId, incident *model.ApplicationIncident, now timeseries.Time) (timeseries.Time, timeseries.Time) {
	from := incident.OpenedAt.Add(-model.IncidentTimeOffset)
	to := now
	if incident.Resolved() {
		to = incident.ResolvedAt
	}
	incidents, err := api.db.GetApplicationIncidents(projectId, from, incident.OpenedAt)
	if err != nil {
		klog.Errorln(err)
		return from, to
	}
	for _, i := range incidents[incident.ApplicationId] {
		if i.Key == incident.Key || !i.Resolved() {
			continue
		}
		if i.ResolvedAt.After(from) && i.ResolvedAt.Before(to) {
			from = i.ResolvedAt
		}
	}
	return from, to
}
