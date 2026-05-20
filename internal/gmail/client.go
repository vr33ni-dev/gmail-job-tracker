package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/utils"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Email struct {
	ID       string
	ThreadID string
	Subject  string
	Body     string
	From     string
	Date     time.Time
}

type Client struct{ svc *gmail.Service }

var htmlTagRegex = regexp.MustCompile(`<[^>]+>`)
var whitespaceRegex = regexp.MustCompile(`\s+`)
var hrefRegex = regexp.MustCompile(`(?i)href="(https?://[^"]+)"`)

func stripHTML(s string) string {
	s = htmlTagRegex.ReplaceAllString(s, " ")
	s = whitespaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// extractHTMLLinks pulls href URLs out of HTML regardless of which MIME part
// is used as the body — so scheduling links in anchor tags are never lost.
func extractHTMLLinks(part *gmail.MessagePart) []string {
	if part == nil {
		return nil
	}
	var links []string
	if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			for _, m := range hrefRegex.FindAllStringSubmatch(string(data), -1) {
				links = append(links, m[1])
			}
		}
	}
	for _, p := range part.Parts {
		links = append(links, extractHTMLLinks(p)...)
	}
	return links
}

func NewClient(ctx context.Context, token *oauth2.Token, config *oauth2.Config) (*Client, error) {
	svc, err := gmail.NewService(ctx, option.WithTokenSource(config.TokenSource(ctx, token)))
	if err != nil {
		return nil, fmt.Errorf("gmail service: %w", err)
	}
	return &Client{svc: svc}, nil
}

