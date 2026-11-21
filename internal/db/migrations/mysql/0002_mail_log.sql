-- Journal of every e-mail the application tried to send.

CREATE TABLE mail_log (
    id           BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    kind         VARCHAR(16)   NOT NULL,
    recipient    VARCHAR(255)  NOT NULL,
    subject      VARCHAR(500)  NOT NULL DEFAULT '',
    status       VARCHAR(16)   NOT NULL,
    error        VARCHAR(1000) NOT NULL DEFAULT '',
    group_id     BIGINT        NULL,
    group_name   VARCHAR(255)  NOT NULL DEFAULT '',
    certificates VARCHAR(4000) NOT NULL DEFAULT '',
    cert_count   INT           NOT NULL DEFAULT 0,
    smtp_server  VARCHAR(300)  NOT NULL DEFAULT '',
    duration_ms  INT           NOT NULL DEFAULT 0,
    created_at   BIGINT        NOT NULL,
    KEY idx_mail_log_created (created_at),
    KEY idx_mail_log_group (group_id),
    KEY idx_mail_log_recipient (recipient),
    CONSTRAINT fk_mail_log_group FOREIGN KEY (group_id) REFERENCES alert_groups(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
