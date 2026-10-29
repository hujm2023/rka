-- Content search and version lookup support.
--
-- Section bodies are derived text: the original markdown stays content
-- addressed on disk (blobs.rel_path), while the parsed section text lives here
-- so the search endpoint can query material titles, section titles and bodies
-- without reading the file system on every request.

alter table material_sections add column body text not null default '';

-- Reading order within one material version.
create index material_sections_order_idx on material_sections (material_version_id, start_offset);

-- Catalog and link queries filter by the currently published version.
create index knowledge_links_version_idx on knowledge_links (material_version_id, question_id);

-- Admin job lists are ordered by kind and creation time.
create index jobs_kind_created_idx on jobs (kind, created_at desc);
