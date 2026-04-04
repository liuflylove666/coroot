package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
)

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

func (c *Client) GetRCALogPatterns(ctx context.Context, from, to timeseries.Time, services []string, limit int) ([]RCALogPattern, error) {
	var where []string
	var args []any

	where = append(where, "Timestamp BETWEEN @from AND @to")
	args = append(args,
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
	)

	where = append(where, "SeverityNumber >= 9") // WARNING and above

	if len(services) > 0 {
		where = append(where, "ServiceName IN (@services)")
		args = append(args, clickhouse.Named("services", services))
	}
	where = append(where, "NOT startsWith(ServiceName, 'KubernetesEvents')")

	args = append(args, clickhouse.Named("regex", `([0-9a-f]{8,}|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}|\d{5,})`))
	args = append(args, clickhouse.Named("replacement", "<*>"))

	q := `SELECT 
		ServiceName,
		multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1) AS sev,
		replaceRegexpAll(Body, @regex, @replacement) AS pattern,
		count() AS cnt,
		min(Timestamp) AS first_seen,
		max(Timestamp) AS last_seen,
		any(Body) AS sample
	FROM @@table_otel_logs@@
	WHERE ` + strings.Join(where, " AND ") + `
	GROUP BY ServiceName, sev, pattern
	ORDER BY cnt DESC
	LIMIT ` + fmt.Sprint(limit)

	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RCALogPattern
	for rows.Next() {
		var p RCALogPattern
		var sev int64
		if err := rows.Scan(&p.ServiceName, &sev, &p.Pattern, &p.Count, &p.FirstSeen, &p.LastSeen, &p.Sample); err != nil {
			return nil, err
		}
		p.Severity = model.Severity(sev)
		results = append(results, p)
	}
	return results, nil
}

func (c *Client) GetRCALogTimeline(ctx context.Context, from, to timeseries.Time, services []string, step timeseries.Duration) ([]RCALogSeverityBucket, error) {
	var where []string
	var args []any

	where = append(where, "Timestamp BETWEEN @from AND @to")
	args = append(args,
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
	)
	if len(services) > 0 {
		where = append(where, "ServiceName IN (@services)")
		args = append(args, clickhouse.Named("services", services))
	}
	where = append(where, "NOT startsWith(ServiceName, 'KubernetesEvents')")

	q := fmt.Sprintf(`SELECT 
		multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1) AS sev,
		toStartOfInterval(Timestamp, INTERVAL %d second) AS ts,
		count() AS cnt
	FROM @@table_otel_logs@@
	WHERE %s
	GROUP BY sev, ts
	ORDER BY ts`, int(step), strings.Join(where, " AND "))

	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RCALogSeverityBucket
	for rows.Next() {
		var b RCALogSeverityBucket
		var sev int64
		if err := rows.Scan(&sev, &b.Timestamp, &b.Count); err != nil {
			return nil, err
		}
		b.Severity = model.Severity(sev)
		results = append(results, b)
	}
	return results, nil
}

func (c *Client) GetRCATraceAnalysis(ctx context.Context, from, to timeseries.Time, services []string, limit int) ([]RCATraceGroup, error) {
	var where []string
	var args []any

	where = append(where, "Timestamp BETWEEN @from AND @to")
	args = append(args,
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
	)
	if len(services) > 0 {
		where = append(where, "ServiceName IN (@services)")
		args = append(args, clickhouse.Named("services", services))
	}
	q := `SELECT 
		ServiceName,
		SpanName,
		StatusCode,
		count() AS total,
		countIf(StatusCode = 'STATUS_CODE_ERROR') AS errors,
		avg(Duration / 1000000) AS avg_dur_ms,
		max(Duration / 1000000) AS max_dur_ms,
		quantile(0.99)(Duration / 1000000) AS p99_dur_ms,
		any(TraceId) AS sample_trace
	FROM @@table_otel_traces@@
	WHERE ` + strings.Join(where, " AND ") + `
	GROUP BY ServiceName, SpanName, StatusCode
	ORDER BY errors DESC, total DESC
	LIMIT ` + fmt.Sprint(limit)

	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RCATraceGroup
	for rows.Next() {
		var g RCATraceGroup
		if err := rows.Scan(&g.ServiceName, &g.SpanName, &g.StatusCode, &g.TotalCount, &g.ErrorCount, &g.AvgDurationMs, &g.MaxDurationMs, &g.P99DurationMs, &g.SampleTraceId); err != nil {
			return nil, err
		}
		results = append(results, g)
	}
	return results, nil
}

func (c *Client) GetRCATraceErrorSamples(ctx context.Context, from, to timeseries.Time, services []string, limit int) ([]*model.TraceSpan, error) {
	var where []string
	var args []any

	where = append(where, "Timestamp BETWEEN @from AND @to")
	args = append(args,
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
	)
	if len(services) > 0 {
		where = append(where, "ServiceName IN (@services)")
		args = append(args, clickhouse.Named("services", services))
	}
	where = append(where, "StatusCode = 'STATUS_CODE_ERROR'")

	q := `SELECT 
		Timestamp, TraceId, SpanId, ParentSpanId, SpanName, ServiceName, 
		Duration, StatusCode, StatusMessage, ResourceAttributes, SpanAttributes,
		Events.Timestamp, Events.Name, Events.Attributes
	FROM @@table_otel_traces@@
	WHERE ` + strings.Join(where, " AND ") + `
	ORDER BY Timestamp DESC
	LIMIT ` + fmt.Sprint(limit)

	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*model.TraceSpan
	for rows.Next() {
		var s model.TraceSpan
		var eventsTimestamp []time.Time
		var eventsName []string
		var eventsAttributes []map[string]string
		if err := rows.Scan(
			&s.Timestamp, &s.TraceId, &s.SpanId, &s.ParentSpanId, &s.Name, &s.ServiceName,
			&s.Duration, &s.StatusCode, &s.StatusMessage,
			&s.ResourceAttributes, &s.SpanAttributes,
			&eventsTimestamp, &eventsName, &eventsAttributes,
		); err != nil {
			return nil, err
		}
		if l := len(eventsTimestamp); l > 0 && l == len(eventsName) && l == len(eventsAttributes) {
			s.Events = make([]model.TraceSpanEvent, l)
			for i := range eventsTimestamp {
				s.Events[i].Timestamp = eventsTimestamp[i]
				s.Events[i].Name = eventsName[i]
				s.Events[i].Attributes = eventsAttributes[i]
			}
		}
		results = append(results, &s)
	}
	return results, nil
}

func (c *Client) GetRCALogsByTraceId(ctx context.Context, traceId string, from, to timeseries.Time) ([]*model.LogEntry, error) {
	q := `SELECT ServiceName, Timestamp, multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), Body, TraceId, ResourceAttributes, LogAttributes
	FROM @@table_otel_logs@@
	WHERE Timestamp BETWEEN @from AND @to AND TraceId = @traceId
	ORDER BY Timestamp
	LIMIT 100`

	rows, err := c.Query(ctx, q,
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.Named("traceId", traceId),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*model.LogEntry
	for rows.Next() {
		var e model.LogEntry
		var sev int64
		if err := rows.Scan(&e.ServiceName, &e.Timestamp, &sev, &e.Body, &e.TraceId, &e.ResourceAttributes, &e.LogAttributes); err != nil {
			return nil, err
		}
		e.Severity = model.Severity(sev)
		results = append(results, &e)
	}
	return results, nil
}
