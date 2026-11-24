-- Messenger channels (Telegram chats, Slack channels, webhooks). A group
-- sends its alerts to its recipients (e-mail) and to its channels.

CREATE TABLE channels (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    type       VARCHAR(32)  NOT NULL,
    name       VARCHAR(100) NOT NULL,
    enabled    TINYINT(1)   NOT NULL DEFAULT 1,
    config     MEDIUMTEXT   NOT NULL,
    created_at BIGINT       NOT NULL,
    updated_at BIGINT       NOT NULL,
    UNIQUE KEY uq_channels_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE group_channels (
    group_id   BIGINT NOT NULL,
    channel_id BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (group_id, channel_id),
    KEY idx_group_channels_channel (channel_id),
    CONSTRAINT fk_group_channels_group FOREIGN KEY (group_id) REFERENCES alert_groups(id) ON DELETE CASCADE,
    CONSTRAINT fk_group_channels_channel FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- A notification targets either a recipient (e-mail) or a channel.
ALTER TABLE notifications
    ADD COLUMN channel_id BIGINT NULL,
    ADD COLUMN channel_type VARCHAR(32) NOT NULL DEFAULT '',
    ADD UNIQUE KEY uq_notifications_channel_key (group_id, certificate_id, channel_id, not_after, threshold_days),
    ADD KEY idx_notifications_channel (channel_id),
    ADD CONSTRAINT fk_notifications_channel FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE SET NULL;

-- The mail journal becomes the delivery journal of every channel type.
ALTER TABLE mail_log
    ADD COLUMN channel VARCHAR(32) NOT NULL DEFAULT 'email',
    ADD KEY idx_mail_log_channel (channel);
