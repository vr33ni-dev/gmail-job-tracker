package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/gmail"
)

type emailParser interface {
	ParseJobEmail(ctx context.Context, subject, body, from string, existingStages []domain.ApplicationStage) (domain.ParsedEmail, error)
}

type syncStore interface {
	GetAllJobEmailIDs(ctx context.Context) ([]string, error)
	GetAllJobThreadIDs(ctx context.Context) ([]string, error)
	LastPollTime(ctx context.Context) (time.Time, error)
	GetRecentCorrections(ctx context.Context, limit int) ([]domain.Correction, error)

	GetThreadApplicationID(ctx context.Context, threadID string) int64
	StoreThreadEmail(ctx context.Context, emailID, threadID, from, subject, body string, date time.Time, applicationID int64) error
	LinkThreadEmailToStage(ctx context.Context, emailID string, stageID int64) error

	IsEmailProcessed(ctx context.Context, emailID string) (bool, error)
	MarkEmailProcessed(ctx context.Context, emailID string) error
	UnmarkEmailProcessed(ctx context.Context, emailID string) error
	IsEmailInThreadEmails(ctx context.Context, emailID string) (bool, error)

	FindOrCreateApplication(ctx context.Context, company, role, platform, language, source string, appliedAt time.Time) (int64, error)
	ApplicationExistsByEmailID(ctx context.Context, emailID string) (bool, error)
	GetStageByLastEmailID(ctx context.Context, emailID string) (*domain.ApplicationStage, error)

	ListApplications(ctx context.Context) ([]domain.Application, error)
	FindApplicationById(ctx context.Context, id int64) (*domain.Application, error)
	FindApplicationByCompanyAndRole(ctx context.Context, company, role string) (*domain.Application, error)
	FindMostRecentByCompany(ctx context.Context, company string) (*domain.Application, error)
	FindApplicationByPersonName(ctx context.Context, name string) (*domain.Application, error)

	GetStagesByStatus(ctx context.Context, applicationID int64, status domain.Status) ([]domain.ApplicationStage, error)
	CreateStage(ctx context.Context, applicationID int64, status domain.Status, emailID string, inferred bool, appliedAt time.Time) (int64, error)
	HasAppliedStage(ctx context.Context, applicationID int64) (bool, error)
	FixAppliedStageDate(ctx context.Context, applicationID, stageID int64) error

	GetContactFromAddrsForApp(ctx context.Context, appID int64, userEmail string) ([]string, error)
	GetThreadIDsByApplication(ctx context.Context, applicationID int64) ([]string, error)
	FixEmptyRoles(ctx context.Context) error
	FixAllAppliedStageDates(ctx context.Context) error

	ResolveCompanyAlias(ctx context.Context, company string) string
}

type Service struct {
	store     syncStore
	gmail     *gmail.Client
	llm       emailParser
	userEmail string
	userName  string
}

func NewService(store syncStore, g *gmail.Client, c emailParser, userEmail, userName string) *Service {
	return &Service{
		store:     store,
		gmail:     g,
		llm:       c,
		userEmail: userEmail,
		userName:  userName,
	}
}

