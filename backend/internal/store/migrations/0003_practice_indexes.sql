-- Practice query indexes.
--
-- Session views load the latest attempt per question inside one session, and
-- the attempts/stats history lists a learner's attempts in reverse
-- chronological order. 0001 only indexes (user_id, question_id) and
-- (user_id, created_at); these two indexes keep the session and history paths
-- from scanning the whole attempts table as the fact log grows.

create index if not exists attempts_session_idx
  on attempts (session_id, answered_at desc, created_at desc);

create index if not exists attempts_user_answered_idx
  on attempts (user_id, answered_at desc, created_at desc);
