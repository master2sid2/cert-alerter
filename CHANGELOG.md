# Changelog

All notable changes to this project are documented here. Versions follow
[semantic versioning](https://semver.org).

## [v0.2.0] - 2026-11-24

- Messenger channels: Telegram, Slack (also Mattermost, Rocket.Chat, Discord) and a generic JSON
  webhook. Channels are attached to groups and receive one message per group alert; messengers
  are turned on and off in "Settings → Messengers"; tokens and webhook URLs are stored encrypted.
- Group test notifications also go to the group's channels.
- The mail log became the delivery log (e-mails and messenger messages) with a channel filter;
  the notification history shows and filters channels too.

## [v0.1.0] - 2026-11-21

First release.

- Monitoring of SSL/TLS certificates by URL or `host:port` (HTTPS, LDAPS, SMTPS, IDN, IPv6),
  with chain, trust and host name verification.
- Alerts strictly per group at 30, 14, 7 and 3 days before expiry (configurable), plus an
  optional "expired" stage; one digest e-mail per recipient and group.
- SMTP with STARTTLS or implicit TLS and authentication; the SMTP password is stored encrypted.
- Mail log: every sent e-mail with recipient, time and result; CSV export.
- Dashboard with the expiry horizon, upcoming expirations, problems and group health.
- Multiple users with e-mail invitations, admin and user roles, configurable password policy,
  lockout and session limits, audit log.
- SQLite by default, MySQL/MariaDB supported, `db-copy` to move the data.
- Separate application and HTTP access logs, each to stdout/stderr or its own file, reopened on SIGHUP.
- systemd unit, `/etc/default/cert-alerter` and `make install`; Docker image.
