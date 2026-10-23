# Security

Use GitHub private vulnerability reporting if available; otherwise request a private contact in an issue without describing an exploit or including secrets.

Default listener: loopback. For remote use, configure an HTTPS reverse proxy and `public_url`. The admin token grants all records; consumers get independent Bot scopes and optional reply permission. Do not share the admin token with consumers.

Pending download URLs and AES keys are encrypted in SQLite with `data/encryption.key`; completed records do not retain them. Archived messages and original files are not encrypted at rest. Use disk encryption and protect the whole data directory and backups. Keep the key with its database when backing up.

A consumer with `allow_private_network=true` can deliver to internal addresses. Use only trusted, administrator-controlled destinations. Otherwise private IPs and redirects are blocked at connection time. No public inbound WeCom webhook endpoint is exposed.

This is not enterprise-wide conversation archiving. Only callbacks received by the configured Bot are retained. Review your organization's data-handling and retention rules before enabling it. No automatic record deletion is included in this release.
