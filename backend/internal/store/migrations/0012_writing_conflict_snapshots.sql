alter table writing_versions drop constraint writing_versions_document_id_version_source_key;
create unique index writing_versions_user_version_idx
  on writing_versions (document_id, version) where source = 'user';
create index writing_versions_conflict_history_idx
  on writing_versions (document_id, version, created_at) where source = 'conflict';
