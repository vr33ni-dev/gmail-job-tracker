package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	pq "github.com/lib/pq"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

type Store struct{ db *sql.DB }

// EnsureDatabase creates the database in the DSN if it doesn't exist yet.
func EnsureDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return fmt.Errorf("no database name in DSN")
	}
	u.Path = "/postgres"
	sys, err := sql.Open("postgres", u.String())
	if err != nil {
		return fmt.Errorf("open system db: %w", err)
	}
	defer sys.Close()
	var exists bool
	if err := sys.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, dbName).Scan(&exists); err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if !exists {
		if _, err := sys.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(dbName)); err != nil {
			return fmt.Errorf("create database %q: %w", dbName, err)
		}
		log.Printf("db: created database %q", dbName)
	}
	return nil
}

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

// FindOrCreateApplication finds an existing application by company+role or creates one.
// Returns the application ID.
func (s *Store) FindOrCreateApplication(ctx context.Context, company, role, platform, language, url string, appliedAt time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM applications WHERE LOWER(company)=LOWER($1) AND LOWER(role)=LOWER($2) LIMIT 1`,
		company, role,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO applications (company, role, platform, language, url, applied_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		company, role, platform, language, url, appliedAt,
	).Scan(&id)
	return id, err
}

// FindOrCreateApplication finds an existing application by company+role or creates one.
// Returns the application ID.
func (s *Store) FindApplicationById(ctx context.Context, id int64) (*domain.Application, error) {
	var app domain.Application
	err := s.db.QueryRowContext(ctx,
		`SELECT id, company, role, platform, language, url, applied_at FROM applications WHERE id = $1`,
		id,
	).Scan(&app.ID, &app.Company, &app.Role, &app.Platform, &app.Language, &app.URL, &app.AppliedAt)
	if err != nil {
		return nil, err
	}
	return &app, nil
}

// CreateStage inserts a new application_stages row and returns the stage ID.
// Returns domain.ErrDuplicateStage if the status is a singleton and one already exists.
func (s *Store) CreateStage(ctx context.Context, applicationID int64, status domain.Status, lastEmailID string, needsReview bool, appliedAt time.Time) (int64, error) {
	if domain.IsSingletonStage(status) {
		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM application_stages WHERE application_id=$1 AND status=$2)`,
			applicationID, status,
		).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, domain.ErrDuplicateStage
		}
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO application_stages (application_id, status, last_email_id, needs_review, applied_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		applicationID, status, lastEmailID, needsReview, appliedAt,
	).Scan(&id)
	return id, err
}

func (s *Store) ListApplications(ctx context.Context) ([]domain.Application, error) {
	return s.listApplications(ctx, "")
}

