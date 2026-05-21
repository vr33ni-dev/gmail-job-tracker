package domain

import (
	"errors"
	"time"
)

// ErrDuplicateStage is returned when trying to add a singleton stage that already exists.
var ErrDuplicateStage = errors.New("stage of this type already exists for this application")

// IsSingletonStage returns true for stage types that may only appear once per application.
func IsSingletonStage(s Status) bool {
	switch s {
	case StatusApplied, StatusOffer, StatusRejected, StatusWithdrawn:
		return true
	}
	return false
}

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
	ID            int64              `json:"id"`
	Company       string             `json:"company"`
	Role          string             `json:"role"`
	Platform      string             `json:"platform"`
	Language      string             `json:"language"`
	URL           string             `json:"url"`
	AppliedAt     time.Time          `json:"applied_at"`
	CurrentStatus Status             `json:"current_status"`
	LastUpdatedAt time.Time          `json:"last_updated_at"`
	GmailURL      string             `json:"gmail_url,omitempty"`
	Stages        []ApplicationStage `json:"stages"`
}

type ApplicationStage struct {
	ID            int64     `json:"id"`
	ApplicationID int64     `json:"application_id"`
	Status        Status    `json:"status"`
	LastEmailID   string    `json:"last_email_id"`
	NeedsReview   bool      `json:"needs_review"`
	AppliedAt     time.Time `json:"applied_at"`
}

type ParsedEmail struct {
	Company     string `json:"company"`
	Role        string `json:"role"`
	Status      Status `json:"status"`
	Confidence  string `json:"confidence"`
	Summary     string `json:"summary"`
	Language    string `json:"language"`
	Platform    string `json:"platform"`
	IsDuplicate bool   `json:"is_duplicate"`
}

type ThreadConversation struct {
	Stage        ThreadEmail   `json:"stage"`
	Conversation []ThreadEmail `json:"conversation"`
}

type ThreadEmail struct {
	ID        int64     `json:"id"`
	EmailID   string    `json:"email_id"`
	ThreadID  string    `json:"thread_id"`
	StageID   *int64    `json:"stage_id,omitempty"`
	FromAddr  string    `json:"from_addr"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	EmailDate time.Time `json:"email_date"`
	IsStage   bool      `json:"is_stage"`
}

type Correction struct {
	EmailSubject  string `json:"email_subject"`
	EmailBody     string `json:"email_body"`
	WrongStatus   string `json:"wrong_status"`
	CorrectStatus string `json:"correct_status"`
	Command       string `json:"command"`
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
