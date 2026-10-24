# Security

Report vulnerabilities through [GitHub private vulnerability reporting](https://github.com/iCookCloud/wecom-inbox/security/advisories/new). If that entry is unavailable, request a private contact in an issue without describing the exploit, including customer data or exposing secrets.

Default listener: loopback. For remote use, configure an HTTPS reverse proxy and `public_url`. The admin token grants all records; consumers get independent source scopes and optional reply permission. Do not share the admin token with consumers.

Pending download URLs, media IDs, raw customer-service payloads and notification tokens are encrypted in SQLite with `data/encryption.key`. Successful archive completion clears pending message payloads; unsupported customer-service records retain encrypted evidence, and sync state retains an encrypted notification token. Access tokens are cached in memory. Archived messages and original files are not encrypted at rest. Use disk encryption and protect the whole data directory and backups. Keep the key with its database when backing up.

A consumer with `allow_private_network=true` can deliver to internal addresses. Use only trusted, administrator-controlled destinations. Otherwise private IPs and redirects are blocked at connection time. WeCom smart bots connect outbound over WebSocket. WeChat customer service additionally exposes `GET/POST /callbacks/wechat-kf/{connection_id}` through an HTTPS reverse proxy. That public endpoint verifies the platform signature, timestamp, AES payload and recipient CorpID; it does not use an administrator token. Restrict proxy exposure to that path when the inbox UI should remain private.

This is not enterprise-wide conversation archiving. Only messages delivered through configured Bots or customer-service accounts are retained; other personal WeChat chats and arbitrary groups are inaccessible. Review your organization's data-handling and retention rules before enabling it. No automatic record deletion is included in this release.
