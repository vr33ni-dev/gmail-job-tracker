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

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

// ── Ollama ───────────────────────────────────────────────────────────────────
func (c *Client) suggestRuleWithOllama(ctx context.Context, userMsg string) (string, error) {
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
			{Role: "system", Content: suggestRuleSystemPrompt},
			{Role: "user", Content: userMsg},
		},
		"max_tokens": 60,
		"stream":     false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaAPIURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("ollama empty response")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func (c *Client) parseWithOllama(ctx context.Context, prompt, subject, body, from, existingContext string) (*domain.ParsedEmail, error) {
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
			{Role: "user", Content: fmt.Sprintf("From: %s\nSubject: %s\n\n%s%s", from, subject, truncate(body, 2000), existingContext)},
		},
		"max_tokens": 500,
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
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("empty ollama response")
	}
	if out.Choices[0].FinishReason == "length" {
		return nil, fmt.Errorf("ollama output truncated — increase max_tokens or use a larger model")
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
