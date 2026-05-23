package gmail

import (
	"encoding/base64"
	"log"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"
)

var htmlTagRegex = regexp.MustCompile(`<[^>]+>`)
var whitespaceRegex = regexp.MustCompile(`\s+`)
var hrefRegex = regexp.MustCompile(`(?i)href="(https?://[^"]+)"`)

func stripHTMLAndWhitespace(s string) string {
	s = htmlTagRegex.ReplaceAllString(s, " ")
	s = whitespaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

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
			return stripHTMLAndWhitespace(strings.TrimSpace(string(data)))
		}
	}

	return ""
}
