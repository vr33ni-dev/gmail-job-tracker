-- +goose Up
CREATE TYPE application_status AS ENUM (
    'applied','reviewing','screening','interview','ai_interview','offer','rejected','withdrawn','no_response'
);

CREATE TABLE applications (
    id            BIGSERIAL PRIMARY KEY,
    company       TEXT NOT NULL,
    role          TEXT NOT NULL,
    platform      TEXT NOT NULL DEFAULT 'other',
    applied_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status        application_status NOT NULL DEFAULT 'applied',
    last_email_id TEXT,
    email_body    TEXT NOT NULL DEFAULT '',
    language      TEXT NOT NULL DEFAULT 'en',
    notes         TEXT NOT NULL DEFAULT '',
    url           TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE status_events (
    id             BIGSERIAL PRIMARY KEY,
    application_id BIGINT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    from_status    application_status,
    to_status      application_status NOT NULL,
    email_id       TEXT,
    email_subject  TEXT,
    parsed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE processed_emails (
    email_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_applications_status     ON applications(status);
CREATE INDEX idx_applications_applied_at ON applications(applied_at DESC);
CREATE INDEX idx_status_events_app       ON status_events(application_id);

-- +goose Down
DROP TABLE IF EXISTS status_events;
DROP TABLE IF EXISTS processed_emails;
DROP TABLE IF EXISTS applications;
DROP TYPE IF EXISTS application_status;