# Gmail Job Tracker

Automatically syncs a Gmail inbox and uses an LLM to detect and track job applications.

## What it does

- Connects to Gmail account via OAuth
- Scans emails for job application signals (confirmations, rejections, interviews, etc.)
- Stores and tracks applications in a PostgreSQL database
- Exposes a REST API to query your application history

## Tech stack

- **Go** with [chi](https://github.com/go-chi/chi) router
- **PostgreSQL** with [goose](https://github.com/pressly/goose) migrations
- **React + TypeScript** frontend
- **Gmail API** for email access
- **LLM classification** — supports Claude (Anthropic), Ollama (local, default)

## Prerequisites

- Go 1.22+
- A Google Cloud project with the Gmail API enabled
- One of:
  - [Ollama](https://ollama.com) (free, local)
  - An Anthropic API key (Claude)

## LLM Setup

By default the tracker uses Ollama for local, free, private classification.

### Ollama

```bash
brew install ollama
ollama serve
ollama pull llama3.1:8b
```

Set in `.env`:

```bash
LLM_PROVIDER=ollama
OLLAMA_MODEL=llama3.1:8b
```

### Claude (Anthropic)

Better accuracy, limited usage. Recommended for initial bulk sync.

```bash
LLM_PROVIDER=claude
ANTHROPIC_API_KEY=your_key_here
```

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
| `LLM_PROVIDER`         | claude OR ollama             |
| `OLLAMA_MODEL`         | llama3.1:8b                  |
| `IS_DEMO`              | demonstration mode           |
| `IN_CLAUSE`            | defaults to ':anywhere'      |
| `OUTPUT_LABEL`         | e.g. 'jobs'                  |

1. Run database migrations:

```bash
go run cmd/server/main.go 
OR
make run
# Reset db
make db-reset / make db-truncate # keep corrections, useful for re-syncs
## restart server # wait for "migrations applied"
# Ctrl+C
# Apply DB seed
make db-seed / db-seed-demo
```

1. Start the server:

```bash
go run cmd/server/main.go 
make run
```

## Google OAuth setup

1. Go to the [Google Cloud Console](https://console.cloud.google.com/)
2. Create a project and enable the Gmail API
3. Create OAuth 2.0 credentials (Desktop app)
4. Download the credentials and add the client ID and secret to your `.env`

On first run, you'll be prompted to authorize access — this generates a `token.json` file (never commit this).

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
make db-seed OR make db-seed-demo
```

### Personal Seed File

`seeds.sql` is git-ignored and contains your personal configuration:

- Company aliases specific to your applications

After every `make db-reset`, run `make db-seed` to restore your config. Or use `make db-fresh` which does both.

## Outlook

Follow my Github Issues to stay tuned for future improvements.
