-- Initial schema (SQLite). Timestamps are unix seconds (UTC).

CREATE TABLE users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    email               TEXT    NOT NULL UNIQUE,
    name                TEXT    NOT NULL DEFAULT '',
    password_hash       TEXT    NOT NULL DEFAULT '',
    role                TEXT    NOT NULL DEFAULT 'user',
    status              TEXT    NOT NULL DEFAULT 'invited',
    failed_logins       INTEGER NOT NULL DEFAULT 0,
    locked_until        INTEGER,
    password_changed_at INTEGER,
    last_login_at       INTEGER,
    invited_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE password_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL
);
CREATE INDEX idx_password_history_user ON password_history(user_id, id);

CREATE TABLE sessions (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE user_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT    NOT NULL UNIQUE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose    TEXT    NOT NULL,
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);
CREATE INDEX idx_user_tokens_user ON user_tokens(user_id, purpose);
CREATE INDEX idx_user_tokens_created_by ON user_tokens(created_by);

CREATE TABLE alert_groups (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE recipients (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    email      TEXT    NOT NULL UNIQUE,
    name       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE group_recipients (
    group_id     INTEGER NOT NULL REFERENCES alert_groups(id) ON DELETE CASCADE,
    recipient_id INTEGER NOT NULL REFERENCES recipients(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (group_id, recipient_id)
);
CREATE INDEX idx_group_recipients_recipient ON group_recipients(recipient_id);

CREATE TABLE certificates (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    url                 TEXT    NOT NULL,
    host                TEXT    NOT NULL,
    port                INTEGER NOT NULL,
    name                TEXT    NOT NULL DEFAULT '',
    notes               TEXT    NOT NULL DEFAULT '',
    enabled             INTEGER NOT NULL DEFAULT 1,
    created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    last_checked_at     INTEGER,
    last_success_at     INTEGER,
    last_error          TEXT    NOT NULL DEFAULT '',
    subject_cn          TEXT    NOT NULL DEFAULT '',
    subject_org         TEXT    NOT NULL DEFAULT '',
    issuer_cn           TEXT    NOT NULL DEFAULT '',
    issuer_org          TEXT    NOT NULL DEFAULT '',
    sans                TEXT,
    serial_number       TEXT    NOT NULL DEFAULT '',
    fingerprint_sha256  TEXT    NOT NULL DEFAULT '',
    signature_algorithm TEXT    NOT NULL DEFAULT '',
    key_algorithm       TEXT    NOT NULL DEFAULT '',
    key_bits            INTEGER NOT NULL DEFAULT 0,
    not_before          INTEGER,
    not_after           INTEGER,
    trusted             INTEGER NOT NULL DEFAULT 0,
    verify_error        TEXT    NOT NULL DEFAULT '',
    tls_version         TEXT    NOT NULL DEFAULT '',
    resolved_ip         TEXT    NOT NULL DEFAULT '',
    chain               TEXT,
    UNIQUE (host, port)
);
CREATE INDEX idx_certificates_not_after ON certificates(not_after);
CREATE INDEX idx_certificates_created_by ON certificates(created_by);

CREATE TABLE group_certificates (
    group_id       INTEGER NOT NULL REFERENCES alert_groups(id) ON DELETE CASCADE,
    certificate_id INTEGER NOT NULL REFERENCES certificates(id) ON DELETE CASCADE,
    created_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (group_id, certificate_id)
);
CREATE INDEX idx_group_certificates_cert ON group_certificates(certificate_id);
CREATE INDEX idx_group_certificates_created_by ON group_certificates(created_by);

CREATE TABLE notifications (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id        INTEGER REFERENCES alert_groups(id) ON DELETE SET NULL,
    certificate_id  INTEGER REFERENCES certificates(id) ON DELETE SET NULL,
    recipient_id    INTEGER REFERENCES recipients(id) ON DELETE SET NULL,
    group_name      TEXT    NOT NULL DEFAULT '',
    certificate_url TEXT    NOT NULL DEFAULT '',
    recipient_email TEXT    NOT NULL DEFAULT '',
    not_after       INTEGER NOT NULL,
    threshold_days  INTEGER NOT NULL,
    days_left       INTEGER NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT    NOT NULL DEFAULT '',
    next_attempt_at INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    sent_at         INTEGER,
    UNIQUE (group_id, certificate_id, recipient_id, not_after, threshold_days)
);
CREATE INDEX idx_notifications_status ON notifications(status, next_attempt_at);
CREATE INDEX idx_notifications_created ON notifications(created_at);
CREATE INDEX idx_notifications_certificate ON notifications(certificate_id);
CREATE INDEX idx_notifications_recipient ON notifications(recipient_id);

CREATE TABLE settings (
    name       TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    user_email  TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    entity_type TEXT    NOT NULL DEFAULT '',
    entity_id   INTEGER,
    details     TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_audit_log_created ON audit_log(created_at);
CREATE INDEX idx_audit_log_user ON audit_log(user_id);

CREATE TABLE app_locks (
    name       TEXT    PRIMARY KEY,
    owner      TEXT    NOT NULL,
    expires_at INTEGER NOT NULL
);
