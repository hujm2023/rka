-- RKA platform schema v1.
-- Business data only. Original files, derived files, logs, backups and
-- credentials live under the project data root, never inside this schema.

-- ---------------------------------------------------------------- accounts
create table users (
  id uuid primary key default gen_random_uuid(),
  email text not null,
  display_name text not null,
  role text not null check (role in ('admin', 'learner')),
  password_hash text not null,
  status text not null default 'active' check (status in ('active', 'disabled')),
  must_change_password boolean not null default false,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  last_login_at timestamptz
);
create unique index users_email_key on users (lower(email));

create table invites (
  id uuid primary key default gen_random_uuid(),
  email text not null,
  role text not null check (role in ('admin', 'learner')),
  token_hash text not null unique,
  created_by uuid references users (id) on delete set null,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  accepted_at timestamptz,
  accepted_by uuid references users (id) on delete set null
);
create index invites_email_idx on invites (lower(email));

create table sessions (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  token_hash text not null unique,
  user_agent text not null default '',
  ip text not null default '',
  created_at timestamptz not null default now(),
  last_seen_at timestamptz not null default now(),
  expires_at timestamptz not null,
  revoked_at timestamptz
);
create index sessions_user_idx on sessions (user_id) where revoked_at is null;

create table platform_state (
  id smallint primary key default 1 check (id = 1),
  admin_bootstrapped_at timestamptz,
  bootstrap_closed boolean not null default false,
  bank_version text not null default '',
  content_version text not null default '',
  updated_at timestamptz not null default now()
);
insert into platform_state (id) values (1);

-- ---------------------------------------------------------------- content
create table blobs (
  sha256 text primary key,
  size bigint not null check (size >= 0),
  media_type text not null default 'text/markdown; charset=utf-8',
  rel_path text not null,
  created_at timestamptz not null default now()
);

