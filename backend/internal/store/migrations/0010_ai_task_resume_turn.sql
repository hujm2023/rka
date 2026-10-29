-- Resume marker for an AI task that is waiting for a user reply.
--
-- This column originally lived inside 0005, which had already been applied in
-- earlier development databases; editing an applied migration never re-runs it.
-- Keeping the statement idempotent here repairs every existing database without
-- requiring a rebuild, while new databases simply apply it once.
alter table ai_tasks add column if not exists resume_turn boolean not null default false;
