package domain

import "strings"

func NormalizeRole(role string) string {
	role = strings.TrimSpace(role)
	role = strings.ReplaceAll(role, " (m/f/d)", "")
	role = strings.ReplaceAll(role, " (m/w/d)", "")
	role = strings.ReplaceAll(role, " (f/m/d)", "")
	role = strings.ReplaceAll(role, " (m/f/x)", "")
	role = strings.ReplaceAll(role, " (f/m/x)", "")
	role = strings.ReplaceAll(role, " (gn)", "")
	// strip location suffixes
	if idx := strings.Index(role, " | "); idx != -1 {
		role = role[:idx]
	}
	return strings.TrimSpace(role)
}

// legalSuffixes lists common legal entity suffixes to strip when normalizing company names.
var legalSuffixes = []string{
	" gmbh & co. kg", " gmbh & co kg",
	" ag & co. kg",
	" gmbh", " ag", " se",
	" corporation", " corp.", " corp",
	" limited", " ltd.", " ltd",
	" incorporated", " inc.", " inc",
	" llc", " l.l.c.",
	" b.v.", " bv",
	" s.a.", " sa",
	" s.r.l.", " srl",
}

// NormalizeCompany strips legal entity suffixes so "CoreWillSoft" and "CoreWillSoft GmbH"
// resolve to the same application.
func NormalizeCompany(company string) string {
	company = strings.TrimSpace(company)
	lower := strings.ToLower(company)
	for _, suffix := range legalSuffixes {
		if strings.HasSuffix(lower, suffix) {
			company = strings.TrimSpace(company[:len(company)-len(suffix)])
			break
		}
	}
	return company
}