func (s *Service) SyncAll(ctx context.Context) error {
	since, err := s.store.LastPollTime(ctx)
	outputLabel := os.Getenv("OUTPUT_LABEL")

	if err != nil {
		since = time.Now().Add(-120 * 24 * time.Hour)
	}
	// roll back 7 days to catch emails missed by previous syncs (safe — processed_emails prevents double-processing)
	since = since.Add(-7 * 24 * time.Hour)
	log.Printf("SyncAll(): polling gmail since %s", since.Format(time.DateOnly))
	if corrections, err := s.store.GetRecentCorrections(ctx, 20); err == nil {
		log.Printf("llm: %d correction(s) active", len(corrections))
	}

	emails, err := s.gmail.FetchJobEmails(ctx, since)
	if err != nil {
		log.Printf("SyncAll(): fetch emails error: %v", err)
		return err
	}

	log.Printf("SyncAll(): fetched %d emails", len(emails))
	sort.Slice(emails, func(i, j int) bool { return emails[i].Date.Before(emails[j].Date) })

	affectedCompanies := map[string]bool{}
	for _, email := range emails {
		company, err := s.processEmail(ctx, email)
		if err != nil {
			if strings.Contains(err.Error(), "usage limits") {
				log.Printf("SyncAll(): rate limited, stopping sync — will resume on next run")
				return nil
			}
			log.Printf("SyncAll(): error processing %s: %v", email.ID, err)
		}
		if company != "" {
			affectedCompanies[company] = true
		}
	}
	log.Printf("SyncAll(): sync complete — processed %d emails", len(emails))

	if len(affectedCompanies) > 0 {
		companies := make([]string, 0, len(affectedCompanies))
		for company := range affectedCompanies {
			companies = append(companies, company)
		}
		if err := s.SelfHealForCompanies(ctx, companies); err != nil {
			log.Printf("SyncAll(): self-heal error: %v", err)
		}
		// label + archive only when something changed
		s.labelAndArchive(ctx, outputLabel)
	}

	return nil
}

func (s *Service) labelAndArchive(ctx context.Context, outputLabel string) {
	// add label to all known message IDs (reliable — uses exact message IDs)
	emailIDs, err := s.store.GetAllJobEmailIDs(ctx)

	if err != nil {
		log.Printf("SyncAll(): Error fetching emailIDs %v", err)
	} else if len(emailIDs) > 0 {
		if err := s.gmail.BatchMoveToLabel(ctx, emailIDs, outputLabel); err != nil {
			log.Printf("SyncAll(): Error moving messages to label %s: %v", outputLabel, err)
		} else {
			log.Printf("SyncAll(): Batch moved %d messages to label %s", len(emailIDs), outputLabel)
		}
	}

	// archive whole threads out of inbox (thread-level, removes INBOX from all messages in conversation)
	threadIDs, err := s.store.GetAllJobThreadIDs(ctx)
	if err != nil {
		log.Printf("SyncAll(): Error fetching thread IDs %v", err)
	} else if len(threadIDs) > 0 {
		if err := s.gmail.ArchiveThreads(ctx, threadIDs); err != nil {
			log.Printf("SyncAll(): Error archiving threads %v", err)
		} else {
			log.Printf("SyncAll(): Archived %d threads", len(threadIDs))
		}
	}
}

// BackfillThreads fetches all messages from Gmail for each thread belonging to
// the given application and stores them in thread_emails. Called on demand when
// the user opens an application's journey view.
func (s *Service) BackfillThreads(ctx context.Context, applicationID int64) error {
	threadIDs, err := s.store.GetThreadIDsByApplication(ctx, applicationID)
	if err != nil {
		return err
	}
	for _, tid := range threadIDs {
		emails, err := s.gmail.FetchThreadEmails(ctx, tid)
		if err != nil {
			log.Printf("thread backfill %s: %v", tid, err)
			continue
		}
		for _, email := range emails {
			_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, stripHTML(email.Body), email.Date, applicationID)
		}
	}
	return nil
}

func (s *Service) SelfHealForCompanies(ctx context.Context, companies []string) error {
	allApps, err := s.store.ListApplications(ctx)
	if err != nil {
		return fmt.Errorf("self-heal for companies %v: failed to list applications: %w", companies, err)
	}

	var apps []domain.Application
	for _, app := range allApps {
		for _, company := range companies {
			if strings.EqualFold(domain.NormalizeCompany(app.Company), domain.NormalizeCompany(company)) {
				apps = append(apps, app)
				break
			}
		}
	}

	s.selfHealSchedulingEmails(ctx, apps)

	// reload so selfHealAppliedStages sees any stages added by scheduling heal
	allApps, err = s.store.ListApplications(ctx)
	if err != nil {
		return fmt.Errorf("self-heal for companies %v: failed to reload applications: %w", companies, err)
	}
	apps = apps[:0]
	for _, app := range allApps {
		for _, company := range companies {
			if strings.EqualFold(domain.NormalizeCompany(app.Company), domain.NormalizeCompany(company)) {
				apps = append(apps, app)
				break
			}
		}
	}
	s.selfHealAppliedStages(ctx, apps)
	log.Printf("self-heal: complete for %v", companies)

	// no data quality fixes here — save those for full heal
	return nil
}

