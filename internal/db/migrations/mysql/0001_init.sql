-- Initial schema (MySQL 8+/MariaDB 10.5+). Timestamps are unix seconds (UTC).

CREATE TABLE users (
    id                  BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email               VARCHAR(255) NOT NULL,
    name                VARCHAR(255) NOT NULL DEFAULT '',
    password_hash       VARCHAR(255) NOT NULL DEFAULT '',
    role                VARCHAR(16)  NOT NULL DEFAULT 'user',
    status              VARCHAR(16)  NOT NULL DEFAULT 'invited',
    failed_logins       INT          NOT NULL DEFAULT 0,
    locked_until        BIGINT       NULL,
    password_changed_at BIGINT       NULL,
    last_login_at       BIGINT       NULL,
    invited_by          BIGINT       NULL,
    created_at          BIGINT       NOT NULL,
    updated_at          BIGINT       NOT NULL,
    UNIQUE KEY uq_users_email (email),
    CONSTRAINT fk_users_invited_by FOREIGN KEY (invited_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE password_history (
    id            BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id       BIGINT       NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at    BIGINT       NOT NULL,
    KEY idx_password_history_user (user_id, id),
    CONSTRAINT fk_password_history_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE sessions (
    id           VARCHAR(64)  NOT NULL PRIMARY KEY,
    user_id      BIGINT       NOT NULL,
    created_at   BIGINT       NOT NULL,
    last_seen_at BIGINT       NOT NULL,
    expires_at   BIGINT       NOT NULL,
    ip           VARCHAR(64)  NOT NULL DEFAULT '',
    user_agent   VARCHAR(255) NOT NULL DEFAULT '',
    KEY idx_sessions_user (user_id),
    KEY idx_sessions_expires (expires_at),
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE user_tokens (
    id         BIGINT      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    token_hash VARCHAR(64) NOT NULL,
    user_id    BIGINT      NOT NULL,
    purpose    VARCHAR(16) NOT NULL,
    created_by BIGINT      NULL,
    created_at BIGINT      NOT NULL,
    expires_at BIGINT      NOT NULL,
    used_at    BIGINT      NULL,
    UNIQUE KEY uq_user_tokens_hash (token_hash),
    KEY idx_user_tokens_user (user_id, purpose),
    CONSTRAINT fk_user_tokens_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT fk_user_tokens_created_by FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE alert_groups (
    id          BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(255)  NOT NULL,
    description VARCHAR(2000) NOT NULL DEFAULT '',
    created_at  BIGINT        NOT NULL,
    updated_at  BIGINT        NOT NULL,
    UNIQUE KEY uq_alert_groups_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE recipients (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email      VARCHAR(255) NOT NULL,
    name       VARCHAR(255) NOT NULL DEFAULT '',
    created_at BIGINT       NOT NULL,
    updated_at BIGINT       NOT NULL,
    UNIQUE KEY uq_recipients_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE group_recipients (
    group_id     BIGINT NOT NULL,
    recipient_id BIGINT NOT NULL,
    created_at   BIGINT NOT NULL,
    PRIMARY KEY (group_id, recipient_id),
    KEY idx_group_recipients_recipient (recipient_id),
    CONSTRAINT fk_group_recipients_group FOREIGN KEY (group_id) REFERENCES alert_groups(id) ON DELETE CASCADE,
    CONSTRAINT fk_group_recipients_recipient FOREIGN KEY (recipient_id) REFERENCES recipients(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE certificates (
    id                  BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    url                 VARCHAR(2048) NOT NULL,
    host                VARCHAR(255)  NOT NULL,
    port                INT           NOT NULL,
    name                VARCHAR(255)  NOT NULL DEFAULT '',
    notes               VARCHAR(2000) NOT NULL DEFAULT '',
    enabled             TINYINT(1)    NOT NULL DEFAULT 1,
    created_by          BIGINT        NULL,
    created_at          BIGINT        NOT NULL,
    updated_at          BIGINT        NOT NULL,
    last_checked_at     BIGINT        NULL,
    last_success_at     BIGINT        NULL,
    last_error          VARCHAR(1000) NOT NULL DEFAULT '',
    subject_cn          VARCHAR(255)  NOT NULL DEFAULT '',
    subject_org         VARCHAR(255)  NOT NULL DEFAULT '',
    issuer_cn           VARCHAR(255)  NOT NULL DEFAULT '',
    issuer_org          VARCHAR(255)  NOT NULL DEFAULT '',
    sans                MEDIUMTEXT    NULL,
    serial_number       VARCHAR(128)  NOT NULL DEFAULT '',
    fingerprint_sha256  VARCHAR(64)   NOT NULL DEFAULT '',
    signature_algorithm VARCHAR(64)   NOT NULL DEFAULT '',
    key_algorithm       VARCHAR(32)   NOT NULL DEFAULT '',
    key_bits            INT           NOT NULL DEFAULT 0,
    not_before          BIGINT        NULL,
    not_after           BIGINT        NULL,
    trusted             TINYINT(1)    NOT NULL DEFAULT 0,
    verify_error        VARCHAR(1000) NOT NULL DEFAULT '',
    tls_version         VARCHAR(16)   NOT NULL DEFAULT '',
    resolved_ip         VARCHAR(64)   NOT NULL DEFAULT '',
    chain               MEDIUMTEXT    NULL,
    UNIQUE KEY uq_certificates_host_port (host, port),
    KEY idx_certificates_not_after (not_after),
    CONSTRAINT fk_certificates_created_by FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE group_certificates (
    group_id       BIGINT NOT NULL,
    certificate_id BIGINT NOT NULL,
    created_by     BIGINT NULL,
    created_at     BIGINT NOT NULL,
    PRIMARY KEY (group_id, certificate_id),
    KEY idx_group_certificates_cert (certificate_id),
    CONSTRAINT fk_group_certificates_group FOREIGN KEY (group_id) REFERENCES alert_groups(id) ON DELETE CASCADE,
    CONSTRAINT fk_group_certificates_cert FOREIGN KEY (certificate_id) REFERENCES certificates(id) ON DELETE CASCADE,
    CONSTRAINT fk_group_certificates_created_by FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE notifications (
    id              BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    group_id        BIGINT        NULL,
    certificate_id  BIGINT        NULL,
    recipient_id    BIGINT        NULL,
    group_name      VARCHAR(255)  NOT NULL DEFAULT '',
    certificate_url VARCHAR(2048) NOT NULL DEFAULT '',
    recipient_email VARCHAR(255)  NOT NULL DEFAULT '',
    not_after       BIGINT        NOT NULL,
    threshold_days  INT           NOT NULL,
    days_left       INT           NOT NULL,
    status          VARCHAR(16)   NOT NULL DEFAULT 'pending',
    attempts        INT           NOT NULL DEFAULT 0,
    last_error      VARCHAR(1000) NOT NULL DEFAULT '',
    next_attempt_at BIGINT        NOT NULL,
    created_at      BIGINT        NOT NULL,
    sent_at         BIGINT        NULL,
    UNIQUE KEY uq_notifications_key (group_id, certificate_id, recipient_id, not_after, threshold_days),
    KEY idx_notifications_status (status, next_attempt_at),
    KEY idx_notifications_created (created_at),
    KEY idx_notifications_certificate (certificate_id),
    KEY idx_notifications_recipient (recipient_id),
    CONSTRAINT fk_notifications_group FOREIGN KEY (group_id) REFERENCES alert_groups(id) ON DELETE SET NULL,
    CONSTRAINT fk_notifications_cert FOREIGN KEY (certificate_id) REFERENCES certificates(id) ON DELETE SET NULL,
    CONSTRAINT fk_notifications_recipient FOREIGN KEY (recipient_id) REFERENCES recipients(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE settings (
    name       VARCHAR(64) NOT NULL PRIMARY KEY,
    value      MEDIUMTEXT  NOT NULL,
    updated_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE audit_log (
    id          BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id     BIGINT        NULL,
    user_email  VARCHAR(255)  NOT NULL DEFAULT '',
    action      VARCHAR(64)   NOT NULL,
    entity_type VARCHAR(32)   NOT NULL DEFAULT '',
    entity_id   BIGINT        NULL,
    details     VARCHAR(2000) NOT NULL DEFAULT '',
    ip          VARCHAR(64)   NOT NULL DEFAULT '',
    created_at  BIGINT        NOT NULL,
    KEY idx_audit_log_created (created_at),
    KEY idx_audit_log_user (user_id),
    CONSTRAINT fk_audit_log_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE app_locks (
    name       VARCHAR(64) NOT NULL PRIMARY KEY,
    owner      VARCHAR(64) NOT NULL,
    expires_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
