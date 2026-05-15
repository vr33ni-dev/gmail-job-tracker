package sync

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/gmail"
	llm "github.com/vr33ni-dev/gmail-job-tracker/internal/llm"
)

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

	// move all emails belonging to interview applications to Jobs label
	emailIDs, err := s.store.GetEmailIDsForInterviewApplications(ctx)
	if err != nil {
		log.Printf("label move: fetch email IDs: %v", err)
	} else if len(emailIDs) > 0 {
		if err := s.gmail.BatchMoveToLabel(ctx, emailIDs, "Jobs"); err != nil {
			log.Printf("label move: batch modify: %v", err)
		} else {
			log.Printf("label move: moved %d emails to Jobs label", len(emailIDs))
		}
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
				Company:     app.Company,
				Role:        app.Role,
				Platform:    app.Platform,
				AppliedAt:   earliest.Add(-1 * time.Hour),
				Status:      domain.StatusApplied,
				EmailBody:   "⚠️ Application confirmation email not found. This entry was automatically created by the self-healing sync.",
				Language:    app.Language,
				NeedsReview: true,
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
	log.Printf("processing email %s: subject=%q from=%q", email.ID, email.Subject, email.From)
	if processed, err := s.store.IsEmailProcessed(ctx, email.ID); err != nil || processed {
		return err
	}

	// skip emails sent by the user
	// handle sent emails — skip all
	if s.isSentByUser(email.From) {
		log.Printf("skipping sent email from self")
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	// skip reminders and noise before doing any DB work
	if isReminder(email.Body) || isNoise(email.From) {
		log.Printf("skipping noise/reminder email: %s (reminder=%v noise=%v)",
			email.Subject, isReminder(email.Body), isNoise(email.From))
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	parsed, err := s.llm.ParseJobEmail(ctx, email.Subject, stripHTML(email.Body), email.From)
	if err != nil {
		log.Printf("skipping email %s: %v", email.ID, err)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	log.Printf("parsed email %s: company=%s role=%s status=%s confidence=%s",
		email.ID, parsed.Company, parsed.Role, parsed.Status, parsed.Confidence)

	if parsed.Confidence == "low" {
		if hasInterviewLink(email.Body) {
			log.Printf("overriding low confidence — interview link detected in %s", email.ID)
			parsed.Status = domain.StatusInterview
			parsed.Confidence = "medium"
			// extract company from sender domain as fallback
			if parsed.Company == "" {
				parsed.Company = extractDomainCompany(email.From)
				if parsed.Company == "" {
					parsed.Company = extractCompanyFromBody(email.Body)
				}
			}
		} else {
			log.Printf("skipping low confidence email %s", email.ID)
			return s.store.MarkEmailProcessed(ctx, email.ID)
		}
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

	// create new row for this status stage
	app := &domain.Application{
		Company:     s.store.ResolveCompanyAlias(ctx, parsed.Company),
		Role:        domain.NormalizeRole(parsed.Role),
		Platform:    parsed.Platform,
		AppliedAt:   appliedAt,
		Status:      parsed.Status,
		LastEmailID: email.ID,
		EmailBody:   stripHTML(email.Body),
		Language:    parsed.Language,
	}
	if err := s.store.UpsertApplication(ctx, app); err != nil {
		return err
	}

	// // move to Jobs label in Gmail
	// if err := s.gmail.MoveToLabel(ctx, email.ID, "Jobs"); err != nil {
	// 	log.Printf("warning: could not move email %s to Jobs label: %v", email.ID, err)
	// }

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
		strings.Contains(lower, "i'll have a new date") ||
		strings.Contains(lower, "thanks for filling in this form") ||
		strings.Contains(lower, "you're receiving this email because you filled in") ||
		strings.Contains(lower, "thanks for filling in this form") ||
		strings.Contains(lower, "you're receiving this email because you filled in") ||
		strings.Contains(lower, "thank you for submitting your questionnaire") ||
		strings.Contains(lower, "thanks for submitting your questionnaire")
}

func (s *Service) isSentByUser(from string) bool {
	from = strings.ToLower(from)
	if s.userEmail != "" && strings.Contains(from, strings.ToLower(s.userEmail)) {
		return true
	}
	if s.userName != "" && strings.Contains(from, strings.ToLower(s.userName)) {
		return true
	}
	return false
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

func stripHTML(body string) string {
	// remove style/script blocks entirely
	re := regexp.MustCompile(`(?is)<(style|script)[^>]*>.*?</(style|script)>`)
	body = re.ReplaceAllString(body, "")
	// remove links but keep their text: <a href="...">text</a> → text
	re = regexp.MustCompile(`(?i)<a[^>]*href=[^>]*>(.*?)</a>`)
	body = re.ReplaceAllString(body, "$1")
	// remove bare URLs
	re = regexp.MustCompile(`https?://\S+`)
	body = re.ReplaceAllString(body, "")
	// replace block elements with newlines
	re = regexp.MustCompile(`(?i)<(br|p|div|tr|li)[^>]*>`)
	body = re.ReplaceAllString(body, "\n")
	// remove all remaining tags
	re = regexp.MustCompile(`<[^>]+>`)
	body = re.ReplaceAllString(body, "")
	// decode common HTML entities
	body = strings.ReplaceAll(body, "&amp;", "&")
	body = strings.ReplaceAll(body, "&lt;", "<")
	body = strings.ReplaceAll(body, "&gt;", ">")
	body = strings.ReplaceAll(body, "&nbsp;", " ")
	body = strings.ReplaceAll(body, "&#8203;", "")
	body = strings.ReplaceAll(body, "&quot;", "\"")
	// collapse multiple blank lines
	re = regexp.MustCompile(`\n{3,}`)
	body = re.ReplaceAllString(body, "\n\n")
	return strings.TrimSpace(body)
}

// if email contains a meet/zoom/calendar link, force interview classification
func hasInterviewLink(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "meet.google.com") ||
		strings.Contains(lower, "zoom.us") ||
		strings.Contains(lower, "teams.microsoft.com") ||
		strings.Contains(lower, "calendly.com") ||
		strings.Contains(lower, "cal.com/")
}

var schedulingDomains = map[string]bool{
	"cal":        true,
	"calendly":   true,
	"zoom":       true,
	"teams":      true,
	"meet":       true,
	"greenhouse": true,
	"lever":      true,
}

func extractDomainCompany(from string) string {
	re := regexp.MustCompile(`@([^.>]+)`)
	matches := re.FindStringSubmatch(strings.ToLower(from))
	if len(matches) > 1 {
		domain := matches[1]
		if schedulingDomains[domain] {
			return ""
		}
		return domain
	}
	return ""
}

func extractCompanyFromBody(body string) string {
	// look for email addresses in body and extract non-scheduling domains
	re := regexp.MustCompile(`[\w.]+@([\w.-]+\.\w+)`)
	matches := re.FindAllStringSubmatch(strings.ToLower(body), -1)
	for _, m := range matches {
		if len(m) > 1 {
			domain := m[1]
			// skip common non-company domains
			skip := []string{"gmail.com", "cal.com", "google.com", "zoom.us", "microsoft.com", "calendly.com"}
			isSkip := false
			for _, s := range skip {
				if strings.Contains(domain, s) {
					isSkip = true
					break
				}
			}
			if !isSkip {
				// return just the company part e.g. "pelo.tech" -> "pelo"
				parts := strings.Split(domain, ".")
				if len(parts) > 0 {
					return parts[0]
				}
			}
		}
	}
	return ""
}
