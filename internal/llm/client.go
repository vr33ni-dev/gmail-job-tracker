package llm

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	ollamaAPIURL = "http://localhost:11434/v1/chat/completions"
)

type Client struct {
	httpClient *http.Client
	provider   string
	store      correctionStore
}

func NewClient(store correctionStore) *Client {
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "ollama"
	}
	return &Client{
		httpClient: &http.Client{},
		provider:   provider,
		store:      store,
	}
}

var systemPrompt = `You analyze job application emails and return JSON only — no markdown, no explanation.

Return this exact structure:
{"company":"string","role":"string","status":"applied|interview|ai_interview|offer|rejected|withdrawn","confidence":"high|medium|low","summary":"one sentence","platform":"linkedin|upwork|greenhouse|lever|softgarden|direct|other","language":"en|de"}

The email may be in English or German. Apply the same status rules regardless of language.

Status rules:
- applied: any confirmation of receipt OR generic acknowledgment — "Thank you for applying", "We'll review your application", "Bewerbung erhalten", "Eingang Ihrer Bewerbung", "Your profile is under review", waitlisted, platform nudges, no specific next step
  ✗ NOT APPLIED: Any email asking the candidate to provide a missing document ("please share your resume", "I forgot to ask for your CV", "send us your portfolio", "could you send over your resume") — these are mid-process follow-ups from a real person; set confidence: "low" and is_duplicate: true
- ai_interview: automated/AI-conducted interview request. Examples: "AI interview", "automated interview", "HireVue", "Spark Hire", "micro1", "one-way video interview", "complete your AI interview"
- interview: ANY personalized outreach where a human has reviewed your profile and wants to move forward — scheduling language, video conference links, take-home assignments, written interview requests, calendar invites
  ✓ INTERVIEW: "Are you available on 19.03.2026 14:00? Join: https://meet.google.com/xxx"
  ✓ INTERVIEW: "I'd like to invite you to a first interview. Book your slot: [calendar link]"
  ✓ INTERVIEW: "The next step is a take-home assignment" (human moving you forward)
  ✓ INTERVIEW: "The next step is to answer a few questions" or "we'd like you to complete a short task" (written assessment — counts as interview even without a meeting link)
  ✓ INTERVIEW: "I enjoyed our conversation, we'd like to move forward" (human follow-up)
  ✓ INTERVIEW: "Thank you for sharing your Resume. we're moving forward to the next step. You can schedule a time that works best for you using this link" — opening thanks does NOT make it applied; the scheduling invite is the dominant signal
  ✗ NOT INTERVIEW: "I will have a new date for you by EOD tomorrow" (no commitment yet)
  ✗ NOT INTERVIEW: "AI notetaker tool will be used" — still a regular interview, NOT ai_interview
  ✗ NOT INTERVIEW: Zoom/calendar meeting notifications ("is inviting you to a scheduled Zoom meeting", "Join Zoom Meeting", meeting ID and passcode only) — these are calendar confirmations, set is_duplicate true
  ✗ NOT INTERVIEW: Gmail reaction notifications ("reacted via Gmail", "reacted to your message") — these are emoji reactions to emails, set is_duplicate true
  ✗ NOT INTERVIEW: Short conversational replies mid-process ("Yes, depends how many months", "Sure, let me check", "Thanks for the update") — set confidence "low" instead
  ✗ NOT INTERVIEW: Upwork job invitations ("invited to submit a proposal", "submit a proposal to work with") — classify as applied
- offer: job offer received
- rejected: not moving forward. Examples: "leider", "nicht berücksichtigen", "haben uns für andere Kandidaten entschieden", "unfortunately", "decided to move forward with other candidates", "decided to move forward with candidates", "we won't be moving forward", "we've filled the position", "we will not be moving forward", "not be progressing", "not progressing your application", "we are unable to move forward", "does not meet our current requirements", "more closely align with", "more closely matches"
- withdrawn: candidate withdrew their application

Platform detection — use sender domain:
- "linkedin" → linkedin.com
- "upwork" → upwork.com
- "greenhouse" → greenhouse.io
- "lever" → lever.co
- "softgarden" → softgarden.io
- "direct" → company domain
- "other" → unclear

IMPORTANT RULES:
- Always extract company name from email body/signature, NEVER from sender domain (lever.co, greenhouse.io are ATS platforms not companies)
- Do not invent, alter, or hallucinate company names. If the company is not clearly stated in the email text or signature, return company:"".
- If the company is clearly ambiguous or only appears as a platform/ATS domain, return company:"" rather than guessing.
- "role" must be the job title the candidate applied for — never interviewer names, team lead titles, meeting names, or email subjects
  ✓ ROLE: "Senior Software Engineer", "Full Stack Developer", "Product Manager" (what the candidate applied for)
  ✗ NOT ROLE: "Head of AI and Engineering", "VP of Product", "Engineering Manager" (these are the interviewer's title — extract from the body what the candidate applied for instead, or use "")
  ✗ NOT ROLE: email subject lines, meeting titles, or department names
  ✗ NOT ROLE: combined titles like "Junior Full Stack Engineer/Senior Backend Engineer" — role must be ONE job title, never combine two with "/" or "or"
- If role cannot be determined, use "" (empty string) — do not guess
- Set confidence "low" if: newsletter, marketing/promotional, product announcement, or unrelated to a job application (contains "Newsletter", "nur solange der Vorrat reicht", "Abmelden", or is about products/sales)
- Set confidence "high" if: email clearly relates to a specific job application with identifiable company and status
- Set confidence "medium" if: email relates to a job application but company or status is ambiguous
- applied, offer, rejected, and withdrawn can each occur ONLY ONCE per application. If you receive a second email of any of these types (e.g. a reminder about a rejection, a follow-up on an offer, a second confirmation of withdrawal), set is_duplicate: true`

