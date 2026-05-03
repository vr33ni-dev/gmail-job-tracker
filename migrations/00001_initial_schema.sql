-- +goose Up
CREATE TYPE application_status AS ENUM (
    'applied','ai_interview','interview','offer','rejected','withdrawn'
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
CREATE TABLE corrections (
    id             BIGSERIAL PRIMARY KEY,
    email_id       TEXT NOT NULL,
    email_subject  TEXT,
    email_body     TEXT,
    wrong_status   application_status,
    correct_status application_status NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
INSERT INTO settings VALUES ('last_poll_time', '2026-01-30');
INSERT INTO settings VALUES ('user_email', '');
INSERT INTO settings VALUES ('user_name', '');
INSERT INTO settings VALUES ('multi_step_reasoning', 'false');

CREATE TABLE company_aliases (
    alias     TEXT PRIMARY KEY,
    canonical TEXT NOT NULL
);
CREATE INDEX idx_applications_status      ON applications(status);
CREATE INDEX idx_applications_applied_at  ON applications(applied_at DESC);
CREATE INDEX idx_status_events_app        ON status_events(application_id);

-- +goose Down
DROP TABLE IF EXISTS company_aliases;
DROP TABLE IF EXISTS corrections;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS status_events;
DROP TABLE IF EXISTS processed_emails;
DROP TABLE IF EXISTS applications;
DROP TYPE IF EXISTS application_status;