func (s *Service) SelfHeal(ctx context.Context) error {
	log.Printf("running self-healing check...")

	apps, err := s.store.ListApplications(ctx)
	if err != nil {
		return err
	}

	s.selfHealSchedulingEmails(ctx, apps)

	// reload so selfHealAppliedStages sees any stages added by scheduling heal
	apps, err = s.store.ListApplications(ctx)
	if err != nil {
		return err
	}
	s.selfHealAppliedStages(ctx, apps)

	if err := s.store.FixEmptyRoles(ctx); err != nil {
		log.Printf("self-heal: fix roles error: %v", err)
	}
	if err := s.store.FixAllAppliedStageDates(ctx); err != nil {
		log.Printf("self-heal: fix applied dates error: %v", err)
	}

	log.Printf("self-healing complete")
	return nil
}

// selfHealAppliedStages runs applied-stage backfill and scheduling email recovery for
// a given set of apps. Called by SelfHeal (all apps) and SyncCompany (one company).
func (s *Service) selfHealAppliedStages(ctx context.Context, apps []domain.Application) {
	for _, app := range apps {
		stageInfo := getStageInfo(app.Stages)

		if stageInfo.hasApplied || len(app.Stages) == 0 {
			continue
		}

		log.Printf("self-heal: %s/%s has no applied stage, searching 1 month back", app.Company, app.Role)

		searchFrom := stageInfo.earliest.Add(-30 * 24 * time.Hour)
		emails, err := s.gmail.FetchJobEmailsForCompany(ctx, app.Company, searchFrom)
		if err != nil {
			log.Printf("self-heal fetch error for %s: %v", app.Company, err)
			continue
		}

		for _, email := range emails {
			_, err := s.processEmail(ctx, email)
			if err != nil {
				log.Printf("self-heal process error %s: %v", email.ID, err)
			}
		}

		found, err := s.store.HasAppliedStage(ctx, app.ID)
		if err != nil {
			log.Printf("self-heal: error checking applied stage for %s: %v", app.Company, err)
		} else if found {
			log.Printf("self-heal: found applied for %s", app.Company)
		} else {
			log.Printf("self-heal: no applied confirmation email found for %s — creating inferred placeholder", app.Company)
			if _, err := s.store.CreateStage(ctx, app.ID, domain.StatusApplied, "", true, stageInfo.earliest.Add(-1*time.Hour)); err != nil {
				log.Printf("self-heal: failed to create placeholder for %s: %v", app.Company, err)
			}
		}

	}
}

// selfHealSchedulingEmails finds Cal.com/Calendly invites that were missed because
// they don't mention the company name. For each app with an interview stage but no
// scheduling service email yet, it searches Gmail by the known contact names.
func (s *Service) selfHealSchedulingEmails(ctx context.Context, apps []domain.Application) {
	for _, app := range apps {
		stageInfo := getStageInfo(app.Stages)

		if !stageInfo.hasInterview {
			continue
		}
		fromAddrs, err := s.store.GetContactFromAddrsForApp(ctx, app.ID, s.getUserEmail())
		if err != nil || len(fromAddrs) == 0 {
			continue
		}

		searchFrom := stageInfo.earliest.Add(-30 * 24 * time.Hour)
		for _, fromAddr := range fromAddrs {
			s.processSchedulingEmailsForContact(ctx, fromAddr, app, searchFrom)
		}
	}
}

