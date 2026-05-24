package api

import (
	"context"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

type appStore interface {
	ListApplications(ctx context.Context) ([]domain.Application, error)
	FindApplicationById(ctx context.Context, id int64) (*domain.Application, error)
	FindOrCreateApplication(ctx context.Context, company, role, platform, language, url string, appliedAt time.Time) (int64, error)
	CreateStage(ctx context.Context, applicationID int64, status domain.Status, lastEmailID string, needsReview bool, appliedAt time.Time) (int64, error)
	GetStageByID(ctx context.Context, stageID int64) (*domain.ApplicationStage, error)
	GetThreadEmailSubjectAndBody(ctx context.Context, emailID string) (subject, body string)
	GetThreadEmailSubjectBodyDate(ctx context.Context, emailID string) (subject, body string, emailDate time.Time, err error)
	AddCorrection(ctx context.Context, emailID, emailSubject, emailBody string, wrongStatus domain.Status, correctStatus string) error
	UpdateStageStatus(ctx context.Context, stageID int64, status domain.Status, lastEmailID string) error
	DeleteStage(ctx context.Context, stageID int64) error
	MarkStageReviewed(ctx context.Context, stageID int64) error
	GetJourney(ctx context.Context, applicationID int64) ([]domain.ThreadConversation, error)
	LinkThreadEmailToStage(ctx context.Context, emailID string, stageID int64) error
	TagThreadEmailsForApplication(ctx context.Context, threadID string, applicationID int64) error
	AddCorrectionRule(ctx context.Context, rule string) error
	UnmarkProcessedEmailsForStage(ctx context.Context, stageID int64) error
}
