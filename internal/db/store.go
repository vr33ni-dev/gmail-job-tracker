package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

type Store struct{ db *sql.DB }

func New(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) ListApplications(ctx context.Context) ([]domain.Application, error) {
	rows, err := s.db.QueryContext(ctx, `
    SELECT id, company, role, platform, applied_at, status, last_email_id, email_body, language, notes, url, created_at, updated_at
    FROM applications ORDER BY applied_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []domain.Application
	for rows.Next() {
		var a domain.Application
		if err := rows.Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
			&a.Status, &a.LastEmailID, &a.EmailBody, &a.Language, &a.Notes, &a.URL, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

func (s *Store) UpsertApplication(ctx context.Context, a *domain.Application) error {
	return s.db.QueryRowContext(ctx, `
    INSERT INTO applications (company, role, platform, applied_at, status, last_email_id, email_body, language, url)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
    ON CONFLICT (id) DO UPDATE
    SET status=EXCLUDED.status, last_email_id=EXCLUDED.last_email_id, 
        email_body=EXCLUDED.email_body, updated_at=NOW()
    RETURNING id, created_at, updated_at`,
		a.Company, a.Role, a.Platform, a.AppliedAt, a.Status, a.LastEmailID, a.EmailBody, a.Language, a.URL,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

func (s *Store) UpdateStatus(ctx context.Context, id int64, status domain.Status, emailID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE applications SET status=$1, last_email_id=$2, updated_at=NOW() WHERE id=$3`,
		status, emailID, id)
	return err
}

func (s *Store) FindByCompanyAndRole(ctx context.Context, company, role string) (*domain.Application, error) {
	var a domain.Application
	err := s.db.QueryRowContext(ctx, `
		SELECT id, company, role, platform, applied_at, status, last_email_id, notes, url, created_at, updated_at
		FROM applications WHERE LOWER(company)=LOWER($1) AND LOWER(role)=LOWER($2)
		ORDER BY applied_at DESC LIMIT 1`, company, role,
	).Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
		&a.Status, &a.LastEmailID, &a.Notes, &a.URL, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &a, err
}

func (s *Store) IsEmailProcessed(ctx context.Context, emailID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_emails WHERE email_id=$1)`, emailID).Scan(&exists)
	return exists, err
}

func (s *Store) MarkEmailProcessed(ctx context.Context, emailID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO processed_emails (email_id) VALUES ($1) ON CONFLICT DO NOTHING`, emailID)
	return err
}

func (s *Store) RecordStatusEvent(ctx context.Context, e *domain.StatusEvent) error {
	return s.db.QueryRowContext(ctx, `
		INSERT INTO status_events (application_id, from_status, to_status, email_id, email_subject)
		VALUES ($1,$2,$3,$4,$5) RETURNING id, parsed_at`,
		e.ApplicationID, e.FromStatus, e.ToStatus, e.EmailID, e.EmailSubject,
	).Scan(&e.ID, &e.ParsedAt)
}

func (s *Store) LastPollTime(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(processed_at) FROM processed_emails`).Scan(&t)
	if err == sql.ErrNoRows {
		return time.Now().Add(-30 * 24 * time.Hour), nil
	}
	return t, err
}