func (s *Service) processEmail(ctx context.Context, email gmail.Email) (string, error) {
	if processed, err := s.store.IsEmailProcessed(ctx, email.ID); err != nil || processed {
		if processed {
			log.Printf("skipping already-processed email %s", email.ID)
		}
		return "", err
	}

	strippedBody := stripHTML(email.Body)

	// check if this thread is already linked to an application — if so we can store immediately
	var threadAppID int64
	if email.ThreadID != "" {
		threadAppID = s.store.GetThreadApplicationID(ctx, email.ThreadID)
		if threadAppID != 0 {
			_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, strippedBody, email.Date, threadAppID)
		}
	}

	if s.isSentByUser(email.From) {
		log.Printf("skipping sent email from self: %s thread=%s", email.ID, email.ThreadID)
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}

	if isNoise(email.From) {
		log.Printf("skipping noise email: %s from=%s", email.Subject, email.From)
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}

	if isGmailReaction(email.Subject, strippedBody) {
		log.Printf("skipping gmail reaction: %s", email.Subject)
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}

	parsed, err := s.llm.ParseJobEmail(ctx, email.Subject, strippedBody, email.From, nil)
	if err != nil {
		if strings.Contains(err.Error(), "usage limits") {
			return "", err // propagate so the outer loop can stop and leave this email unprocessed
		}
		log.Printf("skipping email %s: %v", email.ID, err)
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}

	log.Printf("parsed email %s: company=%s role=%s status=%s confidence=%s",
		email.ID, parsed.Company, parsed.Role, parsed.Status, parsed.Confidence)

	// heuristic rejection override — Ollama often misses rejections; check body keywords
	if parsed.Status != domain.StatusRejected && hasRejectionKeywords(email.Body) {
		log.Printf("overriding to rejected — rejection keywords detected in %s", email.ID)
		parsed.Status = domain.StatusRejected
		parsed.Confidence = "high"
	}

	// fallback company extraction from subject when LLM returned empty ("…at Acto", "…bei Acto")
	if parsed.Company == "" {
		parsed.Company = extractCompanyFromSubject(email.Subject)
	}

	// scheduling language in body signals an interview invite even if the LLM missed it
	if hasSchedulingLanguage(email.Body) && (parsed.Confidence == "low" || parsed.Status == domain.StatusApplied) {
		log.Printf("overriding to interview — scheduling language detected in %s", email.ID)
		parsed.Status = domain.StatusInterview
		parsed.Confidence = "medium"
		if parsed.Company == "" {
			parsed.Company = extractDomainCompany(email.From)
		}
	}

	// a scheduling/meeting platform link in an applied email is a misclassification — it's an interview invite
	if hasInterviewLink(email.Body) && parsed.Status == domain.StatusApplied {
		log.Printf("overriding applied to interview — scheduling link detected in %s", email.ID)
		parsed.Status = domain.StatusInterview
		parsed.Confidence = "medium"
		if parsed.Company == "" {
			parsed.Company = extractDomainCompany(email.From)
		}
	}

	if parsed.Confidence == "low" {
		if hasInterviewLink(email.Body) {
			log.Printf("overriding low confidence — interview link detected in %s", email.ID)
			parsed.Status = domain.StatusInterview
			parsed.Confidence = "medium"
			if parsed.Company == "" {
				parsed.Company = extractDomainCompany(email.From)
				if parsed.Company == "" {
					parsed.Company = extractCompanyFromBody(strippedBody)
				}
			}
		} else {
			// try to store the email against a known app even though we're skipping
			if email.ThreadID != "" && threadAppID == 0 {
				company := parsed.Company
				if company == "" {
					company = extractDomainCompany(email.From)
				}
				company = domain.NormalizeCompany(s.store.ResolveCompanyAlias(ctx, company))
				role := domain.NormalizeRole(parsed.Role)
				if role == "" {
					if existing, err := s.store.FindMostRecentByCompany(ctx, company); err == nil && existing != nil {
						role = existing.Role
					}
				}
				log.Printf("low confidence %s: attempting thread store company=%q role=%q thread=%s", email.ID, company, role, email.ThreadID)
				if existingApp, err := s.store.FindApplicationByCompanyAndRole(ctx, company, role); err == nil && existingApp != nil {
					log.Printf("low confidence %s: storing in thread %s for app %d", email.ID, email.ThreadID, existingApp.ID)
					_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, stripHTML(email.Body), email.Date, existingApp.ID)
				} else {
					log.Printf("low confidence %s: no app found — not storing in thread_emails", email.ID)
				}
			}
			log.Printf("skipping low confidence email %s", email.ID)
			return "", s.store.MarkEmailProcessed(ctx, email.ID)
		}
	}

	// skip emails where LLM extracted an obviously invalid company name.
	// Exception: scheduling service emails with a meeting link — try matching by
	// person name extracted from the subject before giving up.
	if isInvalidCompany(parsed.Company) {
		if isSchedulingService(email.From) && hasInterviewLink(email.Body) {
			names := extractNamesFromSchedulingEmail(email.Subject, stripHTML(email.Body), s.userName)
			for _, name := range names {
				if app, err := s.store.FindApplicationByPersonName(ctx, name); err == nil && app != nil {
					log.Printf("name-based match: %q → app %d (%s/%s)", name, app.ID, app.Company, app.Role)
					parsed.Company = app.Company
					parsed.Role = app.Role
					parsed.Status = domain.StatusInterview
					parsed.Confidence = "medium"
					break
				}
			}
		}
		if isInvalidCompany(parsed.Company) {
			log.Printf("skipping email %s: invalid company name %q", email.ID, parsed.Company)
			return "", s.store.MarkEmailProcessed(ctx, email.ID)
		}
	}

	company := domain.NormalizeCompany(s.store.ResolveCompanyAlias(ctx, parsed.Company))
	role := domain.NormalizeRole(parsed.Role)

	// if role is empty, try to inherit from existing entry for same company
	if role == "" {
		if existing, err := s.store.FindMostRecentByCompany(ctx, company); err == nil && existing != nil && existing.Role != "" {
			role = existing.Role
			log.Printf("inherited role %q from existing entry for %s", role, company)
		}
	}

	var existingApp *domain.Application
	if threadAppID != 0 {
		if threadApp, err := s.store.FindApplicationById(ctx, threadAppID); err == nil && threadApp != nil {
			existingApp = threadApp
			if company == "" {
				company = existingApp.Company
			}
			if role == "" {
				role = existingApp.Role
			}
			log.Printf("thread %s already belongs to app %d; using existing app values", email.ThreadID, existingApp.ID)
		} else {
			log.Printf("warning: thread %s mapped to application %d but could not load app", email.ThreadID, threadAppID)
		}
	}

	if existingApp == nil {
		existingApp, _ = s.store.FindApplicationByCompanyAndRole(ctx, company, role)
	}

	// When Ollama extracts a slightly different role name for the same position, the exact
	// company+role lookup above misses and would create a duplicate application.
	// - Interview emails: route to existing app only if it already has interview stages.
	// - Rejected/offer emails: route to the most recent app for the company (terminal
	//   states always belong to an existing application, not a new one).
	if existingApp == nil && threadAppID == 0 {
		switch parsed.Status {
		case domain.StatusInterview, domain.StatusAIInterview:
			if candidate, err := s.store.FindMostRecentByCompany(ctx, company); err == nil && candidate != nil {
				stages, _ := s.store.GetStagesByStatus(ctx, candidate.ID, parsed.Status)
				if len(stages) > 0 {
					existingApp = candidate
					log.Printf("company-level dedup: routing interview %s to app %d (%s/%s)",
						email.ID, candidate.ID, candidate.Company, candidate.Role)
				}
			}
		case domain.StatusRejected, domain.StatusOffer:
			if candidate, err := s.store.FindMostRecentByCompany(ctx, company); err == nil && candidate != nil {
				existingApp = candidate
				log.Printf("company-level dedup: routing %s %s to app %d (%s/%s)",
					parsed.Status, email.ID, candidate.ID, candidate.Company, candidate.Role)
			}
		}
	}

	if existingApp != nil && threadAppID == 0 && email.ThreadID != "" {
		_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, strippedBody, email.Date, existingApp.ID)
		threadAppID = existingApp.ID
	}

	// calendar notifications and reminders: email is now stored, but no stage should be created.
	if isCalendarNotification(email.Body) {
		log.Printf("skipping calendar notification (stored): %s thread=%s", email.Subject, email.ThreadID)
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}
	if isReminder(email.Body) && !hasInterviewLink(email.Body) &&
		parsed.Status != domain.StatusRejected && parsed.Status != domain.StatusOffer {
		log.Printf("skipping reminder (stored): %s", email.Subject)
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}

	// for interview/ai_interview: re-parse with existing stages as context
	if parsed.Status == domain.StatusInterview || parsed.Status == domain.StatusAIInterview {
		if existingApp != nil {
			existingStages, err := s.store.GetStagesByStatus(ctx, existingApp.ID, parsed.Status)
			if err != nil {
				log.Printf("warning: could not fetch existing stages for %s/%s: %v", company, role, err)
			} else if len(existingStages) > 0 {
				// if this email predates all existing interview stages it is the original
				// invite that should have been processed first — never treat it as duplicate
				allNewer := true
				for _, st := range existingStages {
					if !email.Date.Before(st.AppliedAt) {
						allNewer = false
						break
					}
				}
				if !allNewer {
					reparsed, err := s.llm.ParseJobEmail(ctx, email.Subject, strippedBody, email.From, existingStages)
					if err != nil {
						if strings.Contains(err.Error(), "usage limits") {
							return "", err
						}
						log.Printf("warning: duplicate-check parse failed for %s: %v", email.ID, err)
					} else if reparsed.IsDuplicate {
						log.Printf("skipping duplicate %s email for %s/%s: %s", parsed.Status, company, role, email.ID)
						return "", s.store.MarkEmailProcessed(ctx, email.ID)
					}
				}
			}
		}
	}

	// idempotency — email already created a stage; link the thread email to it
	exists, err := s.store.ApplicationExistsByEmailID(ctx, email.ID)
	if err != nil {
		return "", err
	}
	if exists {
		if email.ThreadID != "" {
			if stage, err := s.store.GetStageByLastEmailID(ctx, email.ID); err == nil && stage != nil {
				_ = s.store.LinkThreadEmailToStage(ctx, email.ID, stage.ID)
				// ensure stored with correct appID if not already
				if threadAppID == 0 {
					_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, strippedBody, email.Date, stage.ApplicationID)
				}
			}
		}
		return "", s.store.MarkEmailProcessed(ctx, email.ID)
	}

	appliedAt := email.Date
	if appliedAt.IsZero() {
		log.Printf("warning: could not parse date for email %s", email.ID)
		appliedAt = time.Now()
	}

	// only one applied confirmation per company+role
	if parsed.Status == domain.StatusApplied && existingApp != nil {
		hasApplied, err := s.store.HasAppliedStage(ctx, existingApp.ID)
		if err != nil {
			return "", err
		}
		if hasApplied {
			log.Printf("skipping duplicate applied email for %s/%s: %s", company, role, email.ID)
			return "", s.store.MarkEmailProcessed(ctx, email.ID)
		}
	}

	appID := threadAppID
	if appID == 0 && existingApp != nil {
		appID = existingApp.ID
	}
	if appID == 0 {
		var err error
		appID, err = s.store.FindOrCreateApplication(ctx, company, role, parsed.Platform, parsed.Language, "", appliedAt)
		if err != nil {
			return "", err
		}
	}

	// store thread email now if we haven't yet (first email for a brand-new application)
	if email.ThreadID != "" && threadAppID == 0 {
		_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, strippedBody, email.Date, appID)
	}

	stageID, err := s.store.CreateStage(ctx, appID, parsed.Status, email.ID, false, appliedAt)
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateStage) {
			log.Printf("skipping duplicate %s stage for %s/%s: %s", parsed.Status, company, role, email.ID)
			return "", s.store.MarkEmailProcessed(ctx, email.ID)
		}
		return "", err
	}
	if parsed.Status == domain.StatusApplied {
		if err := s.store.FixAppliedStageDate(ctx, appID, stageID); err != nil {
			log.Printf("warning: could not fix applied stage date for %s: %v", company, err)
		}
	}

	if email.ThreadID != "" {
		_ = s.store.LinkThreadEmailToStage(ctx, email.ID, stageID)
	}

	return company, s.store.MarkEmailProcessed(ctx, email.ID)
}

