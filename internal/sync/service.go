package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
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

func NewService(store *db.Store, g *gmail.Client, c *llm.Client, userEmail, userName string) *Service {
	return &Service{
		store:     store,
		gmail:     g,
		llm:       c,
		userEmail: userEmail,
		userName:  userName,
	}
}

func (s *Service) Run(ctx context.Context) error {
	since, err := s.store.LastPollTime(ctx)
	outputLabel := os.Getenv("OUTPUT_LABEL")

	if err != nil {
		since = time.Now().Add(-120 * 24 * time.Hour)
	}
	// roll back 7 days to catch emails missed by previous syncs (safe — processed_emails prevents double-processing)
	since = since.Add(-7 * 24 * time.Hour)
	log.Printf("polling gmail since %s", since.Format(time.DateOnly))

	emails, err := s.gmail.FetchJobEmails(ctx, since)
	if err != nil {
		log.Printf("fetch emails error: %v", err)
		return err
	}

	log.Printf("fetched %d emails", len(emails))
	sort.Slice(emails, func(i, j int) bool { return emails[i].Date.Before(emails[j].Date) })

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

	// add Jobs label to all known message IDs (reliable — uses exact message IDs)
	emailIDs, err := s.store.GetAllJobEmailIDs(ctx)
	if err != nil {
		log.Printf("label move: fetch email IDs: %v", err)
	} else if len(emailIDs) > 0 {
		if err := s.gmail.BatchMoveToLabel(ctx, emailIDs, outputLabel); err != nil {
			log.Printf("label move: batch label: %v", err)
		} else {
			log.Printf("label move: labeled %d messages", len(emailIDs))
		}
	}

	// archive whole threads out of inbox (thread-level, removes INBOX from all messages in conversation)
	threadIDs, err := s.store.GetAllJobThreadIDs(ctx)
	if err != nil {
		log.Printf("label move: fetch thread IDs: %v", err)
	} else if len(threadIDs) > 0 {
		if err := s.gmail.ArchiveThreads(ctx, threadIDs); err != nil {
			log.Printf("label move: archive threads: %v", err)
		} else {
			log.Printf("label move: archived %d threads", len(threadIDs))
		}
	}

	return nil
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

func (s *Service) SelfHeal(ctx context.Context) error {
	log.Printf("running self-healing check...")

	apps, err := s.store.ListApplications(ctx)
	if err != nil {
		return err
	}

	s.selfHealApps(ctx, apps)

	if err := s.store.FixEmptyRoles(ctx); err != nil {
		log.Printf("self-heal: fix roles error: %v", err)
	}
	if err := s.store.FixAllAppliedStageDates(ctx); err != nil {
		log.Printf("self-heal: fix applied dates error: %v", err)
	}

	log.Printf("self-healing complete")
	return nil
}

// selfHealApps runs applied-stage backfill and scheduling email recovery for
// a given set of apps. Called by SelfHeal (all apps) and SyncCompany (one company).
func (s *Service) selfHealApps(ctx context.Context, apps []domain.Application) {
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

		if found, err := s.store.HasAppliedStage(ctx, app.ID); err == nil && found {
			log.Printf("self-heal: found applied for %s", app.Company)
		} else {
			log.Printf("self-heal: no applied confirmation email found for %s — creating inferred placeholder", app.Company)
			if _, err := s.store.CreateStage(ctx, app.ID, domain.StatusApplied, "", true, earliest.Add(-1*time.Hour)); err != nil {
				log.Printf("self-heal: failed to create placeholder for %s: %v", app.Company, err)
			}
		}
	}

	if err := s.selfHealSchedulingEmails(ctx, apps); err != nil {
		log.Printf("self-heal: scheduling emails error: %v", err)
	}
}

