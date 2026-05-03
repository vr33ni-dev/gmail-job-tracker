package sync

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/gmail"
	llm "github.com/vr33ni-dev/gmail-job-tracker/internal/llm"
)

// how long before we consider a new email with the same status a new stage
const sameStatusWindowDays = 5

type Service struct {
	store     *db.Store
	gmail     *gmail.Client
	llm       *llm.Client
	userEmail string
	userName  string
}

func NewService(store *db.Store, g *gmail.Client, c *llm.Client) *Service {
	ctx := context.Background()
	return &Service{
		store:     store,
		gmail:     g,
		llm:       c,
		userEmail: store.GetSetting(ctx, "user_email"),
		userName:  store.GetSetting(ctx, "user_name"),
	}
}

func (s *Service) Run(ctx context.Context) error {
	since, err := s.store.LastPollTime(ctx)
	if err != nil {
		since = time.Now().Add(-90 * 24 * time.Hour)
	}
	log.Printf("polling gmail since %s", since.Format(time.DateOnly))

	emails, err := s.gmail.FetchJobEmails(ctx, since)
	if err != nil {
		log.Printf("fetch emails error: %v", err)
		return err
	}

	log.Printf("fetched %d emails", len(emails))

	for _, email := range emails {
		if err := s.processEmail(ctx, email); err != nil {
			if strings.Contains(err.Error(), "usage limits") {
				log.Printf("rate limited, stopping sync — will resume on next run")
				return nil
			}
			log.Printf("error processing %s: %v", email.ID, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("sync complete — processed %d emails", len(emails))

	if err := s.SelfHeal(ctx); err != nil {
		log.Printf("self-heal error: %v", err)
	}
	return nil
}

func (s *Service) SelfHeal(ctx context.Context) error {
	log.Printf("running self-healing check...")

	apps, err := s.store.ListGroupedApplications(ctx)
	if err != nil {
		return err
	}

	for _, app := range apps {
		hasApplied := false
		earliest := time.Now()
		for _, stage := range app.Stages {
			if stage.Status == domain.StatusApplied {
				hasApplied = true
				break
			}
			if stage.AppliedAt.Before(earliest) {
				earliest = stage.AppliedAt
			}
		}

		if hasApplied || len(app.Stages) == 0 {
			continue
		}

		log.Printf("self-heal: %s/%s has no applied stage, searching 1 month back", app.Company, app.Role)

		searchFrom := earliest.Add(-30 * 24 * time.Hour)
		emails, err := s.gmail.FetchJobEmailsForCompany(ctx, app.Company, searchFrom)
		if err != nil {
			log.Printf("self-heal fetch error for %s: %v", app.Company, err)
			continue
		}

		for _, email := range emails {
			if err := s.processEmail(ctx, email); err != nil {
				log.Printf("self-heal process error %s: %v", email.ID, err)
			}
		}

		// check if applied was found after processing
		found, err := s.store.HasAppliedStage(ctx, app.Company, app.Role)
		if err != nil || !found {
			log.Printf("self-heal: no applied found for %s — creating placeholder", app.Company)
			placeholder := &domain.Application{
				Company:   app.Company,
				Role:      app.Role,
				Platform:  app.Platform,
				AppliedAt: earliest.Add(-1 * time.Hour),
				Status:    domain.StatusApplied,
				EmailBody: "⚠️ Application confirmation email not found. This entry was automatically created by the self-healing sync.",
				Language:  app.Language,
			}
			if err := s.store.UpsertApplication(ctx, placeholder); err != nil {
				log.Printf("self-heal: failed to create placeholder for %s: %v", app.Company, err)
			}
		} else {
			log.Printf("self-heal: found applied for %s", app.Company)
		}
	}

	// after all processing, fix any remaining empty roles
	if err := s.store.FixEmptyRoles(ctx); err != nil {
		log.Printf("self-heal: fix roles error: %v", err)
	}

	log.Printf("self-healing complete")
	return nil
}

func (s *Service) processEmail(ctx context.Context, email gmail.Email) error {
	// skip already processed
	if processed, err := s.store.IsEmailProcessed(ctx, email.ID); err != nil || processed {
		return err
	}

	// skip emails sent by the user
	// handle sent emails — skip all
	if isSentByUser(email.From) {
		log.Printf("skipping sent email from self")
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	// skip reminders and noise before doing any DB work
	if isReminder(email.Body) || isNoise(email.From) {
		log.Printf("skipping noise/reminder email: %s", email.Subject)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	parsed, err := s.llm.ParseJobEmail(ctx, email.Subject, email.Body, email.From)
	if err != nil {
		log.Printf("skipping email %s: %v", email.ID, err)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	log.Printf("parsed email %s: company=%s role=%s status=%s confidence=%s",
		email.ID, parsed.Company, parsed.Role, parsed.Status, parsed.Confidence)

	if parsed.Confidence == "low" {
		log.Printf("skipping low confidence email %s", email.ID)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	if parsed.Confidence == "low" {
		log.Printf("skipping low confidence email %s", email.ID)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	// if role is empty, try to inherit from existing entry for same company
	if parsed.Role == "" {
		if existing, err := s.store.FindMostRecentByCompany(ctx, parsed.Company); err == nil && existing != nil && existing.Role != "" {
			parsed.Role = existing.Role
			log.Printf("inherited role %q from existing entry for %s", parsed.Role, parsed.Company)
		}
	}

	// idempotency — don't create duplicate if this email already created a row
	exists, err := s.store.ApplicationExistsByEmailID(ctx, email.ID)
	if err != nil {
		return err
	}
	if exists {
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	appliedAt := email.Date
	if appliedAt.IsZero() {
		log.Printf("warning: could not parse date for email %s", email.ID)
		appliedAt = time.Now()
	}

	// find most recent row for this company+role with the same status
	existing, err := s.store.FindByCompanyRoleAndStatus(ctx, parsed.Company, parsed.Role, parsed.Status)
	if err != nil {
		return err
	}

	if existing != nil {
		daysSince := existing.AppliedAt.Sub(appliedAt).Hours() / 24
		if daysSince >= 0 && daysSince < sameStatusWindowDays {
			// existing row is newer — current email came first, this is a duplicate
			log.Printf("skipping older duplicate status %s for %s", parsed.Status, parsed.Company)
			return s.store.MarkEmailProcessed(ctx, email.ID)
		}
		if daysSince < 0 && daysSince > -sameStatusWindowDays {
			// current email is newer — it's a follow-up to existing, skip
			log.Printf("skipping follow-up email for %s/%s", parsed.Company, parsed.Role)
			return s.store.MarkEmailProcessed(ctx, email.ID)
		}
	}

	// create new row for this status stage
	app := &domain.Application{
		Company:     parsed.Company,
		Role:        domain.NormalizeRole(parsed.Role),
		Platform:    parsed.Platform,
		AppliedAt:   appliedAt,
		Status:      parsed.Status,
		LastEmailID: email.ID,
		EmailBody:   email.Body,
		Language:    parsed.Language,
	}
	if err := s.store.UpsertApplication(ctx, app); err != nil {
		return err
	}

	return s.store.MarkEmailProcessed(ctx, email.ID)
}

func isReminder(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "reminder") ||
		strings.Contains(lower, "friendly reminder") ||
		strings.Contains(lower, "last reminder") ||
		strings.Contains(lower, "don't forget") ||
		strings.Contains(lower, "still interested") ||
		strings.Contains(lower, "follow-up on your application") ||
		strings.Contains(lower, "match score") ||
		strings.Contains(lower, "assessment report") ||
		strings.Contains(lower, "talent pool") ||
		strings.Contains(lower, "thanks again for applying") ||
		strings.Contains(lower, "talent pool") ||
		strings.Contains(lower, "i will have a new date") ||
		strings.Contains(lower, "i'll have a new date")
}

func isSentByUser(from string) bool {
	from = strings.ToLower(from)
	return strings.Contains(from, "lechner") || strings.Contains(from, "v.m.s.lechner")
}

func isNoise(from string) bool {
	lower := strings.ToLower(from)
	return strings.Contains(lower, "emailsys1a.net")
}

func (s *Service) RunLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Run(ctx); err != nil {
				log.Printf("sync error: %v", err)
			}
		}
	}
}
