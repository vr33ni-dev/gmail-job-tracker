package sync

import (
	"time"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

type stageInfo struct {
	hasApplied   bool
	hasInterview bool
	earliest     time.Time
}

func getStageInfo(stages []domain.ApplicationStage) stageInfo {
	stageInfo := stageInfo{earliest: time.Now()}
	for _, st := range stages {
		if st.Status == domain.StatusInterview || st.Status == domain.StatusAIInterview {
			stageInfo.hasInterview = true
		}
		if st.Status == domain.StatusApplied {
			stageInfo.hasApplied = true
		}
		if st.AppliedAt.Before(stageInfo.earliest) {
			stageInfo.earliest = st.AppliedAt
		}
	}
	return stageInfo
}
