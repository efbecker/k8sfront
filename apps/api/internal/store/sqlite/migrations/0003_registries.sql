-- Container registries (Docker Hub, GHCR, Quay, Harbor, private registries and
-- mirrors). The password/token lives encrypted in `secrets`; the CA bundle is
-- public data. JSON columns keep the lists portable (TEXT).
CREATE TABLE registries (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    kind               TEXT NOT NULL,
    host               TEXT NOT NULL UNIQUE,
    endpoint           TEXT NOT NULL,
    mirrors            TEXT NOT NULL DEFAULT '[]',
    username           TEXT NOT NULL DEFAULT '',
    password_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    ca_cert            TEXT NOT NULL DEFAULT '',
    skip_verify        INTEGER NOT NULL DEFAULT 0,
    pull_secret        INTEGER NOT NULL DEFAULT 1,
    namespaces         TEXT NOT NULL DEFAULT '[]',
    cluster_ids        TEXT NOT NULL DEFAULT '[]',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
