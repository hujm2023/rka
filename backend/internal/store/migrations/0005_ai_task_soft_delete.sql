-- User-controlled soft deletion of AI tasks and their summaries.
-- Events and messages stay in the audit trail; API queries exclude deleted
-- tasks so a user can remove an incorrect AI summary without losing history.
alter table ai_tasks add column if not exists deleted_at timestamptz;

-- A user message on a task that was waiting for an answer resumes the existing
-- DeepTutor turn instead of starting a new one.
alter table ai_tasks add column if not exists resume_turn boolean not null default false;

create index if not exists ai_tasks_user_live_idx
  on ai_tasks (user_id, created_at desc)
  where deleted_at is null;