func (s *Store) listApplications(ctx context.Context, company string) ([]domain.Application, error) {
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

	query := `
		SELECT a.id, a.company, a.role, a.platform, a.language, a.url, a.applied_at,
		       s.id, s.status, s.last_email_id, s.needs_review, s.applied_at
		FROM applications a
		JOIN application_stages s ON s.application_id = a.id`
	var args []any
	if company != "" {
		query += ` WHERE LOWER(a.company) = LOWER($1)`
		args = append(args, company)
	}
	query += ` ORDER BY a.company, a.role, s.applied_at ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	appMap := make(map[int64]*domain.Application)
	var order []int64

	statusPriority := map[domain.Status]int{
		domain.StatusApplied:     1,
		domain.StatusAIInterview: 2,
		domain.StatusInterview:   3,
		domain.StatusOffer:       4,
		domain.StatusRejected:    5,
		domain.StatusWithdrawn:   6,
	}

	for rows.Next() {
		var appID int64
		var company, role, platform, language, url string
		var appAppliedAt time.Time
		var stage domain.ApplicationStage
		if err := rows.Scan(
			&appID, &company, &role, &platform, &language, &url, &appAppliedAt,
			&stage.ID, &stage.Status, &stage.LastEmailID, &stage.NeedsReview, &stage.AppliedAt,
		); err != nil {
			return nil, err
		}
		if canonical, ok := aliases[strings.ToLower(company)]; ok {
			company = canonical
		}
		stage.ApplicationID = appID
		if _, exists := appMap[appID]; !exists {
			appMap[appID] = &domain.Application{
				ID:        appID,
				Company:   company,
				Role:      domain.NormalizeRole(role),
				Platform:  platform,
				Language:  language,
				URL:       url,
				AppliedAt: appAppliedAt,
			}
			order = append(order, appID)
		}
		app := appMap[appID]
		app.Stages = append(app.Stages, stage)
		if stage.AppliedAt.After(app.LastUpdatedAt) {
			app.LastUpdatedAt = stage.AppliedAt
		}
		if statusPriority[stage.Status] > statusPriority[app.CurrentStatus] {
			app.CurrentStatus = stage.Status
			if stage.LastEmailID != "" {
				app.GmailURL = "https://mail.google.com/mail/u/0/#all/" + stage.LastEmailID
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]domain.Application, 0, len(order))
	for _, id := range order {
		result = append(result, *appMap[id])
	}
	return result, nil
}

func (s *Store) GetStageByID(ctx context.Context, stageID int64) (*domain.ApplicationStage, error) {
	var st domain.ApplicationStage
	err := s.db.QueryRowContext(ctx,
		`SELECT id, application_id, status, last_email_id, needs_review, applied_at
		 FROM application_stages WHERE id=$1`, stageID,
	).Scan(&st.ID, &st.ApplicationID, &st.Status, &st.LastEmailID, &st.NeedsReview, &st.AppliedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) FindApplicationByCompanyAndRole(ctx context.Context, company, role string) (*domain.Application, error) {
	var app domain.Application
	err := s.db.QueryRowContext(ctx,
		`SELECT id, company, role, platform, language, url, applied_at
		 FROM applications
		 WHERE LOWER(company)=LOWER($1) AND LOWER(role)=LOWER($2)
		 LIMIT 1`, company, role,
	).Scan(&app.ID, &app.Company, &app.Role, &app.Platform, &app.Language, &app.URL, &app.AppliedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &app, nil
}

func (s *Store) FindMostRecentByCompany(ctx context.Context, company string) (*domain.Application, error) {
	var app domain.Application
	err := s.db.QueryRowContext(ctx,
		`SELECT id, company, role, platform, language, url, applied_at
		 FROM applications
		 WHERE LOWER(company)=LOWER($1) AND role != ''
		 ORDER BY applied_at DESC LIMIT 1`, company,
	).Scan(&app.ID, &app.Company, &app.Role, &app.Platform, &app.Language, &app.URL, &app.AppliedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &app, nil
}

func (s *Store) UpdateStageStatus(ctx context.Context, stageID int64, status domain.Status, lastEmailID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE application_stages SET status=$1, last_email_id=$2, updated_at=NOW() WHERE id=$3`,
		status, lastEmailID, stageID)
	return err
}

func (s *Store) DeleteStage(ctx context.Context, stageID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM application_stages WHERE id=$1`, stageID)
	return err
}

func (s *Store) MarkStageReviewed(ctx context.Context, stageID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE application_stages SET needs_review=false WHERE id=$1`, stageID)
	return err
}

func (s *Store) GetStagesByStatus(ctx context.Context, applicationID int64, status domain.Status) ([]domain.ApplicationStage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, application_id, status, last_email_id, needs_review, applied_at
		 FROM application_stages
		 WHERE application_id=$1 AND status=$2
		 ORDER BY applied_at ASC`,
		applicationID, status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stages []domain.ApplicationStage
	for rows.Next() {
		var st domain.ApplicationStage
		if err := rows.Scan(&st.ID, &st.ApplicationID, &st.Status, &st.LastEmailID, &st.NeedsReview, &st.AppliedAt); err != nil {
			return nil, err
		}
		stages = append(stages, st)
	}
	return stages, rows.Err()
}

func (s *Store) HasAppliedStage(ctx context.Context, applicationID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM application_stages WHERE application_id=$1 AND status='applied')`,
		applicationID,
	).Scan(&exists)
	return exists, err
}