// getUserEmail returns the cached user email, fetching it from the Gmail profile
// API on first call if it wasn't populated at startup.
func (s *Service) getUserEmail() string {
	if s.userEmail != "" {
		return s.userEmail
	}
	if email, err := s.gmail.GetUserEmail(context.Background()); err == nil {
		s.userEmail = email
	}
	return s.userEmail
}

func (s *Service) isSentByUser(from string) bool {
	fromLower := strings.ToLower(from)
	userEmail := s.getUserEmail()
	if userEmail != "" && strings.Contains(fromLower, strings.ToLower(userEmail)) {
		return true
	}
	if s.userName == "" {
		return false
	}
	userNameLower := strings.ToLower(s.userName)
	if strings.Contains(fromLower, userNameLower) {
		return true
	}
	// extracted display name is a part of the user's name (e.g. "Verena" matches "Verena Lechner")
	display := strings.ToLower(extractDisplayName(from))
	return display != "" && strings.Contains(userNameLower, display)
}

// SyncCompany fetches and processes all job emails for a specific company going back 6 months.
func (s *Service) SyncCompany(ctx context.Context, company string) error {
	since := time.Now().Add(-180 * 24 * time.Hour)
	log.Printf("SyncCompany(): Polling emails for %q since %s", company, since.Format(time.DateOnly))
	emails, err := s.gmail.FetchJobEmailsForCompany(ctx, company, since)
	if err != nil {
		return fmt.Errorf("fetch emails for %s: %w", company, err)
	}
	log.Printf("SyncCompany(): fetched %d emails for %q", len(emails), company)
	sort.Slice(emails, func(i, j int) bool { return emails[i].Date.Before(emails[j].Date) })
	for _, email := range emails {
		_, err = s.processEmail(ctx, email)
		if err != nil {
			log.Printf("SyncCompany(): %s: %v", email.ID, err)
		}
		time.Sleep(300 * time.Millisecond) //rate-limit Gmail API calls
	}
	log.Printf("SyncCompany(): complete for %q", company)

	// self-heal only for this company's apps, not all applications
	if err := s.SelfHealForCompanies(ctx, []string{company}); err != nil {
		log.Printf("company sync: self-heal error: %v", err)
	}
	log.Printf("SyncCompany(): self-healing complete for %q", company)
	return nil
}

