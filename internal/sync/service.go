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

var statusPriority = map[domain.Status]int{
	domain.StatusApplied:     1,
	domain.StatusNoResponse:  2,
	domain.StatusReviewing:   3,
	domain.StatusScreening:   4,
	domain.StatusAIInterview: 5,
	domain.StatusOffer:       7,
	domain.StatusRejected:    8,
	domain.StatusWithdrawn:   8,
}

func shouldUpdateStatus(current, next domain.Status) bool {
	return statusPriority[next] > statusPriority[current]
}

type Service struct {
	store *db.Store
	gmail *gmail.Client
	llm   *llm.Client
}

func NewService(store *db.Store, g *gmail.Client, c *llm.Client) *Service {
	return &Service{store: store, gmail: g, llm: c}
}

func (s *Service) Run(ctx context.Context) error {
	since, err := s.store.LastPollTime(ctx)
	if err != nil {
		since = time.Now().Add(-30 * 24 * time.Hour)
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
	return nil
}

func (s *Service) processEmail(ctx context.Context, email gmail.Email) error {
	if processed, err := s.store.IsEmailProcessed(ctx, email.ID); err != nil || processed {
		return err
	}

	parsed, err := s.llm.ParseJobEmail(ctx, email.Subject, email.Body, email.From)
	if err != nil {
		log.Printf("skipping email %s: %v", email.ID, err)
		return s.store.MarkEmailProcessed(ctx, email.ID) // mark as processed so it doesn't retry forever
	}

	log.Printf("parsed email %s: company=%s role=%s status=%s confidence=%s",
		email.ID, parsed.Company, parsed.Role, parsed.Status, parsed.Confidence)

	if parsed.Confidence == "low" {
		log.Printf("skipping low confidence email %s", email.ID)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	app, err := s.store.FindByCompanyAndRole(ctx, parsed.Company, parsed.Role)
	if err != nil {
		return err
	}

	if app == nil {
		appliedAt := email.Date
		if appliedAt.IsZero() {
			log.Printf("warning: could not parse date for email %s", email.ID)
			appliedAt = time.Now()
		}
		app = &domain.Application{
			Company:     parsed.Company,
			Role:        parsed.Role,
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
	} else if shouldUpdateStatus(app.Status, parsed.Status) {
		event := &domain.StatusEvent{
			ApplicationID: app.ID,
			FromStatus:    app.Status,
			ToStatus:      parsed.Status,
			EmailID:       email.ID,
			EmailSubject:  email.Subject,
		}
		if err := s.store.RecordStatusEvent(ctx, event); err != nil {
			return err
		}
		if err := s.store.UpdateStatus(ctx, app.ID, parsed.Status, email.ID); err != nil {
			return err
		}
	}

	return s.store.MarkEmailProcessed(ctx, email.ID)
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