// selfHealSchedulingEmails finds Cal.com/Calendly invites that were missed because
// they don't mention the company name. For each app with an interview stage but no
// scheduling service email yet, it searches Gmail by the known contact names.
func (s *Service) selfHealSchedulingEmails(ctx context.Context, apps []domain.Application) error {
	for _, app := range apps {
		hasInterview := false
		earliest := time.Now()
		for _, stage := range app.Stages {
			if stage.Status == domain.StatusInterview || stage.Status == domain.StatusAIInterview {
				hasInterview = true
			}
			if stage.AppliedAt.Before(earliest) {
				earliest = stage.AppliedAt
			}
		}
		if !hasInterview {
			continue
		}
		fromAddrs, err := s.store.GetContactFromAddrsForApp(ctx, app.ID, s.getUserEmail())
		if err != nil || len(fromAddrs) == 0 {
			continue
		}

		searchFrom := earliest.Add(-30 * 24 * time.Hour)
		for _, fromAddr := range fromAddrs {
			// skip the user's own sent addresses — searching by "Verena" returns hundreds of unrelated emails
			if s.isSentByUser(fromAddr) {
				continue
			}
			name := extractDisplayName(fromAddr)
			if name == "" {
				continue
			}
			log.Printf("self-heal scheduling: searching for %q emails for %s/%s", name, app.Company, app.Role)
			emails, err := s.gmail.FetchJobEmailsForCompany(ctx, name, searchFrom)
			if err != nil {
				log.Printf("self-heal scheduling fetch error for %q: %v", name, err)
				continue
			}
			for _, email := range emails {
				if !isSchedulingService(email.From) {
					continue
				}
				// if the email was previously dropped as noise (in processed_emails but not
				// in thread_emails), clear it so it can be matched and linked now
				if processed, _ := s.store.IsEmailProcessed(ctx, email.ID); processed {
					if linked, _ := s.store.IsEmailInThreadEmails(ctx, email.ID); !linked {
						log.Printf("self-heal scheduling: un-processing previously-dropped email %s", email.ID)
						_ = s.store.UnmarkEmailProcessed(ctx, email.ID)
					} else {
						continue
					}
				}
				if err := s.processEmail(ctx, email); err != nil {
					log.Printf("self-heal scheduling process error %s: %v", email.ID, err)
				}
				time.Sleep(300 * time.Millisecond)
			}
		}
	}
	return nil
}

