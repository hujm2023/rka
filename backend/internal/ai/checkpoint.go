package ai

import (
	"context"

	"github.com/jackc/pgx/v5"

	"rka/internal/teaching"
)

// StartIntent is committed before sending anything to the upstream. An empty
// remote ID after this point means unknown acceptance, never proof of no work.
func (s *Service) StartIntent(ctx context.Context, taskID, workerID string) error {
	tag, err := s.db.Pool.Exec(ctx, `update ai_tasks set upstream_started=true where id=$1::uuid and lease_owner=$2`, taskID, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// AppendFrame commits all normalized events and the upstream cursor together.
// Replaying a frame cannot duplicate UI events; a partial write cannot skip one.
func (s *Service) AppendFrame(ctx context.Context, taskID, workerID string, seq int64, events []teaching.Event) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var cursor int64
		if err := tx.QueryRow(ctx, `select upstream_seq from ai_tasks where id=$1::uuid and lease_owner=$2 for update`, taskID, workerID).Scan(&cursor); err != nil {
			if err == pgx.ErrNoRows {
				return ErrLeaseLost
			}
			return err
		}
		if seq > 0 && seq <= cursor {
			return nil
		}
		waiting := false
		for _, event := range events {
			if event.Type == EventQuestion {
				waiting = true
			}
			if event.Type == EventDone || event.Type == EventError || event.Type == EventCancelled {
				continue
			}
			if _, err := appendEventTx(ctx, tx, taskID, event.Type, event.Payload); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `update ai_tasks set upstream_seq=greatest(upstream_seq,$2),upstream_waiting=$3 where id=$1::uuid`, taskID, seq, waiting)
		return err
	})
}

func (s *Service) WaitingResult(ctx context.Context, taskID string) (teaching.Result, error) {
	var text string
	err := s.db.Pool.QueryRow(ctx, `select coalesce(string_agg(coalesce(payload->>'text',''),' ' order by seq),'') from ai_events where task_id=$1::uuid and type in ('delta','question') and seq>coalesce((select max(seq) from ai_events where task_id=$1::uuid and type='accepted'),0)`, taskID).Scan(&text)
	return teaching.Result{Waiting: true, FinalText: text}, err
}

// A reply that was claimed but never dispatched can resume its existing turn.
func (s *Service) RequeueUnstartedReply(ctx context.Context, taskID, workerID string) error {
	tag, err := s.db.Pool.Exec(ctx, `update ai_tasks set status='queued',lease_owner='',lease_until=null where id=$1::uuid and lease_owner=$2 and not upstream_started and resume_turn`, taskID, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}
