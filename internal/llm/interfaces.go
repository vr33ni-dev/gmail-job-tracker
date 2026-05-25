package llm

import (
	"context"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

type correctionStore interface {
	GetRecentCorrections(ctx context.Context, limit int) ([]domain.Correction, error)
}