func (c *Client) ParseJobEmail(ctx context.Context, subject, body, from string, existingStages []domain.ApplicationStage) (*domain.ParsedEmail, error) {
	var corrections string
	if c.store != nil {
		if corrs, err := c.store.GetRecentCorrections(ctx, 20); err == nil && len(corrs) > 0 {
			var rules, examples []domain.Correction
			for _, cor := range corrs {
				if cor.Command != "" {
					rules = append(rules, cor)
				} else {
					examples = append(examples, cor)
				}
			}
			if len(rules) > 0 || len(examples) > 0 {
				corrections += "\n\nPrevious corrections and rules (apply these):\n"
			}
			for _, cor := range rules {
				corrections += fmt.Sprintf("- RULE: %s\n", cor.Command)
			}
			for _, cor := range examples {
				excerpt := truncate(cor.EmailSubject+": "+cor.EmailBody, 200)
				switch {
				case cor.CorrectStatus == "skip":
					corrections += fmt.Sprintf("- SKIP (set confidence low, is_duplicate true) emails like this — they are noise and should not create an application: %q\n", excerpt)
				case cor.CorrectStatus == "conversation":
					corrections += fmt.Sprintf("- CONVERSATION (set is_duplicate true) emails like this — they are part of an ongoing thread but not a new stage milestone: %q\n", excerpt)
				case cor.WrongStatus != "" && cor.CorrectStatus != "":
					corrections += fmt.Sprintf("- Email %q was classified as %s but correct classification is %s\n",
						excerpt, cor.WrongStatus, cor.CorrectStatus)
				}
			}
		}
	}

	// build existing-stages context
	var existingContext string
	if len(existingStages) > 0 {
		existingContext = "\n\nEXISTING_STAGES_SAME_STATUS (already recorded for this company+role):\n"
		for i, s := range existingStages {
			existingContext += fmt.Sprintf("%d. Date: %s | Email ID: %s\n",
				i+1, s.AppliedAt.Format("2006-01-02 15:04"), s.LastEmailID)
		}
		existingContext += "\nAn interview stage is already recorded for this company. Default to is_duplicate: true (same interview conversation) UNLESS this email clearly announces a brand-new distinct event: explicit 'second interview', 'next round', 'technical interview', 'final round', 'coding challenge', new take-home assignment, written assessment or questionnaire ('answer a few questions', 'we'd like you to answer', 'complete the following questions', 'share your thoughts on'), a new specific future date/time being proposed in this email that differs from the stage dates listed above, or explicit progression language ('you passed', 'next step', 'I'd like to move you forward'). Example: 'the next step in our process is for you to answer a few questions' is is_duplicate: false — it is a new written assessment stage, not a repeat of the previous interview. The interviewer being the same person does NOT make it a duplicate — a new date alone is enough. The following are always is_duplicate: true: calendar notifications ('your event is scheduled', 'you have an appointment'), scheduling replies, reminders, 'thank you for the interview' follow-ups, short conversational replies, and any email that does not itself invite you to something new."
	}

	switch c.provider {
	case "claude":
		return c.parseWithClaude(ctx, systemPrompt, corrections, subject, body, from, existingContext)
	default:
		return c.parseWithOllama(ctx, systemPrompt, corrections, subject, body, from, existingContext)
	}
}

const suggestRuleSystemPrompt = `You write correction rules for an email classifier. Given an email that was incorrectly classified, write ONE short rule (max 15 words, imperative, no quotes) that generalises this correction to prevent similar mistakes. Output only the rule text, nothing else.
Examples:
- Gmail reaction notifications ('reacted via Gmail') are conversations, not interview stages
- Emails from noreply@lever.co with subject 'application received' are applied, not interview`

func (c *Client) SuggestRule(ctx context.Context, subject, body, wrongStatus, correctStatus string) (string, error) {
	userMsg := fmt.Sprintf(
		"Email subject: %s\nEmail body (first 400 chars): %s\nWrong classification: %s\nCorrect classification: %s\nWrite the rule:",
		subject, truncate(body, 400), wrongStatus, correctStatus,
	)
	switch c.provider {
	case "claude":
		return c.suggestRuleWithClaude(ctx, userMsg)
	default:
		return c.suggestRuleWithOllama(ctx, userMsg)
	}
}