func (s *Store) ApplicationExistsByEmailID(ctx context.Context, emailID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM application_stages WHERE last_email_id=$1)`, emailID,
	).Scan(&exists)
	return exists, err
}

func (s *Store) GetStageByLastEmailID(ctx context.Context, emailID string) (*domain.ApplicationStage, error) {
	var st domain.ApplicationStage
	err := s.db.QueryRowContext(ctx,
		`SELECT id, application_id, status, last_email_id, needs_review, applied_at
		 FROM application_stages WHERE last_email_id=$1 LIMIT 1`, emailID,
	).Scan(&st.ID, &st.ApplicationID, &st.Status, &st.LastEmailID, &st.NeedsReview, &st.AppliedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &st, err
}

func (s *Store) GetAllJobEmailIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email_id FROM thread_emails`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetThreadIDsByApplication(ctx context.Context, applicationID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT thread_id FROM thread_emails WHERE application_id=$1 AND thread_id != ''`,
		applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetAllJobThreadIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT thread_id FROM thread_emails WHERE application_id IS NOT NULL AND thread_id != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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

func (s *Store) LastPollTime(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(processed_at) FROM processed_emails`).Scan(&t)
	if err == sql.ErrNoRows || t.IsZero() {
		return time.Now().Add(-120 * 24 * time.Hour), nil
	}
	return t, err
}

func (s *Store) UnmarkEmailProcessed(ctx context.Context, emailID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM processed_emails WHERE email_id = $1`, emailID)
	return err
}

func (s *Store) UnmarkProcessedEmailsForStage(ctx context.Context, stageID int64) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM processed_emails WHERE email_id IN (
			SELECT email_id FROM thread_emails WHERE stage_id = $1
			UNION
			SELECT last_email_id FROM application_stages WHERE id = $1 AND last_email_id != ''
		)`, stageID)
	return err
}

func (s *Store) IsEmailInThreadEmails(ctx context.Context, emailID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM thread_emails WHERE email_id = $1)`, emailID).Scan(&exists)
	return exists, err
}

func (s *Store) FindApplicationByPersonName(ctx context.Context, name string) (*domain.Application, error) {
	var appID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT te.application_id FROM thread_emails te
		 WHERE te.body ILIKE $1 OR te.subject ILIKE $1 OR te.from_addr ILIKE $1
		 ORDER BY te.email_date DESC LIMIT 1`,
		"%"+name+"%",
	).Scan(&appID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.FindApplicationById(ctx, appID)
}

// HasSchedulingEmailForApp returns true if the application already has a
// thread email from a scheduling service (cal.com, calendly, etc.).
func (s *Store) HasSchedulingEmailForApp(ctx context.Context, appID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM thread_emails
			WHERE application_id = $1 AND (
				from_addr ILIKE '%cal.com%' OR
				from_addr ILIKE '%calendly.com%' OR
				from_addr ILIKE '%savvycal.com%' OR
				from_addr ILIKE '%chilipiper.com%'
			)
		)`, appID,
	).Scan(&exists)
	return exists, err
}

// GetContactFromAddrsForApp returns distinct from_addr values for non-scheduling,
// non-user senders in the application's thread emails — used to find related
// scheduling emails by contact name. userEmail is excluded so the user's own
// sent addresses are never used as search terms.
func (s *Store) GetContactFromAddrsForApp(ctx context.Context, appID int64, userEmail string) ([]string, error) {
	query := `SELECT DISTINCT from_addr FROM thread_emails
		 WHERE application_id = $1
		 AND from_addr NOT ILIKE '%cal.com%'
		 AND from_addr NOT ILIKE '%calendly.com%'
		 AND from_addr NOT ILIKE '%noreply%'
		 AND from_addr NOT ILIKE '%no-reply%'`
	args := []any{appID}
	if userEmail != "" {
		query += ` AND from_addr NOT ILIKE $2`
		args = append(args, "%"+userEmail+"%")
	}
	query += ` LIMIT 5`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var addrs []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err == nil && addr != "" {
			addrs = append(addrs, addr)
		}
	}
	return addrs, rows.Err()
}

func (s *Store) AddCorrection(ctx context.Context, emailID, emailSubject, emailBody string, wrongStatus domain.Status, correctStatus string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO corrections (email_id, email_subject, email_body, wrong_status, correct_status)
		VALUES ($1, $2, $3, $4, $5)`,
		emailID, emailSubject, emailBody, wrongStatus, correctStatus)
	return err
}

