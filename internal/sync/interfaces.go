package sync

import (
	"context"
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

type emailParser interface {
	ParseJobEmail(ctx context.Context, subject, body, from string, existingStages []domain.ApplicationStage) (*domain.ParsedEmail, error)
}

type syncStore interface {
	GetAllJobEmailIDs(ctx context.Context) ([]string, error)
	GetAllJobThreadIDs(ctx context.Context) ([]string, error)
	LastPollTime(ctx context.Context) (time.Time, error)
	GetRecentCorrections(ctx context.Context, limit int) ([]domain.Correction, error)

	GetThreadApplicationID(ctx context.Context, threadID string) int64
	StoreThreadEmail(ctx context.Context, emailID, threadID, from, subject, body string, date time.Time, applicationID int64) error
	LinkThreadEmailToStage(ctx context.Context, emailID string, stageID int64) error

	IsEmailProcessed(ctx context.Context, emailID string) (bool, error)
	MarkEmailProcessed(ctx context.Context, emailID string) error
	UnmarkEmailProcessed(ctx context.Context, emailID string) error
	IsEmailInThreadEmails(ctx context.Context, emailID string) (bool, error)

	FindOrCreateApplication(ctx context.Context, company, role, platform, language, source string, appliedAt time.Time) (int64, error)
	ApplicationExistsByEmailID(ctx context.Context, emailID string) (bool, error)
	GetStageByLastEmailID(ctx context.Context, emailID string) (*domain.ApplicationStage, error)

	ListApplications(ctx context.Context) ([]domain.Application, error)
	FindApplicationById(ctx context.Context, id int64) (*domain.Application, error)
	FindApplicationByCompanyAndRole(ctx context.Context, company, role string) (*domain.Application, error)
	FindMostRecentByCompany(ctx context.Context, company string) (*domain.Application, error)
	FindApplicationByPersonName(ctx context.Context, name string) (*domain.Application, error)

	GetStagesByStatus(ctx context.Context, applicationID int64, status domain.Status) ([]domain.ApplicationStage, error)
	CreateStage(ctx context.Context, applicationID int64, status domain.Status, emailID string, inferred bool, appliedAt time.Time) (int64, error)
	HasAppliedStage(ctx context.Context, applicationID int64) (bool, error)
	FixAppliedStageDate(ctx context.Context, applicationID, stageID int64) error

	GetContactFromAddrsForApp(ctx context.Context, appID int64, userEmail string) ([]string, error)
	GetThreadIDsByApplication(ctx context.Context, applicationID int64) ([]string, error)
	FixEmptyRoles(ctx context.Context) error
	FixAllAppliedStageDates(ctx context.Context) error

	ResolveCompanyAlias(ctx context.Context, company string) string
}
