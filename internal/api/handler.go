package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
	llmClient "github.com/vr33ni-dev/gmail-job-tracker/internal/llm"
	syncsvc "github.com/vr33ni-dev/gmail-job-tracker/internal/sync"
)

type Handler struct {
	store appStore
	sync  *syncsvc.Service
	llm   *llmClient.Client
}

func NewHandler(store *db.Store, llm *llmClient.Client) *Handler {
	return &Handler{store: store, llm: llm}
}

func (h *Handler) SetSync(svc *syncsvc.Service) {
	h.sync = svc
}

func (h *Handler) authStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if auth.IsConnected() {
		_, _ = w.Write([]byte(`{"connected":true}`))
	} else {
		_, _ = w.Write([]byte(`{"connected":false}`))
	}
}

func (h *Handler) listApplications(w http.ResponseWriter, r *http.Request) {
	filter := domain.ApplicationFilter{
		Company: r.URL.Query().Get("company"),
		SortBy:  r.URL.Query().Get("sort_by"),
		SortDir: r.URL.Query().Get("sort_dir"),
	}
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		tmp, err := time.Parse("2006-01-02", fromStr)
		if err != nil {
			http.Error(w, "invalid from date", http.StatusBadRequest)
			return
		}
		filter.From = tmp
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		tmp, err := time.Parse("2006-01-02", toStr)
		if err != nil {
			http.Error(w, "invalid to date", http.StatusBadRequest)
			return
		}
		filter.To = tmp
	}
	apps, err := h.store.ListApplications(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if apps == nil {
		apps = []domain.Application{}
	}
	writeJSON(w, apps)
}

