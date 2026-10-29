alter table ai_tasks
  add column upstream_started boolean not null default false,
  add column upstream_seq bigint not null default 0,
  add column upstream_waiting boolean not null default false,
  add column invocation int not null default 1;
-- Existing interrupted tasks may have reached the upstream before the upgrade.
update ai_tasks set upstream_started=true where status in ('running','recovering','waiting_user','cancelling');
create table ai_invocations (
  task_id uuid not null references ai_tasks(id) on delete cascade,
  invocation int not null,
  created_at timestamptz not null default now(),
  primary key(task_id, invocation)
);
insert into ai_invocations(task_id,invocation,created_at) select id,1,created_at from ai_tasks;
create index ai_invocations_budget_idx on ai_invocations(created_at);
