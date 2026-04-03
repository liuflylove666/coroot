package api

import (
	"encoding/json"
	"net/http"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/gorilla/mux"
	"k8s.io/klog"
)

func (api *Api) TestIncident(w http.ResponseWriter, r *http.Request, u *db.User) {
	projectId := db.ProjectId(mux.Vars(r)["project"])

	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "create":
		api.createTestIncident(w, projectId)
	case "create_with_rca":
		api.createTestIncidentWithRCA(w, projectId)
	default:
		http.Error(w, "unknown action, use 'create' or 'create_with_rca'", http.StatusBadRequest)
	}
}

func (api *Api) createTestIncident(w http.ResponseWriter, projectId db.ProjectId) {
	now := timeseries.Now()
	appId := model.ApplicationId{
		ClusterId: string(projectId),
		Namespace: "_",
		Kind:      "Unknown",
		Name:      "devops-redis",
	}

	incident := &model.ApplicationIncident{
		ApplicationId: appId,
		Key:           utils.NanoId(8),
		OpenedAt:      now.Add(-30 * timeseries.Minute),
		Severity:      model.CRITICAL,
		Details: model.IncidentDetails{
			AvailabilityBurnRates: []model.BurnRate{
				{
					Severity:               model.CRITICAL,
					LongWindow:             timeseries.Hour,
					ShortWindow:            5 * timeseries.Minute,
					LongWindowBurnRate:     14.4,
					ShortWindowBurnRate:    28.8,
					LongWindowPercentage:   1.44,
					ShortWindowPercentage:  2.88,
					Threshold:              14.4,
				},
			},
			AvailabilityImpact: model.Impact{AffectedRequestPercentage: 15.5},
		},
	}

	if err := api.db.CreateIncident(projectId, appId, incident); err != nil {
		klog.Errorln(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	utils.WriteJson(w, map[string]string{
		"status":       "created",
		"incident_key": incident.Key,
		"message":      "Test incident created. Visit the Incidents page to see it, then click 'Refresh RCA' to trigger AI analysis.",
	})
}

func (api *Api) createTestIncidentWithRCA(w http.ResponseWriter, projectId db.ProjectId) {
	now := timeseries.Now()
	appId := model.ApplicationId{
		ClusterId: string(projectId),
		Namespace: "_",
		Kind:      "Unknown",
		Name:      "devops-redis",
	}

	incident := &model.ApplicationIncident{
		ApplicationId: appId,
		Key:           utils.NanoId(8),
		OpenedAt:      now.Add(-45 * timeseries.Minute),
		Severity:      model.CRITICAL,
		Details: model.IncidentDetails{
			AvailabilityBurnRates: []model.BurnRate{
				{
					Severity:              model.CRITICAL,
					LongWindow:            timeseries.Hour,
					ShortWindow:           5 * timeseries.Minute,
					LongWindowBurnRate:    14.4,
					ShortWindowBurnRate:   28.8,
					LongWindowPercentage:  1.44,
					ShortWindowPercentage: 2.88,
					Threshold:             14.4,
				},
			},
			LatencyBurnRates: []model.BurnRate{
				{
					Severity:              model.WARNING,
					LongWindow:            timeseries.Hour,
					ShortWindow:           5 * timeseries.Minute,
					LongWindowBurnRate:    8.2,
					ShortWindowBurnRate:   16.4,
					LongWindowPercentage:  0.82,
					ShortWindowPercentage: 1.64,
					Threshold:             14.4,
				},
			},
			AvailabilityImpact: model.Impact{AffectedRequestPercentage: 23.5},
			LatencyImpact:      model.Impact{AffectedRequestPercentage: 45.2},
		},
		RCA: &model.RCA{
			Status:       "OK",
			ShortSummary: "Redis connection timeout due to memory pressure causing OOM kills",
			RootCause: `The **devops-redis** service experienced connection timeouts caused by memory exhaustion.

### Key Findings:
- Redis instance hit its memory limit (~256MB configured), triggering the OOM killer
- Connected clients experienced ` + "`ETIMEDOUT`" + ` errors during the kill/restart cycle
- The memory spike correlates with a large batch job that ran at the anomaly start time

### Evidence:
- Memory usage hit 100% at the anomaly start time
- Container restarts detected: 3 restarts within 30 minutes
- Error rate increased from 0 to ~15.5% of requests`,
			ImmediateFixes: `### Short-term:
1. **Increase Redis memory limit** to at least 512MB:
` + "   ```yaml\n   resources:\n     limits:\n       memory: 512Mi\n   ```" + `

2. **Configure Redis maxmemory-policy** to ` + "`allkeys-lru`" + ` to handle memory pressure gracefully

3. **Rate-limit the batch job** to prevent memory spikes

### Long-term:
- Consider Redis Cluster for horizontal scaling
- Implement connection pooling in clients
- Add memory usage alerts at 80% threshold`,
			DetailedRootCause: `## Anomaly Summary
The devops-redis service experienced critical availability degradation with 15.5% of requests failing and latency spikes affecting 45.2% of requests.

## Issue Propagation
` + "```" + `
batch-job → devops-redis (memory pressure) → all downstream services (connection errors)
` + "```" + `

## Timeline
| Time | Event |
|------|-------|
| T-45m | Batch job started, Redis memory usage began increasing |
| T-30m | Redis hit memory limit, OOM killer triggered |
| T-28m | Container restarted, brief recovery |
| T-25m | Second OOM kill as batch job continued |
| T-20m | Third restart, batch job still active |
| T-15m | Memory stabilized after partial batch completion |

## Evidence
- **Memory metrics**: Max=256MB, steady at 99.8% for 15 minutes before OOM
- **Container events**: 3x ` + "`OOMKilled`" + ` events in 30-minute window
- **Error traces**: ` + "`ETIMEDOUT`" + ` connecting to redis:6379
- **Latency**: P99 latency jumped from 5ms to 2.8s during the incident

## Root Cause
The root cause is insufficient memory allocation for the Redis instance combined with an unbounded batch job that caused memory to exceed the container's limit. The OOM killer terminated the Redis process, causing cascading connection failures across all services depending on Redis.`,
		},
	}

	if err := api.db.CreateIncident(projectId, appId, incident); err != nil {
		klog.Errorln(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := api.db.UpdateIncidentRCA(projectId, incident, incident.RCA); err != nil {
		klog.Errorln(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	utils.WriteJson(w, map[string]string{
		"status":       "created",
		"incident_key": incident.Key,
		"message":      "Test incident with complete RCA created. Visit the Incidents page to see it.",
	})
}
