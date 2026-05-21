package domain

import "testing"

func TestNormalizeCompany(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"basic", "Google", "Google"},
		{"trimming", "  Google  ", "Google"},
		{"remove suffixes 1", "Google GmbH", "Google"},
		{"remove suffixes 2", "Google Inc.", "Google"},
		{"remove suffixes 3", "Google LLC", "Google"},
		{"remove suffixes 4", "Google Corporation", "Google"},
		{"remove suffixes 5", "Google Ltd.", "Google"},
		{"remove suffixes 6", "Google AG", "Google"},
		{"remove suffixes 7", "Google GmbH & Co. KG", "Google"},
		{"remove suffixes 8", "Google B.V.", "Google"},
		{"remove suffixes 9", "Google S.A.", "Google"},
		{"remove suffixes 10", "Google S.R.L.", "Google"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeCompany(tt.input); got != tt.expected {
				t.Errorf("NormalizeCompanyName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeRole(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"basic", "Software Engineer", "Software Engineer"},
		{"trimming", "  Software Engineer  ", "Software Engineer"},
		{"remove suffixes 1", "Software Engineer (m/f/d)", "Software Engineer"},
		{"remove suffixes 2", "Software Engineer (m/w/d)", "Software Engineer"},
		{"remove suffixes 3", "Software Engineer (f/m/d)", "Software Engineer"},
		{"remove suffixes 4", "Software Engineer (m/f/x)", "Software Engineer"},
		{"remove suffixes 5", "Software Engineer (f/m/x)", "Software Engineer"},
		{"remove suffixes 6", "Software Engineer (gn)", "Software Engineer"},
		{"strip location", "Software Engineer | Berlin", "Software Engineer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeRole(tt.input); got != tt.expected {
				t.Errorf("NormalizeRole(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