func (s *Store) AddCorrectionRule(ctx context.Context, rule string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO corrections (command) VALUES ($1)`, rule)
	return err
}

func (s *Store) GetRecentCorrections(ctx context.Context, limit int) ([]domain.Correction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email_subject, email_body, COALESCE(wrong_status,''), COALESCE(correct_status,''), COALESCE(command,'')
		FROM corrections ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var corrections []domain.Correction
	for rows.Next() {
		var c domain.Correction
		if err := rows.Scan(&c.EmailSubject, &c.EmailBody, &c.WrongStatus, &c.CorrectStatus, &c.Command); err != nil {
			return nil, err
		}
		corrections = append(corrections, c)
	}
	return corrections, rows.Err()
}

// GetThreadApplicationID returns the application_id already linked to any email in
// the given thread, or 0 if the thread is not yet associated with an application.
func (s *Store) GetThreadApplicationID(ctx context.Context, threadID string) int64 {
	if threadID == "" {
		return 0
	}
	var appID int64
	_ = s.db.QueryRowContext(ctx,
		`SELECT application_id FROM thread_emails WHERE thread_id=$1 AND application_id IS NOT NULL LIMIT 1`,
		threadID).Scan(&appID)
	return appID
}

// StoreThreadEmail persists an email into thread_emails linked to the given application.
// Emails without a known application (applicationID == 0) are silently dropped — only
// definitively-linked emails belong in this table.
func (s *Store) StoreThreadEmail(ctx context.Context, emailID, threadID, from, subject, body string, date time.Time, applicationID int64) error {
	if applicationID == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO thread_emails (email_id, thread_id, from_addr, subject, body, email_date, application_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (email_id) DO UPDATE SET body = EXCLUDED.body WHERE thread_emails.body = ''`,
		emailID, threadID, from, subject, body, date, applicationID)
	return err
}

func (s *Store) LinkThreadEmailToStage(ctx context.Context, emailID string, stageID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE thread_emails SET stage_id=$1 WHERE email_id=$2`,
		stageID, emailID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("thread email not found: email_id=%s", emailID)
	}
	return nil
}

// TagThreadEmailsForApplication re-tags all emails in a thread to the given application.
// Used by the promote endpoint when the user explicitly assigns a thread to an application.
func (s *Store) TagThreadEmailsForApplication(ctx context.Context, threadID string, applicationID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE thread_emails SET application_id=$1 WHERE thread_id=$2`,
		applicationID, threadID)
	return err
}

func (s *Store) GetThreadEmailSubjectAndBody(ctx context.Context, emailID string) (subject, body string) {
	if emailID == "" {
		return "", ""
	}
	_ = s.db.QueryRowContext(ctx, `SELECT subject, body FROM thread_emails WHERE email_id=$1`, emailID).Scan(&subject, &body)
	return subject, body
}

