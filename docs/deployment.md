# Deployment and recovery

## Installation

Build with `make build`, or download the binary and `SHA256SUMS` from a published release. Linux amd64/arm64 and macOS arm64 are build targets. Native CLI installation, local HTTP startup, Go/race tests and backup/restore have passed on a macOS 15 arm64 runner. Real WeCom acceptance and first-time human desktop onboarding remain separate gates; Windows is not supported in this version.

`install.sh <binary> <SHA256SUMS>` verifies the selected asset and installs it into `~/.local/bin`. `INBOX_INSTALL_DIR` selects another directory. It refuses to overwrite an existing executable; keep old releases when planning upgrades.

Run `wecom-inbox init -c /path/to/config.toml`. The generated file is mode 0600 and contains a random administrator token. Configuration-relative `data_dir` is resolved relative to that configuration file, not the working directory. `WECOM_INBOX_CONFIG` chooses the default config file; `WECOM_INBOX_ADMIN_TOKEN` can override the administrator token. The old `WECOM_GW_CONFIG` variable is accepted as a migration convenience.

## Service and TLS

Run as a dedicated OS user. A systemd template is in `deploy/wecom-inbox.service`; adjust paths and provision its data directory and config file for that user. Use loopback with an HTTPS reverse proxy for remote access. Set `public_url` to its exact HTTPS origin so browser cookies are Secure and origin checks are correct. Do not expose the administrator login over plain HTTP.

Bot secrets are used only by the transport. Each Bot needs an explicit `allowed_users` and/or `allowed_chats` list; `"*"` is an intentional allow-all choice, not the default. User IDs are WeCom platform IDs. Valid config edits reload; invalid changes retain the old config. Changes to `listen` and `data_dir` require restart.

Only one process can own a data directory. Only one gateway should connect to a given Bot across hosts. An authentication failure stays visible and does not reconnect indefinitely; correct the secret to restart that connection.

## Backup and restore

Stop the service for offline maintenance. Backups contain private messages and the encryption key.

```sh
wecom-inbox backup -c /path/config.toml --out /backups/inbox-2026-10-23
wecom-inbox restore --from /backups/inbox-2026-10-23 --out /srv/inbox-restored
```

Both destinations must be new directories. Backup takes a SQLite snapshot, copies referenced original and recovery files, checks files against catalog hashes and writes a checksum manifest. Restore verifies copied hashes and database references. Existing directories are never overwritten. A failed backup/restore directory is incomplete; do not run it as a service. Keep it for diagnosis or remove it yourself only when appropriate.

Restore configuration separately, point `data_dir` to the restored directory, run `check`, then start a single service. The key must travel with the database. Missing/wrong keys and unsupported schema versions fail closed.

## Legacy import

```sh
wecom-inbox import -c /path/config.toml \
  --file /old-data/bot/messages/2026-07-31.jsonl --media-root /old-data
```

The source file and source media are unchanged. Import never follows references outside `media-root`, never downloads old `raw` URLs, and never creates automatic deliveries or replies. The same completed record can be imported again without duplication. An interrupted import stays `importing`; rerun the same input to resume. Missing attachments or invalid lines are explicitly reported and produce exit 1, including when an already-incomplete record is imported again. Completed partial imports retain their prior failure status; restoring missing source files does not silently overwrite the archive. Inspect those records rather than assuming a complete migration.

Validate record counts, original timestamps and file hashes before retiring any old system. Legacy Bot-management commands are not included; configure Bots in the new private TOML file.

## Practical limits

- Single host and one ingest worker; SQLite serializes database access.
- Unsupported message or mixed-part types are explicitly rejected without a success receipt.
- Callback body up to 1 MiB; message text/quote up to 512 KiB; up to 20 attachments.
- Default per-file limit 50 MiB, configurable from 1–50; aggregate attachments up to 100 MiB per message.
- Three media-download attempts while links remain usable. Expired links cannot be recreated; request a new upload.
- Five Webhook delivery attempts; failed events can be replayed by an authorized operator.
- Result summary up to 4000 bytes. A platform acknowledgement timeout means uncertain delivery, not success.
- Search scans normalized fields with result pagination; benchmark your own data before large deployments.
- No automatic data retention/deletion or cloud replication. Monitor free disk space and back up regularly.

Health reports a database probe; it does not certify Bot connection health or a recipient's view. Inspect `/v1/bots`, delivery status and real client receipts for those checks.


<a id="wechat-customer-service"></a>

## WeChat customer service

This connector uses **enterprise-managed WeChat customer service** at `qyapi.weixin.qq.com`. It does not use the standalone customer-service API. Ordinary WeChat users contact a customer-service account through its official link; they do not need to install WeCom. This does not grant access to their other chats or groups.

