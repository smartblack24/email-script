# FWD Email Outreach CLI

Production-ready **CLI-only** Gmail outreach automation for **FWD Consulting Group**.

No frontend, dashboard, or web UI — just a Cobra CLI and an optional background worker backed by SQLite.

## Features

- Import contacts from Excel (`.xlsx`) or CSV with flexible header mapping
- Approval gate before any send
- Gmail OAuth 2.0 with token refresh, quotas, backoff, and idempotent sends
- Editable plain-text templates (initial + follow-ups)
- Configurable follow-ups (default Day 0 / 4 / 9) with reply detection
- Suppression / permanent opt-out list
- Named campaigns with pause/resume
- Dry-run by default; live send requires `--execute` plus compliance config
- Excel export reporting

## Requirements

- Go 1.22+ (toolchain may auto-download a newer patch if needed)
- A Google Cloud OAuth **Desktop** client JSON for live Gmail sends

## Project layout

```
fwd-email-outreach/
  cmd/outreach/          # CLI entrypoint
  internal/              # config, importer, gmail, sender, scheduler, …
  migrations/            # reference SQL schema
  templates/             # initial.txt, followup1.txt, followup2.txt
  sample-data/           # contacts.xlsx + approved.csv
  tests/                 # end-to-end tests
  data/                  # SQLite DB (created at runtime; gitignored)
```

## Build (Windows / macOS / Linux)

```bash
cd fwd-email-outreach
go mod download
go build -o outreach ./cmd/outreach
```

Windows:

```bat
cd fwd-email-outreach
go build -o outreach.exe .\cmd\outreach
```

## Configuration

Copy `.env.example` to `.env` and edit:

```bash
cp .env.example .env
```

**Required before live sends (`--execute`):**

| Variable | Purpose |
|----------|---------|
| `SENDER_NAME` | From display name |
| `SENDER_EMAIL` | Must match authenticated Gmail |
| `BUSINESS_NAME` | Business identity |
| `BUSINESS_ADDRESS` | Physical postal address |
| `OPT_OUT_EMAIL` | Unsubscribe contact |
| `DRY_RUN=false` | Allow live sends |

Conservative defaults: daily/hourly limits, min interval, business-hour window, `DRY_RUN=true`.

## Gmail OAuth setup (user credentials required)

Live Gmail access cannot be completed in CI without your Google account:

1. Open [Google Cloud Console](https://console.cloud.google.com/)
2. Create/select a project → APIs & Services → enable **Gmail API**
3. Configure OAuth consent screen (External or Internal)
4. Create credentials → **OAuth client ID** → Application type **Desktop app**
5. Download the JSON and save as `credentials.json` (or path in `GMAIL_CREDENTIALS_FILE`)
6. Run:

```bash
./outreach auth
```

7. Open the printed URL, sign in, paste the authorization code
8. `token.json` is saved (gitignored) and refreshed automatically thereafter

Until OAuth is configured, use `--mock-gmail` for local demos, and rely on the unit/integration tests (they use an in-memory Gmail mock).

## Typical workflow

```bash
# 1) Import prospects
./outreach import --file sample-data/contacts.xlsx

# 2) Review
./outreach contacts list

# 3) Approve from a curated CSV (Email column required)
./outreach contacts approve --file sample-data/approved.csv

# 4) Preview rendered messages
./outreach preview --limit 10

# 5) Dry-run (default; no Gmail calls)
./outreach send --dry-run --limit 20

# 6) Live send (only after OAuth + compliance + DRY_RUN=false)
./outreach send --execute --limit 20

# 7) Background follow-ups / reply checks
./outreach worker                 # dry-run follow-ups
./outreach worker --execute       # live follow-ups

# 8) Ops
./outreach status
./outreach export --file report.xlsx
./outreach suppress --email person@example.com
./outreach campaign pause default
./outreach campaign resume default
```

Multiple campaigns: pass `--campaign other-name` on send/preview, or `campaign pause|resume <name-or-id>`.

## Tests

```bash
cd fwd-email-outreach
go test ./...
```

Coverage includes: Excel/CSV header mapping, multi specialization/links, Excel hyperlinks, duplicate emails, templates, Gmail mocks, dry-run safety, rate limits, follow-up eligibility, reply tracking, suppression, and DB claim recovery / duplicate prevention.

## Compliance notes

- Contacts are **not** emailed until approved
- Suppressed / opted-out addresses are never auto-reactivated
- Public email addresses are **not** assumed to grant bulk permission — track source/permission notes on import
- Subjects and bodies must remain non-deceptive; edit templates carefully
- The tool respects Gmail quotas and does not attempt to circumvent provider restrictions

## License

Internal use for FWD Consulting Group outreach operations.
