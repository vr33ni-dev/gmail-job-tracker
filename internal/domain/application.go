package domain

import "time"

type Status string

const (
	StatusApplied     Status = "applied"
	StatusInterview   Status = "interview"
	StatusAIInterview Status = "ai_interview"
	StatusOffer       Status = "offer"
	StatusRejected    Status = "rejected"
	StatusWithdrawn   Status = "withdrawn"
)

type Application struct {
	ID          int64     `json:"id"`
	Company     string    `json:"company"`
	Role        string    `json:"role"`
	Platform    string    `json:"platform"`
	AppliedAt   time.Time `json:"applied_at"`
	Status      Status    `json:"status"`
	LastEmailID string    `json:"last_email_id"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	EmailBody   string    `json:"email_body"`
	Language    string    `json:"language"`
}

type ApplicationStage struct {
	ID          int64     `json:"id"`
	Status      Status    `json:"status"`
	AppliedAt   time.Time `json:"applied_at"`
	EmailBody   string    `json:"email_body"`
	LastEmailID string    `json:"last_email_id"`
}

type GroupedApplication struct {
	Company       string             `json:"company"`
	Role          string             `json:"role"`
	Platform      string             `json:"platform"`
	Language      string             `json:"language"`
	URL           string             `json:"url"`
	CurrentStatus Status             `json:"current_status"`
	AppliedAt     time.Time          `json:"applied_at"`
	Stages        []ApplicationStage `json:"stages"`
}
type StatusEvent struct {
	ID            int64     `json:"id"`
	ApplicationID int64     `json:"application_id"`
	FromStatus    Status    `json:"from_status"`
	ToStatus      Status    `json:"to_status"`
	EmailID       string    `json:"email_id"`
	EmailSubject  string    `json:"email_subject"`
	ParsedAt      time.Time `json:"parsed_at"`
}

type ParsedEmail struct {
	Company    string `json:"company"`
	Role       string `json:"role"`
	Status     Status `json:"status"`
	Confidence string `json:"confidence"`
	Summary    string `json:"summary"`
	Language   string `json:"language"`
	Platform   string `json:"platform"`
}

type Correction struct {
	EmailSubject  string `json:"email_subject"`
	EmailBody     string `json:"email_body"`
	WrongStatus   Status `json:"wrong_status"`
	CorrectStatus Status `json:"correct_status"`
}
