create table knowledge_indexes (
 id uuid primary key default gen_random_uuid(),
 material_version_id uuid not null references material_versions(id),
 fingerprint text not null,
 kb_name text not null unique,
 status text not null default 'queued' check(status in ('queued','indexing','ready','failed')),
 error text not null default '',
 lease_until timestamptz,
 lease_owner text not null default '',
 updated_at timestamptz not null default now(),
 created_at timestamptz not null default now(),
 unique(material_version_id,fingerprint)
);
