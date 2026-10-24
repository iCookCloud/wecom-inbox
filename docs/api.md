# HTTP API v1

All `/v1` routes require `Authorization: Bearer <token>` or an administrator browser session. Consumer tokens are scoped to their configured source IDs (the `bots` configuration field is retained for compatibility). Unknown or out-of-scope records return 404. Browser writes require the session CSRF token and a matching origin; API consumers use Bearer tokens.

Credentials belong in private configuration. Responses never contain Bot secrets, signing secrets, download encryption keys or internal absolute file paths.

| Method and path | Behavior |
|---|---|
| `GET /healthz` | Database health only; no records or credentials |
| `GET /v1/session` | Current administrator/consumer identity; browser CSRF token |
| `GET /v1/bots` | Authorized configured sources and states; `kind` is `wecom_bot` or `wechat_kf` |
| `GET /v1/bots/{id}/sessions` | Administrator-only: the Bot's recent conversations it may message (up to 20, platform order) with name, type and last message time. Uses the WeCom CLI gateway with the Bot ID/secret; requires the "send message" grant in the Bot's 可使用权限, which expires about every 7 days. Upstream failures return 502 with the platform errcode |
| `GET /v1/stats` | Authorized record and attachment counts |
| `GET /v1/consumers` | Administrator-only consumer scopes; credentials omitted |
| `GET /v1/messages` | Cursor-paginated records |
| `GET /v1/messages/{id}` | Message, attachments, delivery state and results; consumers see only their own processing records |
| `GET /v1/attachments/{id}` | Hash-verified file download; integrity failure is 409 |
| `GET /v1/export?format=json\|csv` | All records matching the filters, from a stable initial ID boundary |
| `POST /v1/messages/{id}/results` | Idempotent processing result and optional queued source-chat reply |
| `POST /v1/deliveries/{id}/retry` | Replay a failed/delivered event with the same event ID |

Message filters: `q` (substring of message/quote/filename, including Chinese), `bot_id`, `sender_id`, `type`, `status`, `from`, `to`, `limit` (1–200, default 50), `cursor`. Timestamps use RFC3339. `status=attention` includes partial/failed archives. Use returned `next_cursor` with the same filters. New arrivals do not shift existing pages. Search is a bounded-result substring scan; this release does not claim a large-scale full-text index.

```json
{"messages":[],"next_cursor":"100:51"}
```

`next_cursor` is omitted on the final page. Message statuses are `pending`, `importing` (offline import awaiting completion), `ready`, `partial`, `failed`. An attachment has an opaque ID, original name, kind, size, SHA-256 and its own status. A failed attachment remains visible.

## Signed Webhook

The consumer URL receives JSON:

```json
{"schema_version":1,"event_id":"opaque-stable-id","type":"message.archived","message":{"id":123,"bot_id":"your-bot","attachments":[]}}
```

The complete message shape is the same as the read API. Headers:

- `X-WeCom-Inbox-Event`: stable event ID, also in the body.
- `X-WeCom-Inbox-Timestamp`: Unix seconds.
- `X-WeCom-Inbox-Signature`: `sha256=` followed by hex HMAC-SHA256 of `timestamp + "." + raw_request_body`, keyed by the consumer signing secret.

Verify the HMAC with a constant-time comparison, require a recent timestamp, and deduplicate event IDs. A 2xx response acknowledges receipt. Each consumer has independent delivery state; up to five attempts use bounded exponential backoff. Replaying keeps the same event ID. HTTP delivery is at-least-once, never exactly-once. Redirects are refused. HTTP/private network destinations require explicit `allow_private_network=true`.

A newly configured consumer does not automatically receive old records. It can use the read API to process history. Imported legacy records never automatically enqueue webhooks or replies.

## Processing result

```json
{
  "idempotency_key":"stable-job-id",
  "status":"completed",
  "summary":"Verified the attachment and imported two items.",
  "reply":false
}
```

`status` is `completed` or `failed`; the key is 1–128 bytes and the summary is at most 4000 bytes. Scope: message + consumer + key. An identical retry returns the original result (200); a new result is 201; a conflicting reuse is 409.

`reply=true` needs `allow_reply=true` and a nonempty summary. It queues a text/Markdown message to the already-recorded source conversation, never an arbitrary destination. Current Bot and consumer permissions are checked again before sending. Revoked actions are cancelled. For smart bots, the platform's acknowledgement determines `delivered`; a missing acknowledgement remains `uncertain`, without automatic resend. Customer-service sends use `accepted` until an asynchronous failure is observed; API acceptance does not confirm final delivery. A delivery being pending does not mean the platform received it.

## Error shape

```json
{"error":{"code":"unauthorized","message":"sign in or supply a valid Bearer token"}}
```

HTTP 400: invalid input; 401: missing/invalid identity; 403: permission/CSRF failure; 404: unknown or out-of-scope record; 409: pending/integrity/idempotency conflict; 429: login rate limit; 500/503: storage or service failure.


## Customer-service sources

Customer-service source IDs are `kf:<corp_id>:<open_kfid>`. These immutable identifiers are used in `bot_id`, consumer `bots` scopes, filters and `/v1/bots`; existing smart-bot IDs do not change. Source metadata includes `kind`. Customer-service states include `connecting`, `listening` (last API sync succeeded), `sync_failed` and `disabled`. `listening` alone does not verify callback reachability or real-user delivery. The Bot CLI conversation-list endpoint is not supported for these sources.

Customer messages have `chat_type="wechat_kf"`, `sender_id`/`chat_id` equal to the customer's `external_userid`, and `platform_at` from `send_time`. Human-servicer messages have `chat_type="wechat_kf_event"`, sender equal to the servicer's platform ID and chat equal to the customer ID. The latter cannot be replied to and never automatically trigger consumers, preventing automated reply loops. Do not use a Bot-only `chat_type` enum for a consumer configured to read customer-service sources.

Voice attachments here contain the platform-provided recording. Merged records are flattened with sender/time labels and individual media. Structured cards retain the supplied JSON fields as searchable text; links are never automatically fetched. Unsupported/incomplete types are visible failures; raw evidence remains encrypted in storage and is not included in API responses. Event-only platform notifications are not exposed as inbound messages.

`reply=true` for customer service has a 2048-byte summary limit. A result with `reply=false` retains the normal 4000-byte limit. Reply states add `accepted`; later asynchronous platform failures update it to `failed`. An expired 48-hour window, rejected API request or exhausted platform allowance is a visible failure. This version sends text results only and does not transfer human-managed sessions or send arbitrary outbound files.

`GET/POST /callbacks/wechat-kf/{connection_id}` is the only public customer-service endpoint. It authenticates Tencent signatures and encrypted recipient identity rather than accepting a Bearer token. GET verifies the endpoint; POST persists a sync notification and returns `success`. Invalid signatures/receivers return 403, invalid payloads 400, missing/disabled connections 404 and failed persistence 503. It never accepts user-authored archive records.
