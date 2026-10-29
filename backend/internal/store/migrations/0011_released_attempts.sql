-- All learner-facing projections must respect formal-exam disclosure rules.
-- Scoring and session restoration continue reading the immutable attempts table.
create view released_attempts as
select a.* from attempts a
where not exists (
  select 1 from practice_sessions s
  where s.id = a.session_id and s.mode = 'exam' and s.submitted_at is null
);
