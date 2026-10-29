-- Legacy lesson index entries from the bundled content snapshot.
--
-- The old reader and the AI citations address sources as `lessonId`. RKA now
-- serves full materials, but keeps a thin compatibility index so existing
-- deep links (`/knowledge?id=<lessonId>`) and citations can resolve to the
-- material version and section they were generated from. The body itself is
-- never duplicated here: it is content addressed in blobs/originals.

create table content_lessons (
  id text primary key,
  title text not null,
  book text not null default '',
  module text not null default 'other',
  source_file text not null default '',
  source_url text not null default '',
  license text not null default '',
  chars int not null default 0,
  minutes int not null default 0,
  excerpt text not null default '',
  body_sha256 text not null default '',
  material_id text references materials (id) on delete set null,
  section_stable_key text,
  created_at timestamptz not null default now()
);

create index content_lessons_material_idx on content_lessons (material_id);
create index content_lessons_section_idx on content_lessons (section_stable_key);
