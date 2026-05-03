package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	ollamaAPIURL = "http://localhost:11434/v1/chat/completions"
)

type Client struct {
	httpClient *http.Client
	provider   string
	store      *db.Store
}

func NewClient(store *db.Store) *Client {
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
- ai_interview: automated/AI-conducted interview request. Examples: "AI interview", "automated interview", "HireVue", "Spark Hire", "micro1", "one-way video interview", "complete your AI interview"
- interview: ANY personalized outreach where a human has reviewed your profile and wants to move forward — scheduling language, video conference links, take-home assignments, written interview requests, calendar invites
  ✓ INTERVIEW: "Are you available on 19.03.2026 14:00? Join: https://meet.google.com/xxx"
  ✓ INTERVIEW: "I'd like to invite you to a first interview. Book your slot: [calendar link]"
  ✓ INTERVIEW: "The next step is a take-home assignment" (human moving you forward)
  ✓ INTERVIEW: "I enjoyed our conversation, we'd like to move forward" (human follow-up)
  ✗ NOT INTERVIEW: "I will have a new date for you by EOD tomorrow" (no commitment yet)
  ✗ NOT INTERVIEW: "Baran is on vacation, we are finding another team member" (no commitment yet)
  ✗ NOT INTERVIEW: "AI notetaker tool will be used" — still a regular interview, NOT ai_interview
  ✗ NOT INTERVIEW: Upwork job invitations ("invited to submit a proposal", "submit a proposal to work with") — classify as applied
- offer: job offer received
- rejected: not moving forward. Examples: "leider", "nicht berücksichtigen", "haben uns für andere Kandidaten entschieden", "unfortunately", "decided to move forward with other candidates", "we won't be moving forward", "we've filled the position"
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
- "role" must be the job title the candidate applied for — never interviewer names, team lead titles, meeting names, or email subjects
- If role cannot be determined, use "" (empty string) — do not guess
- Set confidence "low" if: newsletter, marketing/promotional, product announcement, or unrelated to a job application (contains "Newsletter", "nur solange der Vorrat reicht", "Abmelden", or is about products/sales)
- Set confidence "high" if: email clearly relates to a specific job application with identifiable company and status
- Set confidence "medium" if: email relates to a job application but company or status is ambiguous`

func (c *Client) ParseJobEmail(ctx context.Context, subject, body, from string) (*domain.ParsedEmail, error) {
	// build prompt with corrections injected
	prompt := systemPrompt
	if c.store != nil {

		if corrections, err := c.store.GetRecentCorrections(ctx, 10); err == nil && len(corrections) > 0 {

			prompt += "\n\nExamples from previous corrections (learn from these):\n"
			for _, cor := range corrections {
				prompt += fmt.Sprintf("- Email: %q was classified as %s but correct classification is %s\n",
					truncate(cor.EmailSubject+": "+cor.EmailBody, 100),
					cor.WrongStatus,
					cor.CorrectStatus,
				)
			}
		}
	}

	switch c.provider {
	case "claude":
		return c.parseWithClaude(ctx, prompt, subject, body, from)
	default:
		return c.parseWithOllama(ctx, prompt, subject, body, from)
	}
}

// ── Claude ───────────────────────────────────────────────────────────────────
func (c *Client) parseWithClaude(ctx context.Context, prompt, subject, body, from string) (*domain.ParsedEmail, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 300,
		"system":     prompt,
		"messages":   []msg{{Role: "user", Content: fmt.Sprintf("From: %s\nSubject: %s\n\n%s", from, subject, truncate(body, 2000))}},
	})

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == 529 || resp.StatusCode == 503 {
			lastErr = fmt.Errorf("claude %d: %s", resp.StatusCode, b)
			continue
		}
		if resp.StatusCode == 400 {
			if strings.Contains(string(b), "usage limits") {
				return nil, fmt.Errorf("claude %d: %s", resp.StatusCode, b)
			}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("claude %d: %s", resp.StatusCode, b)
		}

		var out struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		if len(out.Content) == 0 {
			return nil, fmt.Errorf("empty claude response")
		}

		text := cleanJSON(out.Content[0].Text)
		var parsed domain.ParsedEmail
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, fmt.Errorf("parse claude json: %w — raw: %s", err, text)
		}
		if parsed.Confidence == "" {
			parsed.Confidence = "medium"
		}
		return &parsed, nil
	}
	return nil, lastErr
}

// ── Ollama ───────────────────────────────────────────────────────────────────
func (c *Client) parseWithOllama(ctx context.Context, prompt, subject, body, from string) (*domain.ParsedEmail, error) {
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3.1:8b"
	}

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []msg{
			{Role: "system", Content: prompt},
			{Role: "user", Content: fmt.Sprintf("From: %s\nSubject: %s\n\n%s", from, subject, truncate(body, 2000))},
		},
		"max_tokens": 300,
		"stream":     false,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaAPIURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed — is ollama running? %w", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama %d: %s", resp.StatusCode, b)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("empty ollama response")
	}

	text := cleanJSON(out.Choices[0].Message.Content)
	var parsed domain.ParsedEmail
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("parse ollama json: %w — raw: %s", err, text)
	}
	if parsed.Confidence == "" {
		parsed.Confidence = "medium"
	}
	return &parsed, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// extract just the JSON object
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	}
	// fix common Llama JSON issues — missing quote before key after comma
	s = strings.ReplaceAll(s, ",.", ",\"")
	s = strings.ReplaceAll(s, ", .", ", \"")
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
