-- Journal of every e-mail the application tried to send.

CREATE TABLE mail_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    recipient    TEXT    NOT NULL,
    subject      TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL,
    error        TEXT    NOT NULL DEFAULT '',
    group_id     INTEGER REFERENCES alert_groups(id) ON DELETE SET NULL,
    group_name   TEXT    NOT NULL DEFAULT '',
    certificates TEXT    NOT NULL DEFAULT '',
    cert_count   INTEGER NOT NULL DEFAULT 0,
    smtp_server  TEXT    NOT NULL DEFAULT '',
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);
CREATE INDEX idx_mail_log_created ON mail_log(created_at);
CREATE INDEX idx_mail_log_group ON mail_log(group_id);
CREATE INDEX idx_mail_log_recipient ON mail_log(recipient);
