package search

const pgSearchMigrateSQL = `
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

CREATE TABLE IF NOT EXISTS search_docs (
  pk TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  entity_id BIGINT NOT NULL,
  workspace_id BIGINT NOT NULL,
  owner_id BIGINT NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  snippet TEXT NOT NULL DEFAULT '',
  href TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  phone TEXT NOT NULL DEFAULT '',
  website TEXT NOT NULL DEFAULT '',
  company TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  facets TEXT NOT NULL DEFAULT '',
  embedding vector(1536),
  content_tsv tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(content, '')), 'B') ||
    setweight(to_tsvector('simple', coalesce(name, '') || ' ' || coalesce(email, '') || ' ' || coalesce(company, '') || ' ' || coalesce(subject, '')), 'A')
  ) STORED
);

CREATE INDEX IF NOT EXISTS search_docs_embedding_hnsw
  ON search_docs USING hnsw (embedding vector_cosine_ops);
CREATE INDEX IF NOT EXISTS search_docs_tsv_gin
  ON search_docs USING gin (content_tsv);
CREATE INDEX IF NOT EXISTS search_docs_email_trgm
  ON search_docs USING gin (email gin_trgm_ops);
CREATE INDEX IF NOT EXISTS search_docs_name_trgm
  ON search_docs USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS search_docs_company_trgm
  ON search_docs USING gin (company gin_trgm_ops);
CREATE INDEX IF NOT EXISTS search_docs_ws_kind
  ON search_docs (workspace_id, kind);
`
