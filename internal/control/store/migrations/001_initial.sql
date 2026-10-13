CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT PRIMARY KEY,
  checksum TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS config_revisions (
  revision BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  parent_revision BIGINT,
  content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
  config BYTEA NOT NULL CHECK (octet_length(config) BETWEEN 1 AND 1048576),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor TEXT NOT NULL,
  reason TEXT NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('publish','rollback')),
  rollback_source_revision BIGINT,
  UNIQUE (revision, content_hash),
  FOREIGN KEY (parent_revision) REFERENCES config_revisions(revision),
  FOREIGN KEY (rollback_source_revision) REFERENCES config_revisions(revision)
);
CREATE TABLE IF NOT EXISTS active_config (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  revision BIGINT NOT NULL REFERENCES config_revisions(revision),
  content_hash TEXT NOT NULL,
  FOREIGN KEY (revision, content_hash) REFERENCES config_revisions(revision, content_hash)
);
CREATE TABLE IF NOT EXISTS config_audit (
  audit_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  revision BIGINT NOT NULL REFERENCES config_revisions(revision),
  operation TEXT NOT NULL CHECK (operation IN ('publish','rollback')),
  actor TEXT NOT NULL,
  reason TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS config_revisions_created_idx ON config_revisions (created_at DESC, revision DESC);

CREATE TABLE IF NOT EXISTS data_plane_instances (
  instance_id TEXT NOT NULL CHECK (instance_id <> ''),
  boot_id TEXT NOT NULL,
  build TEXT NOT NULL DEFAULT '',
  connected BOOLEAN NOT NULL DEFAULT FALSE,
  active_revision BIGINT NOT NULL DEFAULT 0,
  active_hash TEXT NOT NULL DEFAULT '',
  last_heartbeat TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_nack TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (instance_id)
);
