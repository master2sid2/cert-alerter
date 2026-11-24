-- Messenger channels (Telegram chats, Slack channels, webhooks). A group
-- sends its alerts to its recipients (e-mail) and to its channels.

CREATE TABLE channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT    NOT NULL,
    name       TEXT    NOT NULL UNIQUE,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT    NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE group_channels (
    group_id   INTEGER NOT NULL REFERENCES alert_groups(id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (group_id, channel_id)
);
CREATE INDEX idx_group_channels_channel ON group_channels(channel_id);

-- A notification targets either a recipient (e-mail) or a channel.
ALTER TABLE notifications ADD COLUMN channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL;
ALTER TABLE notifications ADD COLUMN channel_type TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX uq_notifications_channel_key ON notifications(group_id, certificate_id, channel_id, not_after, threshold_days);
CREATE INDEX idx_notifications_channel ON notifications(channel_id);

-- The mail journal becomes the delivery journal of every channel type.
ALTER TABLE mail_log ADD COLUMN channel TEXT NOT NULL DEFAULT 'email';
CREATE INDEX idx_mail_log_channel ON mail_log(channel);