create table materials (
  id text primary key,
  title text not null,
  book text not null,
  kind text not null check (kind in ('textbook', 'outline', 'notes', 'guide')),
  module text not null,
  source_file text not null,
  source_url text not null default '',
  license text not null default '',
  review_status text not null default 'published' check (review_status in ('draft', 'published', 'withdrawn')),
  current_version_id uuid,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table material_versions (
  id uuid primary key default gen_random_uuid(),
  material_id text not null references materials (id) on delete cascade,
  version int not null check (version > 0),
  sha256 text not null references blobs (sha256),
  chars int not null default 0,
  minutes int not null default 0,
  excerpt text not null default '',
  index_status text not null default 'ready' check (index_status in ('pending', 'building', 'ready', 'failed')),
  index_error text not null default '',
  published_at timestamptz,
  created_at timestamptz not null default now(),
  unique (material_id, version)
);

alter table materials
  add constraint materials_current_version_fk
  foreign key (current_version_id) references material_versions (id) on delete set null;

create table material_sections (
  id uuid primary key default gen_random_uuid(),
  material_version_id uuid not null references material_versions (id) on delete cascade,
  anchor text not null,
  stable_key text not null,
  title text not null,
  level int not null check (level between 1 and 6),
  start_offset int not null check (start_offset >= 0),
  path text[] not null default '{}',
  unique (material_version_id, anchor)
);
create index material_sections_key_idx on material_sections (material_version_id, stable_key);

create table questions (
  id text primary key,
  source_type text not null check (source_type in ('real', 'mock', 'ai')),
  term text not null default '',
  paper text not null default '',
  question_no int not null default 0,
  module text not null default 'other',
  knowledge text not null default '',
  difficulty text not null default '',
  figure_missing boolean not null default false,
  multi_blank boolean not null default false,
  review_status text not null default 'published' check (review_status in ('published', 'needs_review', 'withdrawn')),
  current_version_id uuid,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index questions_module_idx on questions (module);
create index questions_term_idx on questions (term);

create table question_versions (
  id uuid primary key default gen_random_uuid(),
  question_id text not null references questions (id) on delete cascade,
  revision text not null,
  stem text not null,
  options jsonb not null,
  answer text not null,
  analysis text not null default '',
  scoring_rule jsonb not null default '{"type":"single-choice","points":1,"ruleVersion":"v1"}',
  source_file text not null default '',
  source_label text not null default '',
  source_url text not null default '',
  figure jsonb,
  created_at timestamptz not null default now(),
  unique (question_id, revision)
);

alter table questions
  add constraint questions_current_version_fk
  foreign key (current_version_id) references question_versions (id) on delete set null;

create table knowledge_points (
  id text primary key,
  name text not null,
  module text not null default 'other',
  parent_id text references knowledge_points (id) on delete set null,
  created_at timestamptz not null default now()
);

create table question_knowledge_points (
  question_id text not null references questions (id) on delete cascade,
  knowledge_point_id text not null references knowledge_points (id) on delete cascade,
  source text not null default 'auto' check (source in ('auto', 'manual')),
  confidence numeric(4, 3) not null default 0,
  status text not null default 'auto' check (status in ('auto', 'approved', 'rejected')),
  primary key (question_id, knowledge_point_id)
);

create table knowledge_links (
  id uuid primary key default gen_random_uuid(),
  question_id text not null references questions (id) on delete cascade,
  material_id text not null references materials (id) on delete cascade,
  material_version_id uuid not null references material_versions (id) on delete cascade,
  section_id uuid not null references material_sections (id) on delete cascade,
  section_stable_key text not null,
  terms text[] not null default '{}',
  score numeric(6, 2) not null default 0,
  status text not null default 'auto' check (status in ('auto', 'approved', 'rejected')),
  reviewed_by uuid references users (id) on delete set null,
  reviewed_at timestamptz,
  created_at timestamptz not null default now(),
  unique (question_id, section_id)
);
create index knowledge_links_question_idx on knowledge_links (question_id);

create table subjective_items (
  id text primary key,
  kind text not null check (kind in ('case', 'essay')),
  revision text not null,
  title text not null,
  module text not null default 'other',
  term text not null default '',
  paper text not null default '',
  source_type text not null default 'real',
  source_url text not null default '',
  payload jsonb not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index subjective_items_kind_idx on subjective_items (kind);

-- ---------------------------------------------------------------- practice
create table practice_sessions (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  mode text not null check (mode in ('chapter', 'all', 'exam', 'wrong', 'favorites', 'review')),
  scope jsonb not null default '{}',
  question_ids text[] not null,
  revisions jsonb not null default '{}',
  index int not null default 0 check (index >= 0),
  elapsed_seconds int not null default 0 check (elapsed_seconds >= 0),
  status text not null default 'active' check (status in ('active', 'finished', 'abandoned')),
  bank_version text not null default '',
  version int not null default 1,
  deadline_at timestamptz,
  submitted_at timestamptz,
  score int,
  max_score int,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create unique index practice_sessions_one_active on practice_sessions (user_id) where status = 'active';
create index practice_sessions_user_idx on practice_sessions (user_id, created_at desc);

create table attempts (
  id text primary key,
  user_id uuid not null references users (id) on delete cascade,
  session_id uuid references practice_sessions (id) on delete set null,
  question_id text not null references questions (id) on delete cascade,
  question_revision text not null,
  selected text not null,
  correct boolean not null,
  assisted boolean not null default false,
  duration_ms int not null default 0 check (duration_ms >= 0),
  answered_at timestamptz not null,
  client_seq bigint,
  created_at timestamptz not null default now()
);
create index attempts_user_question_idx on attempts (user_id, question_id, answered_at desc);
create index attempts_user_created_idx on attempts (user_id, created_at desc);

create table marks (
  user_id uuid not null references users (id) on delete cascade,
  question_id text not null references questions (id) on delete cascade,
  favorite boolean not null default false,
  uncertain boolean not null default false,
  note text not null default '',
  version int not null default 1,
  updated_at timestamptz not null default now(),
  primary key (user_id, question_id)
);

create table reading_progress (
  user_id uuid not null references users (id) on delete cascade,
  material_id text not null references materials (id) on delete cascade,
  material_version_id uuid references material_versions (id) on delete set null,
  progress numeric(5, 4) not null default 0 check (progress >= 0 and progress <= 1),
  section_anchor text not null default '',
  section_offset int not null default 0,
  done boolean not null default false,
  updated_at timestamptz not null default now(),
  primary key (user_id, material_id)
);

create table annotations (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  material_id text not null references materials (id) on delete cascade,
  material_version_id uuid not null references material_versions (id) on delete cascade,
  section_anchor text not null default '',
  quote text not null default '',
  note text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);
create index annotations_user_idx on annotations (user_id, material_id) where deleted_at is null;

-- ---------------------------------------------------------------- learning evidence
create table evidence (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  kind text not null check (kind in ('reading', 'hint_used', 'assisted_pass', 'independent_pass', 'wrong', 'self_declared')),
  question_id text references questions (id) on delete cascade,
  knowledge_point_id text references knowledge_points (id) on delete set null,
  material_id text references materials (id) on delete cascade,
  rule_version text not null default 'v1',
  payload jsonb not null default '{}',
  occurred_at timestamptz not null default now(),
  created_at timestamptz not null default now()
);
create index evidence_user_kind_idx on evidence (user_id, kind, occurred_at desc);

create table mastery (
  user_id uuid not null references users (id) on delete cascade,
  knowledge_point_id text not null references knowledge_points (id) on delete cascade,
  state text not null default 'unknown' check (state in ('unknown', 'read', 'assisted', 'independent', 'self_declared')),
  evidence_count int not null default 0,
  rule_version text not null default 'v1',
  updated_at timestamptz not null default now(),
  primary key (user_id, knowledge_point_id)
);

create table review_items (
  user_id uuid not null references users (id) on delete cascade,
  question_id text not null references questions (id) on delete cascade,
  due_at timestamptz not null,
  interval_days int not null default 1,
  reason jsonb not null default '{}',
  updated_at timestamptz not null default now(),
  primary key (user_id, question_id)
);
create index review_items_due_idx on review_items (user_id, due_at);

-- ---------------------------------------------------------------- writing
create table writing_documents (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  kind text not null check (kind in ('case', 'essay')),
  target_id text not null,
  values jsonb not null default '{}',
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (user_id, kind, target_id)
);

create table writing_versions (
  id uuid primary key default gen_random_uuid(),
  document_id uuid not null references writing_documents (id) on delete cascade,
  version int not null,
  values jsonb not null,
  source text not null default 'user' check (source in ('user', 'conflict')),
  created_by uuid references users (id) on delete set null,
  created_at timestamptz not null default now(),
  unique (document_id, version, source)
);

create table writing_reviews (
  id uuid primary key default gen_random_uuid(),
  document_id uuid not null references writing_documents (id) on delete cascade,
  document_version int not null,
  task_id uuid,
  status text not null default 'queued' check (status in ('queued', 'running', 'completed', 'failed', 'cancelled')),
  feedback jsonb,
  created_at timestamptz not null default now(),
  completed_at timestamptz
);
create index writing_reviews_doc_idx on writing_reviews (document_id, created_at desc);

-- ---------------------------------------------------------------- teaching
create table teaching_identities (
  user_id uuid primary key references users (id) on delete cascade,
  teaching_user_id text not null unique,
  credential_ciphertext text not null default '',
  created_at timestamptz not null default now(),
  rotated_at timestamptz
);

create table ai_tasks (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  kind text not null check (kind in ('hint', 'explain', 'followup', 'review_case', 'review_essay', 'path', 'report')),
  status text not null default 'queued' check (status in ('queued', 'running', 'waiting_user', 'completed', 'failed', 'cancelling', 'cancelled', 'recovering')),
  context jsonb not null default '{}',
  idempotency_key text not null,
  teaching_user_id text not null default '',
  teaching_session_id text not null default '',
  teaching_turn_id text not null default '',
  attempt int not null default 0,
  lease_owner text not null default '',
  lease_until timestamptz,
  heartbeat_at timestamptz,
  cancel_requested boolean not null default false,
  error_code text not null default '',
  error_message text not null default '',
  created_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz,
  unique (user_id, idempotency_key)
);
create index ai_tasks_queue_idx on ai_tasks (status, created_at) where status in ('queued', 'recovering');
create index ai_tasks_user_idx on ai_tasks (user_id, created_at desc);

create table ai_events (
  id bigserial primary key,
  task_id uuid not null references ai_tasks (id) on delete cascade,
  seq int not null,
  type text not null,
  payload jsonb not null default '{}',
  created_at timestamptz not null default now(),
  unique (task_id, seq)
);

create table ai_messages (
  id uuid primary key default gen_random_uuid(),
  task_id uuid not null references ai_tasks (id) on delete cascade,
  role text not null check (role in ('user', 'assistant', 'system')),
  content text not null,
  created_at timestamptz not null default now()
);
create index ai_messages_task_idx on ai_messages (task_id, created_at);

-- ---------------------------------------------------------------- admin & ops
create table model_configs (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  kind text not null check (kind in ('llm', 'embedding', 'rerank')),
  provider text not null default 'openai-compatible',
  endpoint text not null default '',
  model text not null default '',
  credential_ciphertext text not null default '',
  auth_configured boolean not null default false,
  config_status text not null default 'incomplete' check (config_status in ('configured', 'incomplete')),
  connection_status text not null default 'unknown' check (connection_status in ('unknown', 'ok', 'failed')),
  capability_status text not null default 'unverified' check (capability_status in ('unverified', 'verified', 'failed')),
  capabilities jsonb not null default '{}',
  last_checked_at timestamptz,
  last_error text not null default '',
  is_default boolean not null default false,
  created_by uuid references users (id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create unique index model_configs_default_kind on model_configs (kind) where is_default;

create table content_grants (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users (id) on delete cascade,
  resource_type text not null check (resource_type in ('material', 'collection', 'capability')),
  resource_id text not null,
  granted_by uuid references users (id) on delete set null,
  created_at timestamptz not null default now(),
  revoked_at timestamptz,
  unique (user_id, resource_type, resource_id)
);

create table settings (
  key text primary key,
  value jsonb not null,
  updated_by uuid references users (id) on delete set null,
  updated_at timestamptz not null default now()
);

create table audit_log (
  id bigserial primary key,
  actor_user_id uuid references users (id) on delete set null,
  action text not null,
  target_type text not null default '',
  target_id text not null default '',
  detail jsonb not null default '{}',
  request_id text not null default '',
  ip text not null default '',
  created_at timestamptz not null default now()
);
create index audit_log_created_idx on audit_log (created_at desc);

create table jobs (
  id uuid primary key default gen_random_uuid(),
  kind text not null,
  status text not null default 'queued' check (status in ('queued', 'running', 'completed', 'failed', 'partial')),
  stats jsonb not null default '{}',
  errors jsonb not null default '[]',
  created_by uuid references users (id) on delete set null,
  created_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz
);

create table backups (
  id uuid primary key default gen_random_uuid(),
  rel_path text not null,
  size_bytes bigint not null default 0,
  sha256 text not null default '',
  manifest jsonb not null default '{}',
  status text not null default 'creating' check (status in ('creating', 'complete', 'failed', 'verified')),
  created_by uuid references users (id) on delete set null,
  created_at timestamptz not null default now()
);
