-- Read-only preservation of legacy RKA v1 browser records that have no
-- server-side equivalent: finished/active browser practice sessions and AI
-- conversations. They are kept so a backup import does not silently discard
-- user data; they are never used to mutate server-side practice state.
create table imported_records (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  source text not null,
  kind text not null check (kind in ('session', 'chat')),
  external_id text not null,
  payload jsonb not null,
  note text not null default '',
  created_at timestamptz not null default now(),
  unique (user_id, source, kind, external_id)
);

create index imported_records_user_idx on imported_records (user_id, created_at desc);