func (h *Handler) getApplicationById(w http.ResponseWriter, r *http.Request) {
	appID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	app, err := h.store.FindApplicationById(r.Context(), appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if app == nil {
		http.Error(w, "application not found", http.StatusNotFound)
		return
	}
	writeJSON(w, app)
}

func (h *Handler) createApplication(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Company  string `json:"company"`
		Role     string `json:"role"`
		Platform string `json:"platform"`
		Language string `json:"language"`
		Status   string `json:"status"`
		URL      string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Status == "" {
		req.Status = "applied"
	}
	appID, err := h.store.FindOrCreateApplication(r.Context(), req.Company, req.Role, req.Platform, req.Language, req.URL, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stageID, err := h.store.CreateStage(r.Context(), appID, domain.Status(req.Status), "", false, time.Now())
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateStage) {
			http.Error(w, "a stage of this type already exists for this application", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]int64{"application_id": appID, "stage_id": stageID})
}

func (h *Handler) getNotesByApplicationID(w http.ResponseWriter, r *http.Request) {
	appID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid application id", http.StatusBadRequest)
		return
	}
	notes, err := h.store.FindNotesByApplicationID(r.Context(), appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if notes == nil {
		notes = []*domain.Note{}
	}
	writeJSON(w, notes)
}

func (h *Handler) triggerSync(w http.ResponseWriter, r *http.Request) {
	if h.sync == nil {
		http.Error(w, `{"error":"gmail not connected"}`, http.StatusServiceUnavailable)
		return
	}
	go func() {
		if err := h.sync.SyncAll(context.Background()); err != nil {
			log.Printf("sync error: %v", err)
		}
	}()
	writeJSON(w, map[string]string{"status": "sync triggered"})
}

func (h *Handler) triggerSelfHeal(w http.ResponseWriter, r *http.Request) {
	if h.sync == nil {
		http.Error(w, `{"error":"gmail not connected"}`, http.StatusServiceUnavailable)
		return
	}
	go func() {
		if err := h.sync.SelfHeal(context.Background()); err != nil {
			log.Printf("manual self-heal error: %v", err)
		}
	}()
	writeJSON(w, map[string]string{"status": "self-heal triggered"})
}

func (h *Handler) syncCompany(w http.ResponseWriter, r *http.Request) {
	if h.sync == nil {
		http.Error(w, `{"error":"gmail not connected"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Company string `json:"company"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Company == "" {
		http.Error(w, "company name required", http.StatusBadRequest)
		return
	}
	go func() {
		if err := h.sync.SyncCompany(context.Background(), req.Company); err != nil {
			log.Printf("company sync error: %v", err)
		}
	}()
	writeJSON(w, map[string]string{"status": "sync triggered", "company": req.Company})
}

func (h *Handler) correctApplication(w http.ResponseWriter, r *http.Request) {
	stageID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stage, err := h.store.GetStageByID(r.Context(), stageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	emailSubject, emailBody := h.store.GetThreadEmailSubjectAndBody(r.Context(), stage.LastEmailID)
	newStatus := domain.Status(body.Status)
	if err := h.store.AddCorrection(r.Context(), stage.LastEmailID, emailSubject, emailBody, stage.Status, string(newStatus)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateStageStatus(r.Context(), stageID, newStatus, stage.LastEmailID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *Handler) deleteApplication(w http.ResponseWriter, r *http.Request) {
	stageID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	stage, err := h.store.GetStageByID(r.Context(), stageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stage.LastEmailID != "" {
		emailSubject, emailBody := h.store.GetThreadEmailSubjectAndBody(r.Context(), stage.LastEmailID)
		_ = h.store.AddCorrection(r.Context(), stage.LastEmailID, emailSubject, emailBody, stage.Status, "skip")
	}
	_ = h.store.UnmarkProcessedEmailsForStage(r.Context(), stageID)
	if err := h.store.DeleteStage(r.Context(), stageID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) demoteApplication(w http.ResponseWriter, r *http.Request) {
	stageID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	stage, err := h.store.GetStageByID(r.Context(), stageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stage.LastEmailID != "" {
		emailSubject, emailBody := h.store.GetThreadEmailSubjectAndBody(r.Context(), stage.LastEmailID)
		_ = h.store.AddCorrection(r.Context(), stage.LastEmailID, emailSubject, emailBody, stage.Status, "conversation")
	}
	if err := h.store.DeleteStage(r.Context(), stageID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// promoteThreadEmail promotes a thread email to an application stage with a specified status
func (h *Handler) promoteThreadEmail(w http.ResponseWriter, r *http.Request) {
	emailID := chi.URLParam(r, "id")
	if emailID == "" {
		http.Error(w, "invalid email id", http.StatusBadRequest)
		return
	}

	var req struct {
		Status        string `json:"status"`
		ApplicationID int64  `json:"application_id"`
		ThreadID      string `json:"thread_id"` // used only for tagging sibling emails
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Record correction: this email was not initially classified correctly, but should have been
	emailSubject, emailBody, emailDate, err := h.store.GetThreadEmailSubjectBodyDate(r.Context(), emailID)
	if err != nil {
		http.Error(w, "failed to load email metadata", http.StatusInternalServerError)
		return
	}
	if emailDate.IsZero() {
		emailDate = time.Now()
	}
	_ = h.store.AddCorrection(r.Context(), emailID, emailSubject, emailBody, "conversation", req.Status)

	// Create a new application stage using the original email date
	stageID, err := h.store.CreateStage(r.Context(), req.ApplicationID, domain.Status(req.Status), emailID, false, emailDate)
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateStage) {
			http.Error(w, "a stage of this type already exists for this application", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Link the thread email to this new stage
	if err := h.store.LinkThreadEmailToStage(r.Context(), emailID, stageID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Tag all emails in the same thread with this application so journey includes them
	if req.ThreadID != "" {
		_ = h.store.TagThreadEmailsForApplication(r.Context(), req.ThreadID, req.ApplicationID)
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]int64{"stage_id": stageID})
}

func (h *Handler) addCorrectionRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule string `json:"rule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Rule == "" {
		http.Error(w, "rule text required", http.StatusBadRequest)
		return
	}
	if err := h.store.AddCorrectionRule(r.Context(), req.Rule); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"status": "created"})
}

func (h *Handler) suggestRule(w http.ResponseWriter, r *http.Request) {
	stageID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		WrongStatus   string `json:"wrong_status"`
		CorrectStatus string `json:"correct_status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stage, err := h.store.GetStageByID(r.Context(), stageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	emailSubject, emailBody := h.store.GetThreadEmailSubjectAndBody(r.Context(), stage.LastEmailID)
	rule := ""
	if h.llm != nil {
		if suggested, llmErr := h.llm.SuggestRule(r.Context(), emailSubject, emailBody, req.WrongStatus, req.CorrectStatus); llmErr == nil {
			rule = suggested
		} else {
			log.Printf("suggestRule LLM error: %v", llmErr)
		}
	}
	writeJSON(w, map[string]string{"rule": rule})
}

func (h *Handler) markReviewed(w http.ResponseWriter, r *http.Request) {
	stageID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.store.MarkStageReviewed(r.Context(), stageID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) getJourney(w http.ResponseWriter, r *http.Request) {
	applicationID, err := parseID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if h.sync != nil {
		if err := h.sync.BackfillThreads(r.Context(), applicationID); err != nil {
			log.Printf("thread backfill app %d: %v", applicationID, err)
		}
	}
	groups, err := h.store.GetJourney(r.Context(), applicationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, groups)
}