func (s *Service) processEmail(ctx context.Context, email gmail.Email) error {
	log.Printf("processing email %s: subject=%q from=%q", email.ID, email.Subject, email.From)
	if processed, err := s.store.IsEmailProcessed(ctx, email.ID); err != nil || processed {
		if processed {
			log.Printf("skipping already-processed email %s", email.ID)
		}
		return err
	}

	// check if this thread is already linked to an application — if so we can store immediately
	var threadAppID int64
	if email.ThreadID != "" {
		threadAppID = s.store.GetThreadApplicationID(ctx, email.ThreadID)
		if threadAppID != 0 {
			_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, stripHTML(email.Body), email.Date, threadAppID)
		}
	}

	// skip emails sent by the user
	if s.isSentByUser(email.From) {
		log.Printf("skipping sent email from self: %s thread=%s", email.ID, email.ThreadID)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	// always skip scheduling service noise (cal.com, calendly, etc.)
	if isNoise(email.From) {
		log.Printf("skipping noise email: %s from=%s", email.Subject, email.From)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}

	parsed, err := s.llm.ParseJobEmail(ctx, email.Subject, stripHTML(email.Body), email.From, nil)
	if err != nil {
		if strings.Contains(err.Error(), "usage limits") {
			return err // propagate so the outer loop can stop and leave this email unprocessed
		}
		log.Printf("skipping email %s: %v", email.ID, err)
		return s.store.MarkEmailProcessed(ctx, email.ID)
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

	// scheduling language in body signals an interview invite even if the URL was stripped
	if hasSchedulingLanguage(email.Body) && (parsed.Confidence == "low" || parsed.Status == domain.StatusApplied) {
		log.Printf("overriding to interview — scheduling language detected in %s", email.ID)
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
					parsed.Company = extractCompanyFromBody(email.Body)
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
			return s.store.MarkEmailProcessed(ctx, email.ID)
		}
	}

	// Hallucination guard: small models (Ollama) sometimes confabulate a company
	// name from few-shot corrections in the prompt. Two passes:
	// 1. If the FIRST significant word of the company is not in subject+sender,
	//    the whole name is wrong (e.g. "Emma…" for an Acto email) — replace with
	//    the sender display name.
	// 2. If the first word is valid but a suffix after " – "/" - " has no words in
	//    context, strip it (e.g. "Acto - The Sleep Company" → "Acto").
	if parsed.Company != "" {
		ctxLower := strings.ToLower(email.Subject + " " + email.From)
		firstWord := ""
		for _, word := range strings.Fields(strings.ToLower(parsed.Company)) {
			if len(word) > 3 {
				firstWord = word
				break
			}
		}
		if firstWord != "" && !strings.Contains(ctxLower, firstWord) {
			override := senderDisplayName(email.From)
			parsed.Company = override
		} else {
			for _, sep := range []string{" – ", " - "} {
				idx := strings.Index(parsed.Company, sep)
				if idx == -1 {
					continue
				}
				suffix := strings.ToLower(parsed.Company[idx+len(sep):])
				suffixInCtx := false
				for _, word := range strings.Fields(suffix) {
					if len(word) > 3 && strings.Contains(ctxLower, word) {
						suffixInCtx = true
						break
					}
				}
				if !suffixInCtx {
					parsed.Company = parsed.Company[:idx]
				}
				break
			}
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
			return s.store.MarkEmailProcessed(ctx, email.ID)
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
		_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, stripHTML(email.Body), email.Date, existingApp.ID)
		threadAppID = existingApp.ID
	}

	// calendar notifications and reminders: email is now stored, but no stage should be created.
	if isCalendarNotification(email.Body) {
		log.Printf("skipping calendar notification (stored): %s thread=%s", email.Subject, email.ThreadID)
		return s.store.MarkEmailProcessed(ctx, email.ID)
	}
	if isReminder(email.Body) && !hasInterviewLink(email.Body) &&
		parsed.Status != domain.StatusRejected && parsed.Status != domain.StatusOffer {
		log.Printf("skipping reminder (stored): %s", email.Subject)
		return s.store.MarkEmailProcessed(ctx, email.ID)
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
					reparsed, err := s.llm.ParseJobEmail(ctx, email.Subject, stripHTML(email.Body), email.From, existingStages)
					if err != nil {
						if strings.Contains(err.Error(), "usage limits") {
							return err
						}
						log.Printf("warning: duplicate-check parse failed for %s: %v", email.ID, err)
					} else if reparsed.IsDuplicate {
						log.Printf("skipping duplicate %s email for %s/%s: %s", parsed.Status, company, role, email.ID)
						return s.store.MarkEmailProcessed(ctx, email.ID)
					}
				}
			}
		}
	}

	// idempotency — email already created a stage; link the thread email to it
	exists, err := s.store.ApplicationExistsByEmailID(ctx, email.ID)
	if err != nil {
		return err
	}
	if exists {
		if email.ThreadID != "" {
			if stage, err := s.store.GetStageByLastEmailID(ctx, email.ID); err == nil && stage != nil {
				_ = s.store.LinkThreadEmailToStage(ctx, email.ID, stage.ID)
				// ensure stored with correct appID if not already
				if threadAppID == 0 {
					_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, stripHTML(email.Body), email.Date, stage.ApplicationID)
				}
			}
		}
		return s.store.MarkEmailProcessed(ctx, email.ID)
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
			return err
		}
		if hasApplied {
			log.Printf("skipping duplicate applied email for %s/%s: %s", company, role, email.ID)
			return s.store.MarkEmailProcessed(ctx, email.ID)
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
			return err
		}
	}

	// store thread email now if we haven't yet (first email for a brand-new application)
	if email.ThreadID != "" && threadAppID == 0 {
		_ = s.store.StoreThreadEmail(ctx, email.ID, email.ThreadID, email.From, email.Subject, stripHTML(email.Body), email.Date, appID)
	}

	stageID, err := s.store.CreateStage(ctx, appID, parsed.Status, email.ID, false, appliedAt)
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateStage) {
			log.Printf("skipping duplicate %s stage for %s/%s: %s", parsed.Status, company, role, email.ID)
			return s.store.MarkEmailProcessed(ctx, email.ID)
		}
		return err
	}
	if parsed.Status == domain.StatusApplied {
		if err := s.store.FixAppliedStageDate(ctx, appID, stageID); err != nil {
			log.Printf("warning: could not fix applied stage date for %s: %v", company, err)
		}
	}

	if email.ThreadID != "" {
		_ = s.store.LinkThreadEmailToStage(ctx, email.ID, stageID)
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
		strings.Contains(lower, "i will have a new date") ||
		strings.Contains(lower, "i'll have a new date") ||
		strings.Contains(lower, "thanks for filling in this form") ||
		strings.Contains(lower, "you're receiving this email because you filled in") ||
		strings.Contains(lower, "upcoming appointment") ||
		strings.Contains(lower, "you're mentioned in the meeting summary") ||
		strings.Contains(lower, "meeting summary") ||
		strings.Contains(lower, "this event isn't in your calendar")
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

var invalidCompanyNames = map[string]bool{
	"gmail": true, "google": true, "outlook": true, "microsoft": true,
	"zoom": true, "calendly": true, "cal": true, "slack": true,
	"linkedin": true, "indeed": true, "glassdoor": true,
	// job platforms — not companies you apply to
	"wellfound": true, "angellist": true, "computrabajo": true,
	"lever": true, "greenhouse": true, "workday": true,
	"smartrecruiters": true, "recruitee": true, "bamboohr": true,
}

func isInvalidCompany(company string) bool {
	return company == "" || invalidCompanyNames[strings.ToLower(company)]
}

func isNoise(from string) bool {
	lower := strings.ToLower(from)
	return strings.Contains(lower, "emailsys1a.net") ||
		strings.Contains(lower, "calendar-notification@google.com") ||
		strings.Contains(lower, "calendar-server.bounces.google.com")
}

func isSchedulingService(from string) bool {
	lower := strings.ToLower(from)
	return strings.Contains(lower, "cal.com") ||
		strings.Contains(lower, "calendly.com") ||
		strings.Contains(lower, "savvycal.com") ||
		strings.Contains(lower, "chilipiper.com")
}

// SyncCompany fetches and processes all job emails for a specific company going back 6 months.
func (s *Service) SyncCompany(ctx context.Context, company string) error {
	since := time.Now().Add(-180 * 24 * time.Hour)
	log.Printf("company sync: fetching emails for %q since %s", company, since.Format(time.DateOnly))
	emails, err := s.gmail.FetchJobEmailsForCompany(ctx, company, since)
	if err != nil {
		return fmt.Errorf("fetch emails for %s: %w", company, err)
	}
	log.Printf("company sync: fetched %d emails for %q", len(emails), company)
	sort.Slice(emails, func(i, j int) bool { return emails[i].Date.Before(emails[j].Date) })
	for _, email := range emails {
		if err := s.processEmail(ctx, email); err != nil {
			log.Printf("company sync %s: %v", email.ID, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("company sync complete for %q", company)

	// self-heal only for this company's apps, not all applications
	allApps, err := s.store.ListApplications(ctx)
	if err == nil {
		var companyApps []domain.Application
		for _, app := range allApps {
			if strings.EqualFold(domain.NormalizeCompany(app.Company), domain.NormalizeCompany(company)) {
				companyApps = append(companyApps, app)
			}
		}
		s.selfHealApps(ctx, companyApps)
	}
	return nil
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
		strings.Contains(lower, "cal.com/") ||
		strings.Contains(lower, "schedule.lever.co")
}

func isCalendarNotification(body string) bool {
	// truncate to avoid matching quoted text from a previous calendar notification
	// embedded in a reply — only the new content at the top matters
	if len(body) > 500 {
		body = body[:500]
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "this is a reminder about your upcoming event") ||
		strings.Contains(lower, "reminder about your upcoming") ||
		strings.Contains(lower, "is inviting you to a scheduled zoom meeting") ||
		strings.Contains(lower, "you're confirmed for your interview") ||
		strings.Contains(lower, "you are confirmed for your interview") ||
		strings.Contains(lower, "confirmed for the following interview") ||
		strings.Contains(lower, "your interview has been confirmed") ||
		strings.Contains(lower, "your interview is confirmed") ||
		strings.Contains(lower, "interview confirmation") && strings.Contains(lower, "date/time:") ||
		strings.Contains(lower, "appointment booked") ||
		strings.Contains(lower, "microsoft teams meeting") ||
		// Google Calendar booking confirmation boilerplate
		strings.Contains(lower, "test your setup at any time before your appointment") ||
		strings.Contains(lower, "new to google meet? learn more about getting started")
}

func hasSchedulingLanguage(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "choose the most convenient slot") ||
		strings.Contains(lower, "choose a convenient slot") ||
		strings.Contains(lower, "choose the most convenient time") ||
		strings.Contains(lower, "book your slot") ||
		strings.Contains(lower, "book a slot") ||
		strings.Contains(lower, "select a time slot") ||
		strings.Contains(lower, "pick a time slot") ||
		strings.Contains(lower, "schedule your interview") ||
		strings.Contains(lower, "book your interview") ||
		strings.Contains(lower, "book an interview appointment") ||
		strings.Contains(lower, "schedule a call") ||
		strings.Contains(lower, "wählen sie einen termin") ||
		strings.Contains(lower, "termin wählen") ||
		strings.Contains(lower, "greenhouse.io/schedule") ||
		strings.Contains(lower, "upcoming interview")
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

// senderDisplayName extracts the display name from a From header (e.g.
// "Acto <acto-jobs@m.personio.com>" → "Acto"), returning "" for individual
// person names and known non-company senders.
// extractDisplayName pulls the display name from a From header including person
// names — unlike senderDisplayName, it does not filter out two-word person names.
func extractDisplayName(from string) string {
	re := regexp.MustCompile(`^"?([^"<]+?)"?\s*<`)
	m := re.FindStringSubmatch(strings.TrimSpace(from))
	if len(m) < 2 {
		return ""
	}
	name := strings.TrimSpace(m[1])
	lower := strings.ToLower(name)
	for _, skip := range []string{"noreply", "no-reply", "google", "calendly", "zoom", "microsoft", "linkedin", "upwork"} {
		if strings.Contains(lower, skip) {
			return ""
		}
	}
	return name
}

func senderDisplayName(from string) string {
	re := regexp.MustCompile(`^"?([^"<]+?)"?\s*<`)
	m := re.FindStringSubmatch(strings.TrimSpace(from))
	if len(m) < 2 {
		return ""
	}
	name := strings.TrimSpace(m[1])
	lower := strings.ToLower(name)
	for _, skip := range []string{"noreply", "no-reply", "google", "tl;dv", "calendly", "zoom", "microsoft", "linkedin", "upwork"} {
		if strings.Contains(lower, skip) {
			return ""
		}
	}
	// two-word name where both words start with a capital → likely a person
	parts := strings.Fields(name)
	if len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0 &&
		parts[0][0] >= 'A' && parts[0][0] <= 'Z' &&
		parts[1][0] >= 'A' && parts[1][0] <= 'Z' {
		return ""
	}
	return name
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

// extractNamesFromSchedulingEmail pulls proper names from scheduling email
// subjects and bodies, filtering out the user's own name. Handles patterns like:
func extractNamesFromSchedulingEmail(subject, body, myName string) []string {
	myLower := strings.ToLower(myName)
	seen := map[string]bool{}
	var names []string

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if myLower != "" && strings.Contains(strings.ToLower(name), myLower) {
			return
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	// subject: "between X and Y" or "meeting/interview/call with X"
	betweenRe := regexp.MustCompile(`(?i)between ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+) and ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)`)
	withRe := regexp.MustCompile(`(?i)(?:meeting|interview|call) with ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)`)
	if m := betweenRe.FindStringSubmatch(subject); m != nil {
		add(m[1])
		add(m[2])
	} else if m := withRe.FindStringSubmatch(subject); m != nil {
		add(m[1])
	}

	// body: "Name - Organizer" / "Name - Host" (booking confirmation format)
	organizerRe := regexp.MustCompile(`([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)\s*[-–]\s*(?:Organizer|Host)`)
	for _, m := range organizerRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}

	// body: "You & Name" (reminder format)
	youAndRe := regexp.MustCompile(`You & ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)`)
	for _, m := range youAndRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}

	emailRe := regexp.MustCompile(`([\w]+(?:\.[\w]+)+)@([\w.-]+\.[a-z]{2,})`)
	skipEmailDomains := map[string]bool{
		"cal.com": true, "calendly.com": true, "gmail.com": true,
		"google.com": true, "zoom.us": true, "microsoft.com": true,
	}
	myEmailLower := ""
	if myName != "" {
		myEmailLower = strings.ToLower(strings.ReplaceAll(myName, " ", "."))
	}
	for _, m := range emailRe.FindAllStringSubmatch(strings.ToLower(body), -1) {
		if len(m) < 3 || skipEmailDomains[m[2]] {
			continue
		}
		localPart := m[1]
		if myEmailLower != "" && strings.Contains(localPart, myEmailLower) {
			continue
		}
		parts := strings.Split(localPart, ".")
		if len(parts) < 2 {
			continue
		}
		var titleParts []string
		for _, p := range parts {
			if len(p) > 1 {
				titleParts = append(titleParts, strings.ToUpper(p[:1])+p[1:])
			}
		}
		if len(titleParts) >= 2 {
			add(strings.Join(titleParts, " "))
		}
	}

	return names
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

func extractCompanyFromSubject(subject string) string {
	lower := strings.ToLower(subject)
	for _, prefix := range []string{" at ", " bei ", " @ "} {
		idx := strings.LastIndex(lower, prefix)
		if idx == -1 {
			continue
		}
		candidate := strings.TrimSpace(subject[idx+len(prefix):])
		candidate = strings.TrimRight(candidate, ".,!?;:")
		if candidate != "" && len(candidate) <= 60 {
			return candidate
		}
	}
	return ""
}

func hasRejectionKeywords(body string) bool {
	lower := strings.ToLower(body)
	keywords := []string{
		"leider", "nicht berücksichtigen", "haben uns für andere kandidaten",
		"unfortunately", "decided to move forward with other candidates",
		"decided to move forward with candidates", "we won't be moving forward",
		"we will not be moving forward", "not be progressing",
		"not progressing your application", "we are unable to move forward",
		"does not meet our current requirements", "more closely align with",
		"more closely matches", "we've filled the position",
		"decided not to move forward", "we have decided not",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
