-- Platform users (local accounts with an argon2id hash, or LDAP/AD accounts
-- whose role is synced from their groups at each login) and small JSON
-- settings documents (LDAP/AD configuration; its bind password lives in
-- `secrets`). The admin token keeps working as break-glass access.
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL,
    role          TEXT NOT NULL,
    password_hash TEXT NOT NULL DEFAULT '',
    disabled      INTEGER NOT NULL DEFAULT 0,
    external_dn   TEXT NOT NULL DEFAULT '',
    groups        TEXT NOT NULL DEFAULT '[]',
    session_epoch INTEGER NOT NULL DEFAULT 0,
    last_login_at TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
