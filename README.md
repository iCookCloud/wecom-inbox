# WeCom Inbox

**Receive once. Keep the original. Choose what happens next.**

A self-hosted business inbox for messages and files delivered to a WeCom smart bot. Search your records, download originals, export JSON/CSV, and connect independent processing services. **No AI account or agent framework is required.**

[中文说明](README.zh-CN.md) · [API and integrations](docs/api.md) · [Deployment and recovery](docs/deployment.md) · [Implementation and verification](docs/design.md)

New to the project? The Chinese guide provides separate paths for [an existing Bot](README.zh-CN.md#with-bot),
[trying the inbox without a Bot](README.zh-CN.md#without-bot), and [creating a new Bot](README.zh-CN.md#create-bot).
These are configurations of the same application. Without a Bot, imported records remain usable, but live WeCom intake and replies are unavailable.

> **v0.1.0 release candidate — private acceptance in progress.** Automated regression tests and local browser checks are available. Real-WeCom acceptance has passed for direct chats, group @-mentions, quoted files and media; reconnect behavior and independent first-time setup are still pending. Verified platform limits: voice arrives as transcribed text only, merged chat records carry no content, quotes of expired files carry no original, and each attachment is capped at 50 MB ([acceptance record](docs/design.md#真实平台验收记录2026-10-24)). This is an independent community project, not an official Tencent product. It archives only callbacks delivered to your Bot, not all conversations in an organization or arbitrary personal WeChat chats.

![WeCom Inbox](docs/assets/inbox-desktop.png)

Web inbox with explicitly synthetic test fixtures; no customer data.

## What it does

- Multiple Bots with explicit sender/chat allowlists and independent connections.
- Durable intake before attachment download; encrypted temporary download credentials.
- Original attachments with SHA-256 integrity checks, separate records for multiple and quoted files, and crash recovery.
- English/Chinese web inbox, filename/body search, filters, stable pagination, downloads and JSON/CSV export.
- Scoped consumer tokens; signed webhooks with independent retries, failed-event replay and idempotent result recording.
- Optional acknowledged replies to the source conversation. Uncertain sends remain visible and are not blindly retried.
- Read-only import of legacy JSONL records, consistent offline backup and restore to a new directory.

The gateway stores the records; your scripts, document tools or agents decide how to process them. An acknowledged webhook and a completed business task are separate states.

## Quick start

Build with **Go 1.26.4 or later** on Linux or macOS:

```sh
git clone https://github.com/iCookCloud/wecom-inbox.git
cd wecom-inbox
make build
./wecom-inbox init
./wecom-inbox check
./wecom-inbox serve
```

Open **http://127.0.0.1:8787** and sign in using `admin_token` in the newly generated private `config.toml`. `init` refuses to overwrite an existing file. An empty inbox works without any Bot or model configured.

Add your Bot to `config.toml` using the settings in [config.example.toml](config.example.toml). Keep the generated random admin token. Configure `id`, `secret`, `enabled = true`, and explicit `allowed_users` or `allowed_chats`. A valid configuration change reloads automatically; changes to the data directory or listening address require a restart.

Use a dedicated test Bot for the first run. A Bot permits only one active connection: do not run another gateway for the same Bot. In groups, follow the platform's mention/quoted-message trigger rules. Platform voice callbacks may provide a transcript without an audio file.

## Try it without an AI or WeCom account

The fixture below is explicitly synthetic and includes one intentionally missing attachment:

```sh
./wecom-inbox import --file tests/fixtures/legacy/messages.jsonl \
  --media-root tests/fixtures/legacy
# Reports 4 imported records and 1 failed attachment; exit 1 is intentional.
./wecom-inbox serve
```

This exercises the real database, archive and web UI. It does **not** prove real WeCom transport behavior. Do not import test fixtures into your production inbox.

## Connect a plain program

Configure a `[[consumers]]` entry with its own token and Bot scopes. Set a URL and signing secret for push delivery, or omit them for a pull-only integration. `allow_reply` defaults to false. The administrator identity `admin` is reserved.

```sh
# INBOX_TOKEN is a scoped consumer token, not the administrator token.
curl -H "Authorization: Bearer $INBOX_TOKEN" \
  'http://127.0.0.1:8787/v1/messages?q=quote'
```

The [standard-library Python example](examples/http-consumer/consumer.py) verifies webhook signatures, downloads and checks archived files, and records results without calling a model. Agent-specific adapters use the same API; no core changes or access to Bot secrets are required. No proprietary Agent adapter is claimed as tested in this release.

## Run and maintain

- `config.toml`: Bot secrets and consumer configuration; private file, never committed.
- `data/inbox.db`: archive, pending work, delivery state and results.
- `data/encryption.key`: key for pending media credentials; back it up with the database.
- `data/files/`: original file bytes and recovery manifests; not automatically deleted.

The executable uses a data-directory lock. Offline maintenance commands require the server to be stopped; use HTTP queries and exports while it is running. Keep the original legacy gateway configuration and data until migration has been verified; this release is not a drop-in replacement for its Bot-management CLI.

See [deployment and recovery](docs/deployment.md) for TLS, systemd, backup, restore, import and operational limits. Archived records are not encrypted at rest; use host/disk protections appropriate to your data.

## Development

```sh
make test
make check                 # go vet and race-enabled regression tests
node --check web/app.js
make release VERSION=v0.1.0
```

Tests live under `tests/` and use synthetic data and local test transports. CI builds without CGO. Source browsing, test outcomes and live-platform acceptance are reported separately in [the plan](docs/design.md).

## License

MIT. See [LICENSE](LICENSE), [third-party notices](THIRD_PARTY_NOTICES.md), [contribution guide](CONTRIBUTING.md) and [security notes](SECURITY.md).
