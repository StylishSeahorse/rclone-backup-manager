-- Central dashboard schema. Written to be portable between SQLite and
-- PostgreSQL: TEXT UUID primary keys generated in Go (no AUTOINCREMENT/SERIAL),
-- timestamps as BIGINT unix epoch, booleans as INTEGER 0/1.
-- (The Go data layer uses "?" placeholders; for PostgreSQL swap in "$n".)

CREATE TABLE IF NOT EXISTS users (
    id            TEXT    PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,                 -- bcrypt
    created_at    BIGINT  NOT NULL
);

-- One row per enrolled Linux agent. Only a SHA-256 of the API key is stored;
-- the key itself is shown once at creation time.
CREATE TABLE IF NOT EXISTS agents (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    api_key_hash  TEXT    NOT NULL UNIQUE,
    hostname      TEXT    NOT NULL DEFAULT '',
    version       TEXT    NOT NULL DEFAULT '',
    os            TEXT    NOT NULL DEFAULT '',
    rclone_ver    TEXT    NOT NULL DEFAULT '',
    browse_roots  TEXT    NOT NULL DEFAULT '[]',    -- JSON array reported by agent
    last_seen_at  BIGINT  NOT NULL DEFAULT 0,
    created_at    BIGINT  NOT NULL
);

-- Wasabi S3 credentials. secret_key_enc is AES-256-GCM ciphertext (base64);
-- the plaintext secret is never returned by the admin API.
CREATE TABLE IF NOT EXISTS wasabi_credentials (
    id             TEXT   PRIMARY KEY,
    name           TEXT   NOT NULL UNIQUE,
    access_key     TEXT   NOT NULL,
    secret_key_enc TEXT   NOT NULL,
    region         TEXT   NOT NULL,                 -- e.g. us-east-1
    bucket         TEXT   NOT NULL,
    endpoint       TEXT   NOT NULL DEFAULT '',      -- '' => derived from region
    created_at     BIGINT NOT NULL
);

-- A backup job binds an agent + credentials + a set of paths + schedules.
CREATE TABLE IF NOT EXISTS backup_jobs (
    id            TEXT    PRIMARY KEY,
    agent_id      TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    credential_id TEXT    NOT NULL REFERENCES wasabi_credentials(id) ON DELETE RESTRICT,
    name          TEXT    NOT NULL,
    dest_prefix   TEXT    NOT NULL DEFAULT '',      -- key prefix inside the bucket
    enabled       INTEGER NOT NULL DEFAULT 1,
    -- incremental: mirror + keep replaced/deleted files in dated version folders
    -- sync:        exact mirror, deletions propagate, no history
    backup_type   TEXT    NOT NULL DEFAULT 'incremental' CHECK (backup_type IN ('incremental', 'sync')),
    retention_days INTEGER NOT NULL DEFAULT 30,             -- incremental only; 0 = keep versions forever
    created_at    BIGINT  NOT NULL,
    updated_at    BIGINT  NOT NULL,
    UNIQUE (agent_id, name)
);

-- Sources selected in the dashboard's file tree (paths as seen by the agent).
CREATE TABLE IF NOT EXISTS backup_paths (
    id     TEXT NOT NULL PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    path   TEXT NOT NULL,
    mode   TEXT NOT NULL DEFAULT 'copy' CHECK (mode IN ('copy', 'sync')), -- legacy; backup_jobs.backup_type decides
    UNIQUE (job_id, path)
);

-- Cron schedules; a job may have several. Evaluated by the agent, not the host.
CREATE TABLE IF NOT EXISTS schedules (
    id        TEXT    PRIMARY KEY,
    job_id    TEXT    NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    cron_expr TEXT    NOT NULL,
    timezone  TEXT    NOT NULL DEFAULT 'UTC',
    enabled   INTEGER NOT NULL DEFAULT 1
);

-- One row per execution of a job on an agent.
CREATE TABLE IF NOT EXISTS runs (
    id          TEXT    PRIMARY KEY,
    job_id      TEXT    NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    agent_id    TEXT    NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    trigger     TEXT    NOT NULL,                   -- schedule | manual
    status      TEXT    NOT NULL,                   -- running | success | failed | cancelled
    exit_code   INTEGER,
    summary     TEXT    NOT NULL DEFAULT '',
    started_at  BIGINT  NOT NULL,
    finished_at BIGINT,
    updated_at  BIGINT  NOT NULL                    -- last log/heartbeat, used to reap dead runs
);
CREATE INDEX IF NOT EXISTS idx_runs_job_started ON runs (job_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status      ON runs (status);

-- rclone stdout/stderr, streamed in batches by the agent. (run_id, seq) makes
-- batch retries idempotent.
CREATE TABLE IF NOT EXISTS run_logs (
    run_id TEXT   NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq    BIGINT NOT NULL,
    ts     BIGINT NOT NULL,                         -- unix ms
    stream TEXT   NOT NULL,                         -- stdout | stderr | agent
    line   TEXT   NOT NULL,
    PRIMARY KEY (run_id, seq)
);

-- Monotonic counter bumped whenever anything an agent consumes changes, so the
-- agent can tell whether its cached config is stale.
CREATE TABLE IF NOT EXISTS agent_config_rev (
    agent_id TEXT   PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    rev      BIGINT NOT NULL DEFAULT 1
);
