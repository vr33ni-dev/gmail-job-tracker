package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Email struct {
	ID      string
	Subject string
	Body    string
	From    string
	Date    time.Time
}

type Client struct{ svc *gmail.Service }

var htmlTagRegex = regexp.MustCompile(`<[^>]+>`)
var whitespaceRegex = regexp.MustCompile(`\s+`)

func stripHTML(s string) string {
	s = htmlTagRegex.ReplaceAllString(s, " ")
	s = whitespaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func NewClient(ctx context.Context, token *oauth2.Token, config *oauth2.Config) (*Client, error) {
	svc, err := gmail.NewService(ctx, option.WithTokenSource(config.TokenSource(ctx, token)))
	if err != nil {
		return nil, fmt.Errorf("gmail service: %w", err)
	}
	return &Client{svc: svc}, nil
}

func (c *Client) FetchJobEmails(ctx context.Context, since time.Time) ([]Email, error) {
	query := fmt.Sprintf(
		`after:%s (subject:"application" OR subject:"applied" OR subject:"interview" OR subject:"offer" OR subject:"unfortunately" OR subject:"regret" OR subject:"next steps" OR subject:"thank you for applying" OR subject:"Bewerbung" OR subject:"Absage" OR subject:"Einladung" OR subject:"leider" OR subject:"Vorstellungsgespräch" OR subject:"Deine Bewerbung" OR subject:"Ihre Bewerbung")`,
		since.Format("2006/01/02"),
	)
	var emails []Email
	pageToken := ""
	for {
		call := c.svc.Users.Messages.List("me").Q(query).MaxResults(50)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		res, err := call.Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			if email, err := c.fetchMessage(ctx, m.Id); err == nil {
				emails = append(emails, *email)
			}
		}
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}
	return emails, nil
}

func (c *Client) fetchMessage(ctx context.Context, id string) (*Email, error) {
	msg, err := c.svc.Users.Messages.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	email := &Email{ID: id}
	for _, h := range msg.Payload.Headers {
		switch h.Name {
		case "Subject":
			email.Subject = h.Value
		case "From":
			email.From = h.Value
		case "Date":
			email.Date = parseEmailDate(h.Value)
		}
	}
	email.Body = stripHTML(extractBody(msg.Payload))
	return email, nil
}

func extractBody(part *gmail.MessagePart) string {
	if part == nil {
		return ""
	}
	if part.MimeType == "text/plain" && part.Body != nil {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	for _, p := range part.Parts {
		if body := extractBody(p); body != "" {
			return body
		}
	}
	return ""
}

func parseEmailDate(dateStr string) time.Time {
	formats := []string{
		time.RFC1123Z, // Mon, 02 Jan 2006 15:04:05 -0700
		time.RFC1123,  // Mon, 02 Jan 2006 15:04:05 MST
		"Mon, 02 Jan 2006 15:04:05 -0700 (MST)",
		"Mon, 2 Jan 2006 15:04:05 -0700 (MST)",
		"Mon, 2 Jan 2006 15:04:05 -0700", // single digit day, no parens
		"Mon, 2 Jan 2006 15:04:05 MST",
		"02 Jan 2006 15:04:05 -0700",
		"2 Jan 2006 15:04:05 -0700",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, dateStr); err == nil {
			return t
		}
	}
	log.Printf("unparseable date: %q", dateStr)
	return time.Time{}
}