func (s *Service) SyncLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SyncAll(ctx); err != nil {
				log.Printf("sync error: %v", err)
			}
		}
	}
}

func (s *Service) processSchedulingEmailsForContact(ctx context.Context, fromAddr string, app domain.Application, searchFrom time.Time) {
	// skip the user's own sent addresses
	if s.isSentByUser(fromAddr) {
		return
	}
	name := extractDisplayName(fromAddr)
	if name == "" {
		return
	}
	log.Printf("self-heal scheduling: searching for %q emails for %s/%s", name, app.Company, app.Role)
	emails, err := s.gmail.FetchJobEmailsForCompany(ctx, name, searchFrom)
	if err != nil {
		log.Printf("self-heal scheduling fetch error for %q: %v", name, err)
		return
	}
	for _, email := range emails {
		if err := s.processSchedulingEmail(ctx, email); err != nil {
			log.Printf("self-heal scheduling process error %s: %v", email.ID, err)
		}
	}
}

func (s *Service) processSchedulingEmail(ctx context.Context, email gmail.Email) error {
	if !isSchedulingService(email.From) {
		return nil // not relevant, skip silently
	}

	if processed, _ := s.store.IsEmailProcessed(ctx, email.ID); processed {
		if linked, _ := s.store.IsEmailInThreadEmails(ctx, email.ID); !linked {
			log.Printf("self-heal scheduling: un-processing previously-dropped email %s", email.ID)
			_ = s.store.UnmarkEmailProcessed(ctx, email.ID)
		} else {
			return nil // already linked, skip
		}
	}

	time.Sleep(300 * time.Millisecond)
	_, err := s.processEmail(ctx, email)
	return err
}
