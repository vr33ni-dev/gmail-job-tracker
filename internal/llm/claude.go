package llm

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

// ── Claude ───────────────────────────────────────────────────────────────────
func (c *Client) suggestRuleWithClaude(ctx context.Context, userMsg string) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 60,
		"system":     suggestRuleSystemPrompt,
		"messages":   []msg{{Role: "user", Content: userMsg}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Content) == 0 {
		return "", fmt.Errorf("claude empty response")
	}
	return strings.TrimSpace(out.Content[0].Text), nil
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

func (c *Client) parseWithClaude(ctx context.Context, staticPrompt, corrections, subject, body, from, existingContext string) (*domain.ParsedEmail, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	systemBlocks := []systemBlock{
		{
			Type:         "text",
			Text:         staticPrompt,
			CacheControl: &cacheControl{Type: "ephemeral"},
		},
	}
	if corrections != "" {
		systemBlocks = append(systemBlocks, systemBlock{
			Type: "text",
			Text: corrections,
		})
	}

	payload, _ := json.Marshal(map[string]any{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 300,
		"system":     systemBlocks,
		"messages":   []msg{{Role: "user", Content: fmt.Sprintf("From: %s\nSubject: %s\n\n%s%s", from, subject, truncate(body, 2000), existingContext)}},
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
		req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")

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
