package practice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"rka/internal/learning"
)

// candidate is one question eligible for a new practice queue, together with
// the learner's latest attempt and mark used for ordering and mode filters.
type candidate struct {
	ID            string
	Revision      string
	Module        string
	Term          string
	Paper         string
	QuestionNo    int
	Knowledge     string
	Difficulty    string
	SourceType    string
	FigureMissing bool
	MultiBlank    bool
	Stem          string
	Options       []byte

	LatestAnsweredAt *time.Time
	LatestCorrect    *bool
	LatestAssisted   *bool
	Favorite         *bool
}

// selectQuestions resolves the server-side queue for a session request. The
// queue is chosen once, at creation time; reopening a session never re-runs
// this selection.
func (s *Service) selectQuestions(ctx context.Context, userID, mode string, sc scope, limit int, now time.Time) ([]candidate, error) {
	args := []any{userID}
	clauses := []string{"q.review_status = 'published'"}
	if mode == "exam" {
		// A formal exam must be built from real / recalled papers only, even if
		// an internal caller forgot to set the scope source.
		clauses = append(clauses, "q.source_type = 'real'")
	}
	if !sc.IncludeMissing {
		// Missing-figure questions stay out of the default queue: the learner
		// must not be asked to guess a figure that failed to import.
		clauses = append(clauses, "q.figure_missing = false")
	}
	if sc.Query != "" {
		literal := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(sc.Query)
		args = append(args, "%"+literal+"%")
		position := len(args)
		clauses = append(clauses, fmt.Sprintf("(v.stem ilike $%d or q.knowledge ilike $%d or q.term ilike $%d)", position, position, position))
	}
	if sc.Module != "" {
		args = append(args, sc.Module)
		clauses = append(clauses, fmt.Sprintf("q.module = $%d", len(args)))
	}
	if sc.Term != "" {
		args = append(args, sc.Term)
		clauses = append(clauses, fmt.Sprintf("q.term = $%d", len(args)))
	}
	if sc.Source != "" {
		args = append(args, sc.Source)
		clauses = append(clauses, fmt.Sprintf("q.source_type = $%d", len(args)))
	}
	if sc.MaterialID != "" {
		args = append(args, sc.MaterialID)
		condition := fmt.Sprintf(`exists (
			select 1 from knowledge_links kl
			join material_sections ms on ms.id = kl.section_id
			where kl.question_id = q.id and kl.material_id = $%d and kl.status <> 'rejected'`, len(args))
		if sc.SectionID != "" {
			args = append(args, sc.SectionID)
			position := len(args)
			condition += fmt.Sprintf(` and exists (
                select 1 from material_sections parent
                where parent.material_version_id = ms.material_version_id
                  and (parent.anchor = $%[1]d or parent.stable_key = $%[1]d or parent.id::text = $%[1]d)
                  and (ms.id = parent.id or parent.anchor = any(ms.path))
            )`, position)
		}
		condition += ")"
		clauses = append(clauses, condition)
	}

	query := `
		select q.id, v.revision, q.module, q.term, q.paper, q.question_no, q.knowledge,
		       q.difficulty, q.source_type, q.figure_missing, q.multi_blank, v.stem, v.options,
		       a.answered_at, a.correct, a.assisted, m.favorite
		from questions q
		join question_versions v on v.id = q.current_version_id
		left join lateral (
			select answered_at, correct, assisted
			from released_attempts
			where user_id = $1::uuid and question_id = q.id
			order by answered_at desc, created_at desc
			limit 1
		) a on true
		left join marks m on m.user_id = $1::uuid and m.question_id = q.id
		where ` + strings.Join(clauses, " and ")

	rows, err := s.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select practice questions: %w", err)
	}
	defer rows.Close()

	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.ID, &item.Revision, &item.Module, &item.Term, &item.Paper,
			&item.QuestionNo, &item.Knowledge, &item.Difficulty, &item.SourceType,
			&item.FigureMissing, &item.MultiBlank, &item.Stem, &item.Options,
			&item.LatestAnsweredAt, &item.LatestCorrect, &item.LatestAssisted, &item.Favorite); err != nil {
			return nil, fmt.Errorf("scan practice question: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	filtered := candidates[:0]
	for _, item := range candidates {
		switch mode {
		case "wrong":
			if item.LatestCorrect == nil || *item.LatestCorrect {
				continue
			}
		case "favorites":
			if item.Favorite == nil || !*item.Favorite {
				continue
			}
		case "review":
			if item.LatestAnsweredAt == nil || item.LatestCorrect == nil || item.LatestAssisted == nil {
				continue
			}
			if learning.ReviewDueAt(*item.LatestAnsweredAt, *item.LatestCorrect, *item.LatestAssisted).After(now) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	candidates = filtered

	sortCandidates(mode, candidates)

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// sortCandidates mirrors the browse experience: unvisited questions first for
// free/chapter practice, papers in reverse chronological order, then question
// number. Formal exams follow the paper order.
func sortCandidates(mode string, items []candidate) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if mode == "exam" {
			return lessQuestionOrder(left, right)
		}
		if mode == "all" || mode == "chapter" {
			leftVisited := left.LatestAnsweredAt != nil
			rightVisited := right.LatestAnsweredAt != nil
			if leftVisited != rightVisited {
				return !leftVisited
			}
		}
		if left.Term != right.Term {
			return left.Term > right.Term
		}
		return lessQuestionOrder(left, right)
	})
}

func lessQuestionOrder(left, right candidate) bool {
	if left.QuestionNo != right.QuestionNo {
		return left.QuestionNo < right.QuestionNo
	}
	return left.ID < right.ID
}
