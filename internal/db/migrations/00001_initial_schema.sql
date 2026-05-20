-- +goose Up
CREATE TYPE application_status AS ENUM (
    'applied','ai_interview','interview','offer','rejected','withdrawn'
);

CREATE TABLE applications (
    id         BIGSERIAL PRIMARY KEY,
    company    TEXT NOT NULL,
    role       TEXT NOT NULL,
    platform   TEXT NOT NULL DEFAULT 'other',
    language   TEXT NOT NULL DEFAULT 'en',
    url        TEXT NOT NULL DEFAULT '',
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE application_stages (
    id             BIGSERIAL PRIMARY KEY,
    application_id BIGINT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    status         application_status NOT NULL DEFAULT 'applied',
    last_email_id  TEXT NOT NULL DEFAULT '',
    needs_review   BOOLEAN NOT NULL DEFAULT false,
    applied_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE processed_emails (
    email_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE corrections (
    id             BIGSERIAL PRIMARY KEY,
    email_id       TEXT NOT NULL DEFAULT '',
    email_subject  TEXT NOT NULL DEFAULT '',
    email_body     TEXT NOT NULL DEFAULT '',
    wrong_status   TEXT NOT NULL DEFAULT '',
    correct_status TEXT NOT NULL DEFAULT '',
    command        TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE thread_emails (
    id             BIGSERIAL PRIMARY KEY,
    email_id       TEXT NOT NULL UNIQUE,
    thread_id      TEXT NOT NULL,
    application_id BIGINT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    stage_id       BIGINT REFERENCES application_stages(id) ON DELETE SET NULL,
    from_addr      TEXT NOT NULL DEFAULT '',
    subject        TEXT NOT NULL DEFAULT '',
    body           TEXT NOT NULL DEFAULT '',
    email_date     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
INSERT INTO settings VALUES ('user_email', '');
INSERT INTO settings VALUES ('user_name', '');

CREATE TABLE company_aliases (
    alias     TEXT PRIMARY KEY,
    canonical TEXT NOT NULL
);

CREATE INDEX idx_applications_company       ON applications(LOWER(company));
CREATE INDEX idx_application_stages_app     ON application_stages(application_id);
CREATE INDEX idx_application_stages_status  ON application_stages(status);
CREATE INDEX idx_application_stages_applied ON application_stages(applied_at DESC);
CREATE INDEX idx_thread_emails_thread       ON thread_emails(thread_id);
CREATE INDEX idx_thread_emails_stage        ON thread_emails(stage_id);
CREATE INDEX idx_thread_emails_application  ON thread_emails(application_id);

-- +goose Down
DROP TABLE IF EXISTS thread_emails;
DROP TABLE IF EXISTS company_aliases;
DROP TABLE IF EXISTS corrections;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS processed_emails;
DROP TABLE IF EXISTS application_stages;
DROP TABLE IF EXISTS applications;
DROP TYPE IF EXISTS application_status;
