package sync

import (
	"regexp"
	"strings"
)

func extractDomainCompany(from string) string {
	re := regexp.MustCompile(`@([^.>]+)`)
	matches := re.FindStringSubmatch(strings.ToLower(from))
	if len(matches) > 1 {
		domain := matches[1]
		if invalidCompanyNames[domain] {
			return ""
		}
		return domain
	}
	return ""
}

var invalidCompanyNames = map[string]bool{
	"gmail": true, "google": true, "outlook": true, "microsoft": true,
	"zoom": true, "calendly": true, "cal": true, "slack": true,
	"linkedin": true, "indeed": true, "glassdoor": true,
	// job platforms — not companies that you applied for
	"wellfound": true, "angellist": true, "computrabajo": true,
	"lever": true, "greenhouse": true, "workday": true,
	"smartrecruiters": true, "recruitee": true, "bamboohr": true,
}

func isInvalidCompany(company string) bool {
	return company == "" || invalidCompanyNames[strings.ToLower(company)]
}

func extractCompanyFromBody(body string) string {
	// look for email addresses in body and extract non-scheduling domains
	re := regexp.MustCompile(`[\w.]+@([\w.-]+\.\w+)`)
	matches := re.FindAllStringSubmatch(strings.ToLower(body), -1)
	for _, m := range matches {
		if len(m) > 1 {
			domain := m[1]
			// skip common non-company domains
			skip := []string{"gmail.com", "cal.com", "google.com", "zoom.us", "microsoft.com", "calendly.com"}
			isSkip := false
			for _, s := range skip {
				if strings.Contains(domain, s) {
					isSkip = true
					break
				}
			}
			if !isSkip {
				// return just the company part e.g. "pelo.tech" -> "pelo"
				parts := strings.Split(domain, ".")
				if len(parts) > 0 {
					return parts[0]
				}
			}
		}
	}
	return ""
}

func extractCompanyFromSubject(subject string) string {
	lower := strings.ToLower(subject)
	for _, prefix := range []string{" at ", " bei ", " @ "} {
		idx := strings.LastIndex(lower, prefix)
		if idx == -1 {
			continue
		}
		candidate := strings.TrimSpace(subject[idx+len(prefix):])
		candidate = strings.TrimRight(candidate, ".,!?;:")
		if candidate != "" && len(candidate) <= 60 {
			return candidate
		}
	}
	return ""
}

func extractNamesFromSchedulingEmail(subject, body, myName string) []string {
	myLower := strings.ToLower(myName)
	seen := map[string]bool{}
	var names []string

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if myLower != "" && strings.Contains(strings.ToLower(name), myLower) {
			return
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	betweenRe := regexp.MustCompile(`(?i)between ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+) and ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)`)
	withRe := regexp.MustCompile(`(?i)(?:meeting|interview|call) with ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)`)
	if m := betweenRe.FindStringSubmatch(subject); m != nil {
		add(m[1])
		add(m[2])
	} else if m := withRe.FindStringSubmatch(subject); m != nil {
		add(m[1])
	}

	organizerRe := regexp.MustCompile(`([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)\s*[-–]\s*(?:Organizer|Host)`)
	for _, m := range organizerRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}

	youAndRe := regexp.MustCompile(`You & ([A-Z][a-z]+(?:\s[A-Z][a-z]+)+)`)
	for _, m := range youAndRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}

	emailRe := regexp.MustCompile(`([\w]+(?:\.[\w]+)+)@([\w.-]+\.[a-z]{2,})`)
	skipEmailDomains := map[string]bool{
		"cal.com": true, "calendly.com": true, "gmail.com": true,
		"google.com": true, "zoom.us": true, "microsoft.com": true,
	}
	myEmailLower := ""
	if myName != "" {
		myEmailLower = strings.ToLower(strings.ReplaceAll(myName, " ", "."))
	}
	for _, m := range emailRe.FindAllStringSubmatch(strings.ToLower(body), -1) {
		if len(m) < 3 || skipEmailDomains[m[2]] {
			continue
		}
		localPart := m[1]
		if myEmailLower != "" && strings.Contains(localPart, myEmailLower) {
			continue
		}
		parts := strings.Split(localPart, ".")
		if len(parts) < 2 {
			continue
		}
		var titleParts []string
		for _, p := range parts {
			if len(p) > 1 {
				titleParts = append(titleParts, strings.ToUpper(p[:1])+p[1:])
			}
		}
		if len(titleParts) >= 2 {
			add(strings.Join(titleParts, " "))
		}
	}

	return names
}
