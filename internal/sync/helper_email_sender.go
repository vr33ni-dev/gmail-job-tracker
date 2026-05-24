package sync

import (
	"regexp"
	"strings"
)

func isNoise(from string) bool {
	lower := strings.ToLower(from)
	return strings.Contains(lower, "emailsys1a.net") ||
		strings.Contains(lower, "calendar-notification@google.com") ||
		strings.Contains(lower, "calendar-server.bounces.google.com")
}

func isSchedulingService(from string) bool {
	lower := strings.ToLower(from)
	return strings.Contains(lower, "cal.com") ||
		strings.Contains(lower, "calendly.com") ||
		strings.Contains(lower, "savvycal.com") ||
		strings.Contains(lower, "chilipiper.com")
}

func extractDisplayName(from string) string {
	re := regexp.MustCompile(`^"?([^"<]+?)"?\s*<`)
	m := re.FindStringSubmatch(strings.TrimSpace(from))
	if len(m) < 2 {
		return ""
	}
	name := strings.TrimSpace(m[1])
	lower := strings.ToLower(name)
	for _, skip := range []string{"noreply", "no-reply", "google", "calendly", "zoom", "microsoft", "linkedin", "upwork"} {
		if strings.Contains(lower, skip) {
			return ""
		}
	}
	return name
}

func senderDisplayName(from string) string {
	re := regexp.MustCompile(`^"?([^"<]+?)"?\s*<`)
	m := re.FindStringSubmatch(strings.TrimSpace(from))
	if len(m) < 2 {
		return ""
	}
	name := strings.TrimSpace(m[1])
	lower := strings.ToLower(name)
	for _, skip := range []string{"noreply", "no-reply", "google", "tl;dv", "calendly", "zoom", "microsoft", "linkedin", "upwork"} {
		if strings.Contains(lower, skip) {
			return ""
		}
	}
	// two-word name where both words start with a capital → likely a person
	parts := strings.Fields(name)
	if len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0 &&
		parts[0][0] >= 'A' && parts[0][0] <= 'Z' &&
		parts[1][0] >= 'A' && parts[1][0] <= 'Z' {
		return ""
	}
	return name
}
