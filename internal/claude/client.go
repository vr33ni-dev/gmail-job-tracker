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

const apiURL = "https://api.anthropic.com/v1/messages"

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{apiKey: os.Getenv("ANTHROPIC_API_KEY"), httpClient: &http.Client{}}
}

var systemPrompt = `You analyze job application emails and return JSON only — no markdown, no explanation.

Return this exact structure:
{"company":"string","role":"string","status":"applied|reviewing|screening|interview|ai_interview|offer|rejected|withdrawn|no_response","confidence":"high|medium|low","summary":"one sentence","platform":"linkedin|upwork|greenhouse|lever|softgarden|direct|other","language":"en|de"}

Status rules:
- applied: confirmation of submission
- reviewing: automated message confirming application is under active review ("your profile is being reviewed", "we'll be in touch", "currently under review")
- screening: a human recruiter personally reaches out to schedule a call or ask questions
- ai_interview: automated/AI-conducted interview request (mentions "AI interview", "automated interview", "HireVue", "Spark Hire", "micro1", "one-way video interview")
- interview: human interview scheduled or completed
- offer: job offer received
- rejected: not moving forward ("leider", "nicht berücksichtigen", "unfortunately", "decided to move forward with other candidates")
- no_response: generic automated acknowledgment with no specific next step
- withdrawn: candidate withdrew application

The email may be in English or German. Apply the same status rules regardless of language.

Common German indicators:
- rejected: "leider", "nicht berücksichtigen", "haben uns für andere Kandidaten entschieden", "absagen"
- applied: "Bewerbung erhalten", "Eingang Ihrer Bewerbung"
- interview: "Vorstellungsgespräch", "Einladung"
- screening: "Erstkontakt", "kurzes Gespräch"


Platform detection rules:
- "linkedin" if sender contains linkedin.com
- "upwork" if sender contains upwork.com  
- "greenhouse" if sender contains greenhouse.io
- "lever" if sender contains lever.co
- "direct" for company domain emails (no-reply@companydomain.com)
- "other" if unclear
- "softgarden" if sender contains softgarden.io`

func (c *Client) ParseJobEmail(ctx context.Context, subject, body, from string) (*domain.ParsedEmail, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}

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

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 529 || resp.StatusCode == 503 {
			// retryable
			continue
		}
		if resp.StatusCode == 400 {
			b, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("claude %d: %s", resp.StatusCode, b)
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("claude %d: %s", resp.StatusCode, b)
		}

		var out struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		if len(out.Content) == 0 {
			return nil, fmt.Errorf("empty response")
		}

		text := strings.TrimSpace(out.Content[0].Text)
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)

		var parsed domain.ParsedEmail
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, err
		}
		return &parsed, nil
	}
	return nil, lastErr
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
