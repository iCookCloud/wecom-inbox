# Deployment and recovery

## Installation

Build with `make build`, or download the binary and `SHA256SUMS` from a published release. Linux amd64/arm64 and macOS arm64 are build targets. macOS is cross-compiled but requires separate real-host acceptance; Windows is not supported in this version.

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

The source file and source media are unchanged. Import never follows references outside `media-root`, never downloads old `raw` URLs, and never creates automatic deliveries or replies. The same completed record can be imported again without duplication. An interrupted import stays `importing`; rerun the same input to resume. Missing attachments or invalid lines are explicitly reported and produce exit 1. Inspect those records rather than assuming a complete migration.

Validate record counts, original timestamps and file hashes before retiring any old system. Legacy Bot-management commands are not included; configure Bots in the new private TOML file.

## Practical limits

- Single host and one ingest worker; SQLite serializes database access.
- Callback body up to 1 MiB; message text/quote up to 512 KiB; up to 20 attachments.
- Default per-file limit 50 MiB, configurable from 1–50; aggregate attachments up to 100 MiB per message.
- Three media-download attempts while links remain usable. Expired links cannot be recreated; request a new upload.
- Five Webhook delivery attempts; failed events can be replayed by an authorized operator.
- Result summary up to 4000 bytes. A platform acknowledgement timeout means uncertain delivery, not success.
- Search scans normalized fields with result pagination; benchmark your own data before large deployments.
- No automatic data retention/deletion or cloud replication. Monitor free disk space and back up regularly.

Health reports a database probe; it does not certify Bot connection health or a recipient's view. Inspect `/v1/bots`, delivery status and real client receipts for those checks.
