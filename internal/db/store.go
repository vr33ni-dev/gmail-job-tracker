package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

func (s *Store) ListStatusEvents(ctx context.Context, applicationID int64) ([]domain.StatusEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, application_id, from_status, to_status, email_id, email_subject, parsed_at
		FROM status_events WHERE application_id=$1 ORDER BY parsed_at ASC`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.StatusEvent
	for rows.Next() {
		var e domain.StatusEvent
		if err := rows.Scan(&e.ID, &e.ApplicationID, &e.FromStatus, &e.ToStatus,
			&e.EmailID, &e.EmailSubject, &e.ParsedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

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
		var lastEmailID, emailBody, language, notes, url sql.NullString
		if err := rows.Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
			&a.Status, &lastEmailID, &emailBody, &language, &notes, &url, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.LastEmailID = lastEmailID.String
		a.EmailBody = emailBody.String
		a.Language = language.String
		a.Notes = notes.String
		a.URL = url.String
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

func (s *Store) UpdateStatus(ctx context.Context, id int64, status domain.Status, emailID, emailBody string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE applications SET status=$1, last_email_id=$2, email_body=$3, updated_at=NOW() WHERE id=$4`,
		status, emailID, emailBody, id)
	return err
}

func (s *Store) FindByCompanyAndRole(ctx context.Context, company, role string) (*domain.Application, error) {
	var a domain.Application
	var lastEmailID, emailBody, language, notes, url sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, company, role, platform, applied_at, status, last_email_id, email_body, language, notes, url, created_at, updated_at
		FROM applications
		WHERE (
			LOWER(company) = LOWER($1)
			OR LOWER(company) LIKE '%' || LOWER($1) || '%'
			OR LOWER($1) LIKE '%' || LOWER(company) || '%'
		)
		AND (
			LOWER(role) = LOWER($2)
			OR LOWER(role) LIKE '%' || LOWER($2) || '%'
			OR LOWER($2) LIKE '%' || LOWER(role) || '%'
		)
		ORDER BY applied_at DESC
		LIMIT 1`, company, role,
	).Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
		&a.Status, &lastEmailID, &emailBody, &language, &notes, &url, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LastEmailID = lastEmailID.String
	a.EmailBody = emailBody.String
	a.Language = language.String
	a.Notes = notes.String
	a.URL = url.String
	return &a, nil
}

func (s *Store) ListGroupedApplications(ctx context.Context) ([]domain.GroupedApplication, error) {
	// load aliases
	aliases := make(map[string]string)
	aliasRows, err := s.db.QueryContext(ctx, `SELECT alias, canonical FROM company_aliases`)
	if err == nil {
		defer aliasRows.Close()
		for aliasRows.Next() {
			var alias, canonical string
			if err := aliasRows.Scan(&alias, &canonical); err == nil {
				aliases[strings.ToLower(alias)] = canonical
			}
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, company, role, platform, language, url, status, applied_at, email_body, last_email_id
		FROM applications
		ORDER BY company, role, applied_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type raw struct {
		id          int64
		company     string
		role        string
		platform    string
		language    string
		url         string
		status      domain.Status
		appliedAt   time.Time
		emailBody   string
		lastEmailID string
	}

	groupMap := make(map[string]*domain.GroupedApplication)
	var order []string

	for rows.Next() {
		var r raw
		var emailBody, lastEmailID, language, url sql.NullString
		if err := rows.Scan(&r.id, &r.company, &r.role, &r.platform,
			&language, &url, &r.status, &r.appliedAt, &emailBody, &lastEmailID); err != nil {
			return nil, err
		}
		r.emailBody = emailBody.String
		r.lastEmailID = lastEmailID.String
		r.language = language.String
		r.url = url.String

		// resolve company alias
		company := r.company
		if canonical, ok := aliases[strings.ToLower(r.company)]; ok {
			company = canonical
		}

		key := strings.ToLower(company) + "|" + strings.ToLower(domain.NormalizeRole(r.role))
		if _, exists := groupMap[key]; !exists {
			groupMap[key] = &domain.GroupedApplication{
				Company:   company,
				Role:      domain.NormalizeRole(r.role),
				Platform:  r.platform,
				Language:  r.language,
				URL:       r.url,
				AppliedAt: r.appliedAt,
			}
			order = append(order, key)
		}

		groupMap[key].Stages = append(groupMap[key].Stages, domain.ApplicationStage{
			ID:          r.id,
			Status:      r.status,
			AppliedAt:   r.appliedAt,
			EmailBody:   r.emailBody,
			LastEmailID: r.lastEmailID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	statusPriority := map[domain.Status]int{
		domain.StatusApplied:     1,
		domain.StatusAIInterview: 2,
		domain.StatusInterview:   3,
		domain.StatusOffer:       4,
		domain.StatusRejected:    5,
		domain.StatusWithdrawn:   6,
	}

	result := make([]domain.GroupedApplication, 0, len(order))
	for _, key := range order {
		g := groupMap[key]

		var best domain.Status
		for _, stage := range g.Stages {
			if best == "" {
				best = stage.Status
				continue
			}
			if statusPriority[stage.Status] > statusPriority[best] {
				best = stage.Status
			}
		}
		g.CurrentStatus = best
		result = append(result, *g)
	}

	return result, nil
}

func (s *Store) FindByCompanyRoleAndStatus(ctx context.Context, company, role string, status domain.Status) (*domain.Application, error) {
	var a domain.Application
	var lastEmailID, emailBody, language, notes, url sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, company, role, platform, applied_at, status, last_email_id, email_body, language, notes, url, created_at, updated_at
		FROM applications
		WHERE (
			LOWER(company) = LOWER($1)
			OR LOWER(company) LIKE '%' || LOWER($1) || '%'
			OR LOWER($1) LIKE '%' || LOWER(company) || '%'
		)
		AND (
			LOWER(role) = LOWER($2)
			OR LOWER(role) LIKE '%' || LOWER($2) || '%'
			OR LOWER($2) LIKE '%' || LOWER(role) || '%'
		)
		AND status = $3
		ORDER BY applied_at DESC
		LIMIT 1`, company, role, status,
	).Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
		&a.Status, &lastEmailID, &emailBody, &language, &notes, &url, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LastEmailID = lastEmailID.String
	a.EmailBody = emailBody.String
	a.Language = language.String
	a.Notes = notes.String
	a.URL = url.String
	return &a, nil
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
	if err == sql.ErrNoRows || t.IsZero() {
		return time.Now().Add(-90 * 24 * time.Hour), nil
	}
	return t, err
}

