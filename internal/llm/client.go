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

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	ollamaAPIURL = "http://localhost:11434/v1/chat/completions"
)

type Client struct {
	httpClient *http.Client
	provider   string // "claude" or "gemini"
}

func NewClient() *Client {
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "gemini" // default to gemini
	}
	return &Client{
		httpClient: &http.Client{},
		provider:   provider,
	}
}

var systemPrompt = `You analyze job application emails and return JSON only — no markdown, no explanation.

Return this exact structure:
{"company":"string","role":"string","status":"applied|reviewing|screening|interview|ai_interview|offer|rejected|withdrawn|no_response","confidence":"high|medium|low","summary":"one sentence","platform":"linkedin|upwork|greenhouse|lever|softgarden|direct|other","language":"en|de"}

The email may be in English or German. Apply the same status rules regardless of language.

Status rules:
- applied: confirmation of submission
- reviewing: automated message confirming application is under active review ("your profile is being reviewed", "we'll be in touch", "currently under review")
- screening: a human recruiter personally reaches out to schedule a call or ask questions
- ai_interview: automated/AI-conducted interview request (mentions "AI interview", "automated interview", "HireVue", "Spark Hire", "micro1", "one-way video interview")
- interview: human interview scheduled or completed
- offer: job offer received
- rejected: not moving forward ("leider", "nicht berücksichtigen", "unfortunately", "decided to move forward with other candidates")
- no_response: generic automated acknowledgment, platform nudges (e.g. "complete your profile", "boost your profile", reminder emails not tied to a specific application)
- withdrawn: candidate withdrew application

Common German indicators:
- rejected: "leider", "nicht berücksichtigen", "haben uns für andere Kandidaten entschieden", "absagen"
- applied: "Bewerbung erhalten", "Eingang Ihrer Bewerbung"
- reviewing: "wird geprüft", "melden uns"
- interview: "Vorstellungsgespräch", "Einladung"
- screening: "Erstkontakt", "kurzes Gespräch"

Platform detection rules:
- "linkedin" if sender contains linkedin.com
- "upwork" if sender contains upwork.com
- "greenhouse" if sender contains greenhouse.io
- "lever" if sender contains lever.co
- "softgarden" if sender contains softgarden.io
- "direct" for company domain emails
- "other" if unclear`

func (c *Client) ParseJobEmail(ctx context.Context, subject, body, from string) (*domain.ParsedEmail, error) {
	switch c.provider {
	case "claude":
		return c.parseWithClaude(ctx, subject, body, from)
	default:
		return c.parseWithOllama(ctx, subject, body, from)
	}
}

// ── Claude ───────────────────────────────────────────────────────────────────
func (c *Client) parseWithClaude(ctx context.Context, subject, body, from string) (*domain.ParsedEmail, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 300,
		"system":     systemPrompt,
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
		return &parsed, nil
	}
	return nil, lastErr
}

// -- ParseWithOllama
func (c *Client) parseWithOllama(ctx context.Context, subject, body, from string) (*domain.ParsedEmail, error) {
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3.2"
	}

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []msg{
			{Role: "system", Content: systemPrompt},
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
	return &parsed, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
