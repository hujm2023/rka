package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"rka/internal/httpx"
)

func lockAdmission(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('rka-ai-admission',0))`)
	return err
}

// Called with the admission lock, in the same transaction as queueing and
// reserving one invocation. Follow-ups and new tasks share the same limits.
func (s *Service) checkAdmission(ctx context.Context, tx pgx.Tx, exclude string) error {
	setting := func(key string, fallback int) (int, error) {
		var raw []byte
		err := tx.QueryRow(ctx, `select value from settings where key=$1`, key).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return fallback, nil
		}
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			return n, nil
		}
		var number float64
		if err := json.Unmarshal(raw, &number); err != nil {
			return fallback, nil
		}
		return int(number), nil
	}
	budget, err := setting("ai.budget.daily", s.cfg.AI.DailyBudget)
	if err != nil {
		return err
	}
	if budget > 0 {
		var used int
		if err := tx.QueryRow(ctx, `select count(*) from ai_invocations where created_at>=(date_trunc('day',now() at time zone 'UTC') at time zone 'UTC')`).Scan(&used); err != nil {
			return err
		}
		if used >= budget {
			return httpx.NewError(http.StatusTooManyRequests, "ai_budget_exceeded", fmt.Sprintf("今日 AI 调用预算已用完（%d/%d）。", used, budget))
		}
	}
	fallback := s.cfg.AI.MaxConcurrency
	if fallback <= 0 {
		fallback = 3
	}
	limit, err := setting("ai.concurrency.limit", fallback)
	if err != nil {
		return err
	}
	if limit > 0 {
		var active int
		if err := tx.QueryRow(ctx, `select count(*) from ai_tasks where id::text<>$1 and deleted_at is null and status in ('queued','running','waiting_user','cancelling','recovering')`, exclude).Scan(&active); err != nil {
			return err
		}
		if active >= limit {
			return httpx.NewError(http.StatusTooManyRequests, "ai_concurrency_limit", "AI 并发任务已达上限，请等待现有任务完成。")
		}
	}
	return nil
}