Provision an enabled custom application, add it to WeCom customer service's **可调用接口的应用**, grant it management of the selected customer-service accounts and put required servicers in its visible scope. Follow the console's trusted-IP/domain requirements. New tenants must not rely on the legacy customer-service system-application secret; the official API stopped granting that path to new tenants in December 2023.

The private configuration uses one `[[wechat_kf]]` connection per authorized application and nested `[[wechat_kf.accounts]]` records. See `config.example.toml` for exact fields. `corp_id` and `secret` identify the application; `token` and `encoding_aes_key` authenticate/decrypt callbacks. The latter must be exactly 43 Base64 characters decoding to 32 bytes. Each account needs an explicit `allowed_customers` list; `["*"]` intentionally accepts everyone who contacts that customer-service account. No Bot is required.

Before enabling the connection, `wecom-inbox kf-accounts -c /private/config.toml --connection customer-service` can list authorized account IDs with a disabled connection. After configuring an account, `wecom-inbox kf-link -c /private/config.toml --bot 'kf:CORP_ID:OPEN_KFID'` obtains its official contact URL. Neither command prints access tokens or secrets. Both contact Tencent; `check` remains offline.

### Public callback, private inbox

Run `serve` before saving the callback in the WeCom console. Configure the same Token and EncodingAESKey on both sides and use:

`https://callback.example.com/callbacks/wechat-kf/customer-service`

The last component is the configured connection `id`. The handler verifies the SHA-1 signature, timestamp, AES payload and recipient CorpID, responds to GET verification with the exact decrypted challenge, and persists POST notifications before acknowledging them. No administrator or consumer token belongs in this URL. All other `/v1` routes retain their existing authentication.

For a callback-only Nginx host inside an already configured TLS `server` block:

```nginx
location /callbacks/wechat-kf/ {
    client_max_body_size 1m;
    proxy_pass http://127.0.0.1:8787;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
}
location / { return 404; }
```

Obtain and configure the domain/TLS certificate separately. Callback verification is GET; notifications are POST, so allow both. Keep the management UI private, or use the normal full-site HTTPS configuration and set `public_url` to that site's exact origin. `public_url` is a browser-origin setting; it need not point to a callback-only host. Keep system time synchronized because signed notifications have a time window.

### Recovery and platform limits

- Every account has its own durable cursor and encrypted short-lived notification token. Accounts are scheduled in rotation so one busy account does not starve another. New callbacks cannot be overwritten by an in-flight sync.
- Messages are committed before advancing a page cursor. A crash between these writes replays the page safely through message deduplication. Empty pages with `has_more=1` are followed.
- A 60-second fallback pull reduces dependence on callbacks; failures back off. Tencent restricts calls without a fresh notification token, so callbacks remain the primary delivery signal. The API only exposes the last three days: initial setup may ingest that interval; outages beyond it can cause an unrecoverable gap.
- Media IDs are kept only in encrypted pending records; fresh access tokens are obtained in memory for download. API URLs are fixed to Tencent and redirects are refused. Callback bodies and JSON responses have size bounds; the normal archive limits still apply.
- Unsupported messages have `failed` status, encrypted raw payload and a visible explanation. Customer-servicer messages are archived as `wechat_kf_event` and do not enqueue Webhooks or automated replies. Non-message system events are used for delivery reconciliation, not shown as conversation content. Invalid or mismatched account pages do not advance the cursor.
- Removing a customer from `allowed_customers` prevents new intake, pending media downloads and queued replies. Previously accepted text/history remains available to authorized consumers; removing admission permission does not delete records or revoke an existing consumer's read scope. Revoke the consumer scope separately if needed.
- Text replies are at most 2048 UTF-8 bytes. Receipt messages count toward Tencent's reply allowance and default off. Session state, the 48-hour window and the five-message allowance are enforced by the platform; no session takeover or automatic message splitting is attempted.
- Successful sends are `accepted`. A later `msg_send_fail` event changes the associated reply to `failed`, even if it arrives before the synchronous API response. Connection ambiguity stays `uncertain`; restart never blindly resends it. Neither state proves what a person saw in WeChat.
- Backups contain the cursor and outbound correlation tables along with messages and files. Stop the process before the normal backup procedure. The additive tables use the existing SQLite schema lifecycle; do not run multiple workers against the same account or data directory.

The initial public release includes this connector with local protocol tests; live customer-service acceptance remains pending. Complete the account-based checklist in [the Chinese README](../README.zh-CN.md#wechat-customer-service) before relying on it for your own live workflow.

Official contracts: [receive and sync](https://developer.work.weixin.qq.com/document/path/94670), [send restrictions](https://developer.work.weixin.qq.com/document/path/94677), [callback validation](https://developer.work.weixin.qq.com/document/path/90930), [download media](https://developer.work.weixin.qq.com/document/path/90254), [account IDs](https://developer.work.weixin.qq.com/document/path/94661), [contact links](https://developer.work.weixin.qq.com/document/path/94665).
