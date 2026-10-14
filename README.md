# TakemYapi — Freshservice data fetcher

Data-fetcher module that extracts **every ticket plus full conversation
history** from a Freshservice (Freshworks) account and exports it as JSON and
CSV so it can be migrated into a new ITSM tool.

## What it does

1. Pages through all tickets via the Freshservice v2 API
   (`GET /api/v2/tickets`), following `Link`/`rel="next"` pagination headers.
2. Fetches the complete conversation history (replies + notes) for each ticket
   with a bounded worker pool (`GET /api/v2/tickets/{id}/conversations`).
3. Downloads every attachment (ticket-level, inside replies/notes,
   inside service-catalog requested items, plus inline `<img>` files
   embedded in descriptions/bodies) into
   `output/attachments/<ticket_id>/`, records the local path back on the
   attachment metadata (so the ticket JSON/CSV references the saved file), and
   skips files already present so re-runs resume cleanly.
4. Writes the result to an output directory as:
   - `json/tickets.json` — combined array of tickets with embedded `conversations`
   - `json/tickets/<id>.json` — one file per ticket (disable with `-split-json=false`)
   - `csv/tickets.csv` — flattened ticket rows
   - `csv/conversations.csv` — one row per reply/note
   - `csv/attachments.csv` — attachment index (ticket, conversation, local saved path, source: `ticket` / `conversation` / `requested_item` / `inline_ticket` / `inline_conversation`)
   - `attachments/<ticket_id>/<attachment_id>_<name>` — downloaded attachment binaries
   - `attachments/<ticket_id>/inline_<id>_<name>` — downloaded inline `<img>` files
   - `meta.json` — run summary (counts, timestamps, API request count, filters)

API specifics handled automatically:

- Basic-auth with API key
- Rate limiting (Freshservice v2 allows 50 req/min)
- Exponential backoff + retry on `429` and `5xx`

## Usage

```sh
go run ./cmd/fetcher \
  -domain yourcompany \
  -api-key [token] \
  -out ./output
```

Configuration can also come from the environment:

## Config

Values are read from a `.env` file in the project folder, then environment
variables, then command-line flags (flags win). See `.env.example`:

```
FRESHSERVICE_DOMAIN=sanghvi.freshservice.com
FRESHSERVICE_API_KEY=your_real_api_key
```

> Keep `.env` private — it contains your API key (already in `.gitignore`).

| Flag             | Env var                | Default   | Description                                  |
| ---------------- | ---------------------- | --------- | -------------------------------------------- |
| `-domain`        | `FRESHSERVICE_DOMAIN`  | —         | Account slug (`<slug>.freshservice.com`) or full custom portal hostname |
| `-api-key`       | `FRESHSERVICE_API_KEY` | —         | API key                                      |
| `-out`           | `FETCHER_OUT_DIR`      | `./output`| Output directory                             |
| `-status`        | —                      | —         | Numeric status filter (`4`=Resolved, `5`=Closed, …) |
| `-updated-since` | —                      | —         | Only tickets updated at/after an RFC3339 timestamp |
| `-concurrency`   | —                      | `5`       | Workers fetching conversations + attachments |
| `-split-json`    | —                      | `true`    | Write per-ticket JSON files too              |
| `-attachments`   | —                      | `true`    | Download attachment binaries (disable with `-attachments=false`) |
| `-limit`         | —                      | `0`       | Max tickets to fetch (0 = all)               |
| `-ticket`        | —                      | `0`       | Fetch a single ticket ID (for testing)       |

### Filtering a migration window

For a full migration, fetch everything (use your portal hostname or account slug):

```sh
go run ./cmd/fetcher -domain servicedesk.sanghviglobal.com -api-key $FS_API_KEY
```

To re-run only recent changes (e.g. incremental sync), use `-updated-since`:

```sh
go run ./cmd/fetcher -domain servicedesk.sanghviglobal.com -api-key $FS_API_KEY \
  -updated-since 2026-01-01T00:00:00Z
```

## Project layout

```
cmd/fetcher/            CLI entrypoint (config flags, orchestration)
internal/config/        Flag/env parsing
internal/freshservice/  API client: client.go (transport) + types.go (models)
internal/exporter/      JSON + CSV writers
```

## Resumable runs

Re-running the fetcher skips tickets already exported. It uses the per-ticket
files in `json/tickets/` as the "already fetched" marker:

- previously fetched tickets are reloaded from disk and written back into the
  combined JSON/CSV **without** re-querying their conversations or re-downloading
  their attachments
- only new tickets hit the API for conversation/attachment work
- attachments already on disk are skipped regardless, so downloads resume cleanly

Requires `-split-json true` (the default).

Re-runs also backfill tickets cached before the requested-item/inline
export existed: only `requested_items` are re-fetched (1 API call each) and
only missing files downloaded — conversations are not re-queried.

## Notes

- Freshservice v2 pagination is page-number based (max 100 per page). For very
  large accounts the API restricts how far you can page, so prefer a
  `-updated-since` sliding window if you hit that boundary.
- `status` values: 2=Open, 3=Pending, 4=Resolved, 5=Closed, 6=Escalated.
- Attachments are downloaded into `output/attachments/<ticket_id>/` with the
  original name preserved, and each ticket/conversation JSON records
  `downloaded_path` pointing at the saved file. Attachment binaries are also
  indexable via `csv/attachments.csv`.
- Inline images (`<img src=".../inline/attachment?token=...">` embedded in
  ticket descriptions and conversation bodies) are NOT listed in the API's
  `attachments` arrays, so the fetcher extracts them from the HTML and
  downloads them the same way (no extra API calls — the token URL authorises
  the download). They are stored as `inline_<id>_<name>` and listed on the
  ticket/conversation JSON under `inline_attachments`.
- Service-catalog file uploads live on `GET /tickets/{id}/requested_items`
  (one extra API call per ticket, only when `-attachments` is on) and are
  downloaded like regular attachments with source `requested_item`.
  Incident tickets have no requested items — the API answers those with
  `404`, which the fetcher treats as an empty list (no warning).