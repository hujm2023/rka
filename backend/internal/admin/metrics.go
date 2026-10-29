package admin

import (
	"context"
	"net/http"
	"time"

	"rka/internal/httpx"
)

// handleMetrics reports task counts, durations and failure counts. It never
// invents a monetary cost: when no billing feed is configured the cost section
// explicitly says so.
func (s *Service) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	response := map[string]any{"generatedAt": time.Now().UTC()}

	aiMetrics, err := s.aiMetrics(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	response["ai"] = aiMetrics

	jobMetrics, err := s.jobMetrics(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	response["jobs"] = jobMetrics

	backups, err := s.backupMetrics(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	response["backups"] = backups

	var users int64
	if err := s.db.Pool.QueryRow(ctx, `select count(*) from users`).Scan(&users); err != nil {
		httpx.Error(w, r, err)
		return
	}
	response["users"] = map[string]any{"total": users}
	response["cost"] = map[string]any{
		"status":   "unknown",
		"amount":   nil,
		"currency": nil,
		"reason":   "未接入计费数据，不展示金额。",
	}
	httpx.JSON(w, r, http.StatusOK, response)
}

func (s *Service) aiMetrics(ctx context.Context) (map[string]any, error) {
	metrics := map[string]any{}
	var total, failed, retried int64
	if err := s.db.Pool.QueryRow(ctx, `
		select count(*),
		       count(*) filter (where status = 'failed'),
		       count(*) filter (where attempt > 1)
		from ai_tasks`).Scan(&total, &failed, &retried); err != nil {
		return nil, err
	}
	var avgDuration, p95Duration float64
	if err := s.db.Pool.QueryRow(ctx, `
		select coalesce(avg(extract(epoch from (finished_at - started_at)) * 1000), 0),
		       coalesce(percentile_cont(0.95) within group (
		           order by extract(epoch from (finished_at - started_at)) * 1000), 0)
		from ai_tasks where started_at is not null and finished_at is not null`).Scan(&avgDuration, &p95Duration); err != nil {
		return nil, err
	}
	byStatus, err := s.countMap(ctx, `select status, count(*) from ai_tasks group by status`)
	if err != nil {
		return nil, err
	}
	byKind, err := s.countMap(ctx, `select kind, count(*) from ai_tasks group by kind`)
	if err != nil {
		return nil, err
	}
	var events, messages int64
	if err := s.db.Pool.QueryRow(ctx, `select count(*) from ai_events`).Scan(&events); err != nil {
		return nil, err
	}
	if err := s.db.Pool.QueryRow(ctx, `select count(*) from ai_messages`).Scan(&messages); err != nil {
		return nil, err
	}
	metrics["tasksTotal"] = total
	metrics["calls"] = total
	metrics["failed"] = failed
	metrics["failures"] = failed
	metrics["retried"] = retried
	metrics["avgDurationMs"] = int64(avgDuration)
	metrics["p95DurationMs"] = int64(p95Duration)
	metrics["byStatus"] = byStatus
	metrics["byKind"] = byKind
	metrics["events"] = events
	metrics["messages"] = messages
	return metrics, nil
}

func (s *Service) jobMetrics(ctx context.Context) (map[string]any, error) {
	metrics := map[string]any{}
	var total, failed int64
	if err := s.db.Pool.QueryRow(ctx, `
		select count(*), count(*) filter (where status = 'failed') from jobs`).Scan(&total, &failed); err != nil {
		return nil, err
	}
	var avgDuration float64
	if err := s.db.Pool.QueryRow(ctx, `
		select coalesce(avg(extract(epoch from (finished_at - started_at)) * 1000), 0)
		from jobs where started_at is not null and finished_at is not null`).Scan(&avgDuration); err != nil {
		return nil, err
	}
	byStatus, err := s.countMap(ctx, `select status, count(*) from jobs group by status`)
	if err != nil {
		return nil, err
	}
	byKind, err := s.countMap(ctx, `select kind, count(*) from jobs group by kind`)
	if err != nil {
		return nil, err
	}
	metrics["total"] = total
	metrics["failed"] = failed
	metrics["avgDurationMs"] = int64(avgDuration)
	metrics["byStatus"] = byStatus
	metrics["byKind"] = byKind
	return metrics, nil
}

func (s *Service) backupMetrics(ctx context.Context) (map[string]any, error) {
	var total, verified int64
	var latest *time.Time
	if err := s.db.Pool.QueryRow(ctx, `
		select count(*), count(*) filter (where status = 'verified'), max(created_at)
		from backups`).Scan(&total, &verified, &latest); err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "verified": verified, "lastCreatedAt": latest}, nil
}

func (s *Service) countMap(ctx context.Context, sql string) (map[string]int64, error) {
	rows, err := s.db.Pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		result[key] = count
	}
	return result, rows.Err()
}
