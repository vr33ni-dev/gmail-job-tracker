package domain

import "strings"

func NormalizeRole(role string) string {
	role = strings.TrimSpace(role)
	role = strings.ReplaceAll(role, " (m/f/d)", "")
	role = strings.ReplaceAll(role, " (m/w/d)", "")
	role = strings.ReplaceAll(role, " (f/m/d)", "")
	role = strings.ReplaceAll(role, " (m/f/x)", "")
	role = strings.ReplaceAll(role, " (f/m/x)", "")
	// strip location suffixes
	if idx := strings.Index(role, " | "); idx != -1 {
		role = role[:idx]
	}
	return strings.TrimSpace(role)
}