func (s *Store) GetThreadEmailSubjectBodyDate(ctx context.Context, emailID string) (subject, body string, emailDate time.Time, err error) {
	if emailID == "" {
		return "", "", time.Time{}, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT subject, body, email_date FROM thread_emails WHERE email_id=$1`, emailID).Scan(&subject, &body, &emailDate)
	if err == sql.ErrNoRows {
		return "", "", time.Time{}, nil
	}
	return
}

func (s *Store) GetJourney(ctx context.Context, applicationID int64) ([]domain.ThreadConversation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT te.id, te.email_id, te.thread_id, te.stage_id, te.from_addr, te.subject, te.body, te.email_date
		FROM thread_emails te
		WHERE te.application_id = $1
		   OR te.thread_id IN (
		       SELECT DISTINCT thread_id FROM thread_emails
		       WHERE application_id = $1 AND thread_id != ''
		   )
		ORDER BY te.email_date ASC`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var emails []domain.ThreadEmail
	for rows.Next() {
		var e domain.ThreadEmail
		var stageID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.EmailID, &e.ThreadID, &stageID,
			&e.FromAddr, &e.Subject, &e.Body, &e.EmailDate); err != nil {
			return nil, err
		}
		if stageID.Valid {
			e.StageID = &stageID.Int64
		}
		emails = append(emails, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groupEmailsByStage(emails), nil
}

func groupEmailsByStage(emails []domain.ThreadEmail) []domain.ThreadConversation {
	var stageIdx []int
	for i, e := range emails {
		if e.StageID != nil {
			stageIdx = append(stageIdx, i)
		}
	}
	if len(stageIdx) == 0 {
		return []domain.ThreadConversation{}
	}
	result := make([]domain.ThreadConversation, len(stageIdx))
	for k, si := range stageIdx {
		result[k] = domain.ThreadConversation{Stage: emails[si], Conversation: []domain.ThreadEmail{}}
	}
	for i, e := range emails {
		if e.StageID != nil {
			continue
		}
		groupIdx := 0
		for k, si := range stageIdx {
			if si <= i {
				groupIdx = k
			}
		}
		result[groupIdx].Conversation = append(result[groupIdx].Conversation, e)
	}
	return result
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

// FixAppliedStageDate backdates an applied stage if it falls after an existing interview.
func (s *Store) FixAppliedStageDate(ctx context.Context, applicationID, appliedStageID int64) error {
	var earliest time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(applied_at) FROM application_stages
		WHERE application_id=$1 AND status IN ('interview','ai_interview')`,
		applicationID,
	).Scan(&earliest)
	if err != nil || earliest.IsZero() {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE application_stages SET applied_at=$1, updated_at=NOW()
		WHERE id=$2 AND applied_at > $1 AND last_email_id = ''`,
		earliest.Add(-1*time.Hour), appliedStageID)
	return err
}

// FixAllAppliedStageDates fixes applied stage dates across all applications.
func (s *Store) FixAllAppliedStageDates(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE application_stages AS ap
		SET applied_at = earliest.min_interview - INTERVAL '1 hour', updated_at = NOW()
		FROM (
			SELECT application_id, MIN(applied_at) AS min_interview
			FROM application_stages
			WHERE status IN ('interview', 'ai_interview')
			GROUP BY application_id
		) AS earliest
		WHERE ap.application_id = earliest.application_id
		AND ap.status = 'applied'
		AND ap.applied_at > earliest.min_interview
		AND ap.last_email_id = ''`); err != nil {
		return err
	}
	// Sync applications.applied_at to match the earliest stage date.
	_, err := s.db.ExecContext(ctx, `
		UPDATE applications a
		SET applied_at = (
			SELECT MIN(s.applied_at) FROM application_stages s WHERE s.application_id = a.id
		), updated_at = NOW()
		WHERE EXISTS (SELECT 1 FROM application_stages s WHERE s.application_id = a.id)
		AND a.applied_at != (
			SELECT MIN(s.applied_at) FROM application_stages s WHERE s.application_id = a.id
		)`)
	return err
}
