# Gmail Job Tracker

Automatically syncs your Gmail inbox and uses Claude AI to detect and track job applications.

## What it does

- Connects to your Gmail account via OAuth
- Scans emails for job application signals (confirmations, rejections, interviews, etc.)
- Stores and tracks applications in a PostgreSQL database
- Exposes a REST API to query your application history

## Tech stack

- **Go** with [chi](https://github.com/go-chi/chi) router
- **PostgreSQL** with [goose](https://github.com/pressly/goose) migrations
- **Gmail API** for email access
- **Claude API** (Anthropic) for email classification

## Prerequisites

- Go 1.21+
- PostgreSQL
- A Google Cloud project with the Gmail API enabled
- An Anthropic API key

## Setup

1. Clone the repo and install dependencies:

```bash
git clone https://github.com/vr33ni-dev/gmail-job-tracker.git
cd gmail-job-tracker
go mod download
```

1. Copy the example env file and fill in your values:

```bash
cp .env.example .env
```

| Variable               | Description                  |
| ---------------------- | ---------------------------- |
| `DATABASE_URL`         | PostgreSQL connection string |
| `ANTHROPIC_API_KEY`    | Your Anthropic API key       |
| `GOOGLE_CLIENT_ID`     | Google OAuth client ID       |
| `GOOGLE_CLIENT_SECRET` | Google OAuth client secret   |
| `PORT`                 | HTTP port (default: 8080)    |

1. Run database migrations:

```bash
go run cmd/server/main.go migrate
# Apply db seed
make db-reset
go run cmd/server/main.go  # wait for "migrations applied"
# Ctrl+C
make db-seed
```

1. Start the server:

```bash
go run cmd/server/main.go
```

## Google OAuth setup

1. Go to the [Google Cloud Console](https://console.cloud.google.com/)
2. Create a project and enable the Gmail API
3. Create OAuth 2.0 credentials (Desktop app)
4. Download the credentials and add the client ID and secret to your `.env`

On first run, you'll be prompted to authorize access — this generates a `token.json` file (never commit this).

## Configuration

### User Settings

The `settings` table stores personal configuration. Update after setup:

```bash
make db-set-user
```

| Key | Description | Example |
|-----|-------------|---------|
| `user_email` | Your Gmail address — used to detect sent emails | `you@gmail.com` |
| `user_name` | Your last name — used to filter out emails you sent | `smith` |
| `last_poll_time` | Controls how far back Gmail is polled on first sync | `2026-01-01` |

### Company Aliases

Some companies send emails from different names or ATS platforms. Add aliases to ensure they group correctly:

```bash
make db-add-alias
# enter: Emma Sleep GmbH
# enter: Emma - The Sleep Company
```

Or directly:

```sql
INSERT INTO company_aliases VALUES ('Emma Sleep GmbH', 'Emma - The Sleep Company');
```

Common cases:

- ATS platforms sending as the company (`lever.co`, `greenhouse.io`)
- Company name variations (`epilot GmbH` vs `epilot`)
- Lowercase variants (`acto` vs `Acto`)

Personal aliases go in `seeds.sql` (git-ignored) so they survive resets:

```bash
make db-seed
```

### Personal Seed File

`seeds.sql` is git-ignored and contains your personal configuration:

- User email and name (emails sent from this address are filtered out during sync. Without this, your own replies (questions to recruiters, withdrawal emails) would be processed as incoming job emails and potentially misclassified)
- Company aliases specific to your applications

After every `make db-reset`, run `make db-seed` to restore your config. Or use `make db-fresh` which does both.

## TODO

### Multi-step reasoning (optional)

Currently classification happens in a single LLM prompt. A two-step approach would improve accuracy:

- Step 1: "Is this a job application email?" → Yes/No filter
- Step 2: Full classification prompt only for confirmed job emails

This reduces false positives (newsletters, unrelated emails slipping through) at the cost of 2x API calls.

Configurable via settings table:

```sql
UPDATE settings SET value='true' WHERE key='multi_step_reasoning';
```

When enabled, Step 1 acts as a pre-filter before the main classification prompt.
Only relevant for bulk syncs — day-to-day volume (2-5 emails) makes this unnecessary.

### Entity extraction improvement

Currently company name and role are extracted by the LLM in a single pass alongside status classification. This can be unreliable when:

- Company name in body differs from sender domain
- Role varies between emails for the same position
- ATS platforms (Lever, Greenhouse) obscure the actual company

Planned improvement:

- Extract sender domain as a reliable company identifier fall
