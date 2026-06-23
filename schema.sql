-- Se ejecuta automáticamente al arrancar (ver db.go),
-- pero lo dejamos aquí también como referencia / para crear la DB a mano si quieres.

CREATE TABLE IF NOT EXISTS users (
    username        TEXT PRIMARY KEY,
    password_hash   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    registered_ip   TEXT,
    last_login_ip   TEXT,
    last_login_at   TIMESTAMPTZ,
    banned          BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS torrents (
    info_hash       TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    size            BIGINT NOT NULL DEFAULT 0,
    uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    downloads       INTEGER NOT NULL DEFAULT 0,
    uploader        TEXT REFERENCES users(username) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS reports (
    id              TEXT PRIMARY KEY,
    info_hash       TEXT NOT NULL,
    torrent_name    TEXT NOT NULL,
    reason          TEXT NOT NULL,
    details         TEXT,
    reporter_ip     TEXT NOT NULL,
    username        TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved        BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS idx_torrents_name_lower ON torrents (LOWER(name));
CREATE INDEX IF NOT EXISTS idx_torrents_uploader ON torrents (uploader);
CREATE INDEX IF NOT EXISTS idx_reports_resolved ON reports (resolved);
CREATE INDEX IF NOT EXISTS idx_reports_info_hash ON reports (info_hash);