func (c *Client) FetchJobEmails(ctx context.Context, since time.Time) ([]Email, error) {
	inClause := utils.InClause()

	query := fmt.Sprintf(
		`in%s after:%s (subject:"application" OR subject:"applied" OR subject:"process update" OR subject:"applying" OR subject:"interview" OR subject:"offer" OR subject:"unfortunately" OR subject:"regret" OR subject:"meeting" OR subject:"next step" OR subject:"next steps" OR subject:"thank you for applying" OR subject:"thanks for applying" OR subject:"your application" OR subject:"assignment" OR subject:"task" OR subject:"challenge" OR subject:"assessment" OR subject:"case study" OR subject:"test" OR subject:"Aufgabe" OR subject:"Hausaufgabe" OR subject:"Bewerbung" OR subject:"Absage" OR subject:"Einladung" OR subject:"leider" OR subject:"Vorstellungsgespräch" OR subject:"Deine Bewerbung" OR subject:"Ihre Bewerbung" OR filename:invite.ics OR filename:invitation.ics OR "meet.google.com" OR "zoom.us" OR "calendly.com" OR "cal.com" OR "greenhouse.io/schedule")`,
		inClause,
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

func (c *Client) FetchJobEmailsForCompany(ctx context.Context, company string, since time.Time) ([]Email, error) {
	keywords := `(subject:"application" OR subject:"applied" OR subject:"process update" OR subject:"applying" OR subject:"interview" OR subject:"offer" OR subject:"unfortunately" OR subject:"regret" OR subject:"meeting" OR subject:"next step" OR subject:"next steps" OR subject:"thank you for applying" OR subject:"thanks for applying" OR subject:"your application" OR subject:"assignment" OR subject:"task" OR subject:"challenge" OR subject:"assessment" OR subject:"case study" OR subject:"test" OR subject:"Aufgabe" OR subject:"Hausaufgabe" OR subject:"Bewerbung" OR subject:"Absage" OR subject:"Einladung" OR filename:invite.ics OR filename:invitation.ics OR "meet.google.com" OR "zoom.us" OR "calendly.com" OR "cal.com" OR "greenhouse.io/schedule" OR "schedule.lever.co" OR "lever.co/schedule")`
	// in:anywhere ensures archived/labeled emails (e.g. moved to a Jobs label by a Gmail filter)
	// are included — without it, Gmail may exclude emails that have been removed from All Mail view
	query := fmt.Sprintf(
		`in:anywhere after:%s "%s" %s`,
		since.Format("2006/01/02"),
		company,
		keywords,
	)
	log.Printf("company sync query: %s", query)

	seen := map[string]bool{}
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

		log.Printf("company sync: gmail returned %d message ids (page)", len(res.Messages))
		for _, m := range res.Messages {
			if seen[m.Id] {
				continue
			}
			seen[m.Id] = true
			email, err := c.fetchMessage(ctx, m.Id)
			if err != nil {
				log.Printf("company sync: failed to fetch message %s: %v", m.Id, err)
				continue
			}
			emails = append(emails, *email)
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
	email := &Email{ID: id, ThreadID: msg.ThreadId}
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
	for _, link := range extractHTMLLinks(msg.Payload) {
		if !strings.Contains(email.Body, link) {
			email.Body += " " + link
		}
	}
	return email, nil
}

func extractBody(part *gmail.MessagePart) string {
	if part == nil {
		return ""
	}

	// prefer plain text
	if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			return strings.TrimSpace(string(data))
		}
	}

	// recurse into multipart
	for _, p := range part.Parts {
		if body := extractBody(p); body != "" {
			return body
		}
	}

	// fall back to HTML and strip tags
	if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
		if data, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
			return stripHTML(strings.TrimSpace(string(data)))
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

func (c *Client) MoveToLabel(ctx context.Context, messageID, labelName string) error {
	// get or create label
	labelID, err := c.getOrCreateLabel(ctx, labelName)
	if err != nil {
		return err
	}

	_, err = c.svc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{
		AddLabelIds:    []string{labelID},
		RemoveLabelIds: []string{"INBOX"},
	}).Context(ctx).Do()
	return err
}

func (c *Client) getOrCreateLabel(ctx context.Context, name string) (string, error) {
	labels, err := c.svc.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	for _, l := range labels.Labels {
		// log.Printf("gmail label: %q id=%s", l.Name, l.Id)
		if strings.EqualFold(l.Name, name) {
			return l.Id, nil
		}
	}

	// create if not exists
	label, err := c.svc.Users.Labels.Create("me", &gmail.Label{
		Name:                  name,
		LabelListVisibility:   "labelShow",
		MessageListVisibility: "show",
	}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return label.Id, nil
}

func (c *Client) BatchMoveToLabel(ctx context.Context, messageIDs []string, labelName string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	labelID, err := c.getOrCreateLabel(ctx, labelName)
	if err != nil {
		return err
	}
	return c.svc.Users.Messages.BatchModify("me", &gmail.BatchModifyMessagesRequest{
		Ids:            messageIDs,
		AddLabelIds:    []string{labelID},
		RemoveLabelIds: []string{"INBOX"},
	}).Context(ctx).Do()
}

// FetchThreadIDsForMessages returns the thread ID for each given message ID.
func (c *Client) FetchThreadIDsForMessages(ctx context.Context, messageIDs []string) ([]string, error) {
	seen := make(map[string]struct{})
	var threadIDs []string
	for _, mid := range messageIDs {
		msg, err := c.svc.Users.Messages.Get("me", mid).Format("metadata").Context(ctx).Do()
		if err != nil {
			log.Printf("warning: could not fetch message %s: %v", mid, err)
			continue
		}
		if _, ok := seen[msg.ThreadId]; !ok {
			seen[msg.ThreadId] = struct{}{}
			threadIDs = append(threadIDs, msg.ThreadId)
		}
	}
	return threadIDs, nil
}

// ArchiveThreads removes INBOX from each thread, archiving whole conversations.
func (c *Client) ArchiveThreads(ctx context.Context, threadIDs []string) error {
	ok, failed := 0, 0
	for _, tid := range threadIDs {
		_, err := c.svc.Users.Threads.Modify("me", tid, &gmail.ModifyThreadRequest{
			RemoveLabelIds: []string{"INBOX"},
		}).Context(ctx).Do()
		if err != nil {
			log.Printf("archive thread failed %s: %v", tid, err)
			failed++
		} else {
			ok++
		}
		time.Sleep(50 * time.Millisecond)
	}
	log.Printf("archive threads: %d ok, %d failed", ok, failed)
	return nil
}

// FetchThreadEmails returns all messages in a Gmail thread.
func (c *Client) FetchThreadEmails(ctx context.Context, threadID string) ([]Email, error) {
	thread, err := c.svc.Users.Threads.Get("me", threadID).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	var emails []Email
	for _, msg := range thread.Messages {
		email := Email{ID: msg.Id, ThreadID: msg.ThreadId}
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
		emails = append(emails, email)
	}
	return emails, nil
}

// MoveThreadsToLabel applies a label and removes INBOX from each thread directly.
// Using threads.modify is more reliable than messages.batchModify for archiving
// whole conversations out of the inbox.
func (c *Client) MoveThreadsToLabel(ctx context.Context, threadIDs []string, labelName string) error {
	if len(threadIDs) == 0 {
		return nil
	}
	labelID, err := c.getOrCreateLabel(ctx, labelName)
	if err != nil {
		return err
	}
	ok, failed := 0, 0
	for _, tid := range threadIDs {
		_, err := c.svc.Users.Threads.Modify("me", tid, &gmail.ModifyThreadRequest{
			AddLabelIds:    []string{labelID},
			RemoveLabelIds: []string{"INBOX"},
		}).Context(ctx).Do()
		if err != nil {
			log.Printf("thread modify failed %s: %v", tid, err)
			failed++
		} else {
			ok++
		}
		time.Sleep(50 * time.Millisecond)
	}
	log.Printf("thread label move: %d ok, %d failed", ok, failed)
	return nil
}
