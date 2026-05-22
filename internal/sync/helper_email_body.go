package sync

import (
	"regexp"
	"strings"
)

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

func isGmailReaction(subject, body string) bool {
	if strings.Contains(strings.ToLower(subject), "reacted via gmail") {
		return true
	}
	// check only the first 100 chars of the body to avoid matching quoted reactions
	preview := body
	if len(preview) > 100 {
		preview = preview[:100]
	}
	return strings.Contains(strings.ToLower(preview), "reacted via gmail")
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
