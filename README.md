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
