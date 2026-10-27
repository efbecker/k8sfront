-- Encrypted secrets (AES-256-GCM). AAD = "secret:<id>:<kind>".
CREATE TABLE secrets (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    ciphertext  BLOB NOT NULL,
    key_id      TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE TABLE clusters (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL UNIQUE,
    kubernetes_version     TEXT NOT NULL DEFAULT '',
    pod_cidr               TEXT NOT NULL DEFAULT '',
    service_cidr           TEXT NOT NULL DEFAULT '',
    cni                    TEXT NOT NULL DEFAULT '',
    topology               TEXT NOT NULL DEFAULT '',
    control_plane_endpoint TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT 'draft',
    kubeconfig_secret_id   TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);

CREATE TABLE servers (
    id                   TEXT PRIMARY KEY,
    cluster_id           TEXT REFERENCES clusters(id) ON DELETE SET NULL,
    name                 TEXT NOT NULL,
    host                 TEXT NOT NULL,
    port                 INTEGER NOT NULL DEFAULT 22,
    ssh_user             TEXT NOT NULL,
    auth_method          TEXT NOT NULL CHECK (auth_method IN ('key','password')),
    role                 TEXT NOT NULL DEFAULT '' CHECK (role IN ('','control-plane','worker')),
    credential_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    passphrase_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    sudo_secret_id       TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    sudo_mode            TEXT NOT NULL DEFAULT 'unknown',
    os_id                TEXT NOT NULL DEFAULT '',
    os_version_id        TEXT NOT NULL DEFAULT '',
    os_pretty_name       TEXT NOT NULL DEFAULT '',
    kernel               TEXT NOT NULL DEFAULT '',
    last_check_at        TEXT,
    last_check_status    TEXT NOT NULL DEFAULT '',
    last_check_report    TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    UNIQUE (host, port)
);

-- SSH host keys, keyed by "host:port". Mismatch aborts the connection.
CREATE TABLE known_hosts (
    host_port   TEXT PRIMARY KEY,
    key_type    TEXT NOT NULL,
    public_key  BLOB NOT NULL,
    fingerprint TEXT NOT NULL,
    first_seen  TEXT NOT NULL,
    last_seen   TEXT NOT NULL
);

CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed','paused','cancelled')),
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    params      TEXT NOT NULL DEFAULT '{}',
    result      TEXT,
    error       TEXT NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    updated_at  TEXT NOT NULL
);
CREATE INDEX jobs_status_idx ON jobs(status);
CREATE INDEX jobs_target_idx ON jobs(target_type, target_id);

CREATE TABLE job_steps (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    key         TEXT NOT NULL,
    name        TEXT NOT NULL,
    server_id   TEXT REFERENCES servers(id) ON DELETE SET NULL,
    status      TEXT NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    started_at  TEXT,
    finished_at TEXT
);
CREATE INDEX job_steps_job_idx ON job_steps(job_id, seq);

-- Redacted output. (job_id, seq) is the resume cursor for WebSocket clients.
CREATE TABLE job_logs (
    job_id    TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    seq       INTEGER NOT NULL,
    ts        TEXT NOT NULL,
    stream    TEXT NOT NULL,
    step_id   INTEGER,
    server_id TEXT,
    line      TEXT NOT NULL,
    PRIMARY KEY (job_id, seq)
) WITHOUT ROWID;

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT NOT NULL,
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    details     TEXT,
    remote_addr TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_ts_idx ON audit_log(ts);
