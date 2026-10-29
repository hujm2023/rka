-- Bind reading evidence to the exact material version the learner reported.
-- Reading progress already stores the version; the evidence ledger keeps the
-- same binding so historical reading facts stay traceable after a revision.
alter table evidence
  add column material_version_id uuid references material_versions (id) on delete set null;

create index evidence_material_version_idx
  on evidence (material_version_id)
  where material_version_id is not null;