func (s *Store) AddCorrection(ctx context.Context, emailID, emailSubject, emailBody string, wrongStatus, correctStatus domain.Status) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO corrections (email_id, email_subject, email_body, wrong_status, correct_status)
		VALUES ($1, $2, $3, $4, $5)`,
		emailID, emailSubject, emailBody, wrongStatus, correctStatus)
	return err
}

func (s *Store) GetRecentCorrections(ctx context.Context, limit int) ([]domain.Correction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email_subject, email_body, wrong_status, correct_status
		FROM corrections ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var corrections []domain.Correction
	for rows.Next() {
		var c domain.Correction
		if err := rows.Scan(&c.EmailSubject, &c.EmailBody, &c.WrongStatus, &c.CorrectStatus); err != nil {
			return nil, err
		}
		corrections = append(corrections, c)
	}
	return corrections, rows.Err()
}

func (s *Store) GetApplication(ctx context.Context, id int64) (*domain.Application, error) {
	var a domain.Application
	var lastEmailID, emailBody, language, notes, url sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, company, role, platform, applied_at, status, last_email_id, email_body, language, notes, url, created_at, updated_at
		FROM applications WHERE id=$1`, id,
	).Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
		&a.Status, &lastEmailID, &emailBody, &language, &notes, &url, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.LastEmailID = lastEmailID.String
	a.EmailBody = emailBody.String
	a.Language = language.String
	a.Notes = notes.String
	a.URL = url.String
	return &a, nil
}

func (s *Store) ApplicationExistsByEmailID(ctx context.Context, emailID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM applications WHERE last_email_id=$1)`, emailID,
	).Scan(&exists)
	return exists, err
}

func (s *Store) FindMostRecentByCompany(ctx context.Context, company string) (*domain.Application, error) {
	var a domain.Application
	var lastEmailID, emailBody, language, notes, url sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, company, role, platform, applied_at, status, last_email_id, email_body, language, notes, url, created_at, updated_at
		FROM applications
		WHERE LOWER(company) = LOWER($1) AND role != ''
		ORDER BY applied_at DESC LIMIT 1`, company,
	).Scan(&a.ID, &a.Company, &a.Role, &a.Platform, &a.AppliedAt,
		&a.Status, &lastEmailID, &emailBody, &language, &notes, &url, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LastEmailID = lastEmailID.String
	a.EmailBody = emailBody.String
	a.Language = language.String
	a.Notes = notes.String
	a.URL = url.String
	return &a, nil
}

func (s *Store) ResolveCompanyAlias(ctx context.Context, company string) string {
	var canonical string
	err := s.db.QueryRowContext(ctx,
		`SELECT canonical FROM company_aliases WHERE LOWER(alias)=LOWER($1)`, company,
	).Scan(&canonical)
	if err != nil {
		return company
	}
	return canonical
}

func (s *Store) GetSetting(ctx context.Context, key string) string {
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key=$1`, key,
	).Scan(&value)
	if err != nil {
		return ""
	}
	return value
}

func (s *Store) HasAppliedStage(ctx context.Context, company, role string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM applications 
			WHERE LOWER(company)=LOWER($1) 
			AND (LOWER(role)=LOWER($2) OR $2='')
			AND status='applied'
		)`, company, role,
	).Scan(&exists)
	return exists, err
}

func (s *Store) FixEmptyRoles(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
        UPDATE applications a SET role = (
            SELECT role FROM applications b 
            WHERE LOWER(b.company) = LOWER(a.company) 
            AND b.role != '' 
            ORDER BY b.applied_at DESC LIMIT 1
        ) 
        WHERE a.role = '' 
        AND EXISTS (
            SELECT 1 FROM applications b 
            WHERE LOWER(b.company) = LOWER(a.company) 
            AND b.role != ''
        )`)
	return err
}
