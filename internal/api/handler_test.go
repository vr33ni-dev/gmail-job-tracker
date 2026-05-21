package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
)

// mockStore lets each test set only the functions it needs; all others panic
// if called unexpectedly, making missing stubs obvious.
type mockStore struct {
	listApplications              func(ctx context.Context) ([]domain.Application, error)
	findApplicationById           func(ctx context.Context, id int64) (*domain.Application, error)
	findOrCreateApplication       func(ctx context.Context, company, role, platform, language, url string, appliedAt time.Time) (int64, error)
	createStage                   func(ctx context.Context, applicationID int64, status domain.Status, lastEmailID string, needsReview bool, appliedAt time.Time) (int64, error)
	getStageByID                  func(ctx context.Context, stageID int64) (*domain.ApplicationStage, error)
	getThreadEmailSubjectAndBody  func(ctx context.Context, emailID string) (string, string)
	getThreadEmailSubjectBodyDate func(ctx context.Context, emailID string) (string, string, time.Time, error)
	addCorrection                 func(ctx context.Context, emailID, subject, body string, wrong domain.Status, correct string) error
	updateStageStatus             func(ctx context.Context, stageID int64, status domain.Status, lastEmailID string) error
	deleteStage                   func(ctx context.Context, stageID int64) error
	markStageReviewed             func(ctx context.Context, stageID int64) error
	getJourney                    func(ctx context.Context, applicationID int64) ([]domain.ThreadConversation, error)
	linkThreadEmailToStage        func(ctx context.Context, emailID string, stageID int64) error
	tagThreadEmailsForApp         func(ctx context.Context, threadID string, applicationID int64) error
	addCorrectionRule             func(ctx context.Context, rule string) error
}

func (m *mockStore) ListApplications(ctx context.Context) ([]domain.Application, error) {
	return m.listApplications(ctx)
}
func (m *mockStore) FindApplicationById(ctx context.Context, id int64) (*domain.Application, error) {
	return m.findApplicationById(ctx, id)
}
func (m *mockStore) FindOrCreateApplication(ctx context.Context, company, role, platform, language, url string, appliedAt time.Time) (int64, error) {
	return m.findOrCreateApplication(ctx, company, role, platform, language, url, appliedAt)
}
func (m *mockStore) CreateStage(ctx context.Context, applicationID int64, status domain.Status, lastEmailID string, needsReview bool, appliedAt time.Time) (int64, error) {
	return m.createStage(ctx, applicationID, status, lastEmailID, needsReview, appliedAt)
}
func (m *mockStore) GetStageByID(ctx context.Context, stageID int64) (*domain.ApplicationStage, error) {
	return m.getStageByID(ctx, stageID)
}
func (m *mockStore) GetThreadEmailSubjectAndBody(ctx context.Context, emailID string) (string, string) {
	return m.getThreadEmailSubjectAndBody(ctx, emailID)
}
func (m *mockStore) GetThreadEmailSubjectBodyDate(ctx context.Context, emailID string) (string, string, time.Time, error) {
	return m.getThreadEmailSubjectBodyDate(ctx, emailID)
}
func (m *mockStore) AddCorrection(ctx context.Context, emailID, subject, body string, wrong domain.Status, correct string) error {
	return m.addCorrection(ctx, emailID, subject, body, wrong, correct)
}
func (m *mockStore) UpdateStageStatus(ctx context.Context, stageID int64, status domain.Status, lastEmailID string) error {
	return m.updateStageStatus(ctx, stageID, status, lastEmailID)
}
func (m *mockStore) DeleteStage(ctx context.Context, stageID int64) error {
	return m.deleteStage(ctx, stageID)
}
func (m *mockStore) MarkStageReviewed(ctx context.Context, stageID int64) error {
	return m.markStageReviewed(ctx, stageID)
}
func (m *mockStore) GetJourney(ctx context.Context, applicationID int64) ([]domain.ThreadConversation, error) {
	return m.getJourney(ctx, applicationID)
}
func (m *mockStore) LinkThreadEmailToStage(ctx context.Context, emailID string, stageID int64) error {
	return m.linkThreadEmailToStage(ctx, emailID, stageID)
}
func (m *mockStore) TagThreadEmailsForApplication(ctx context.Context, threadID string, applicationID int64) error {
	return m.tagThreadEmailsForApp(ctx, threadID, applicationID)
}
func (m *mockStore) AddCorrectionRule(ctx context.Context, rule string) error {
	return m.addCorrectionRule(ctx, rule)
}

// newTestHandler builds a Handler with the given mock store and no sync/llm.
func newTestHandler(ms *mockStore) *Handler {
	return &Handler{store: ms}
}

// routeWith wires a single route on a chi router so URL params are parsed correctly.
func routeWith(method, pattern string, h http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Method(method, pattern, h)
	return r
}

// ── listApplications ──────────────────────────────────────────────────────────

func TestListApplications_OK(t *testing.T) {
	apps := []domain.Application{
		{ID: 1, Company: "Acme", Role: "Engineer"},
	}
	h := newTestHandler(&mockStore{
		listApplications: func(_ context.Context) ([]domain.Application, error) { return apps, nil },
	})

	req := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
	rec := httptest.NewRecorder()
	h.listApplications(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []domain.Application
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Company != "Acme" {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestListApplications_EmptySlice(t *testing.T) {
	h := newTestHandler(&mockStore{
		listApplications: func(_ context.Context) ([]domain.Application, error) { return nil, nil },
	})

	req := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
	rec := httptest.NewRecorder()
	h.listApplications(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []domain.Application
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
}

func TestListApplications_StoreError(t *testing.T) {
	h := newTestHandler(&mockStore{
		listApplications: func(_ context.Context) ([]domain.Application, error) {
			return nil, errors.New("db error")
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
	rec := httptest.NewRecorder()
	h.listApplications(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// ── getApplicationById ────────────────────────────────────────────────────────

func TestGetApplicationById_OK(t *testing.T) {
	app := &domain.Application{ID: 42, Company: "Google", Role: "SWE"}
	h := newTestHandler(&mockStore{
		findApplicationById: func(_ context.Context, id int64) (*domain.Application, error) {
			if id != 42 {
				t.Errorf("got id=%d, want 42", id)
			}
			return app, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/applications/42", nil)
	rec := httptest.NewRecorder()
	routeWith(http.MethodGet, "/api/applications/{id}", h.getApplicationById).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got domain.Application
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != 42 {
		t.Fatalf("id = %d, want 42", got.ID)
	}
}

func TestGetApplicationById_NotFound(t *testing.T) {
	h := newTestHandler(&mockStore{
		findApplicationById: func(_ context.Context, _ int64) (*domain.Application, error) {
			return nil, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/applications/99", nil)
	rec := httptest.NewRecorder()
	routeWith(http.MethodGet, "/api/applications/{id}", h.getApplicationById).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetApplicationById_InvalidID(t *testing.T) {
	h := newTestHandler(&mockStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/applications/abc", nil)
	rec := httptest.NewRecorder()
	routeWith(http.MethodGet, "/api/applications/{id}", h.getApplicationById).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ── createApplication ─────────────────────────────────────────────────────────

func TestCreateApplication_OK(t *testing.T) {
	h := newTestHandler(&mockStore{
		findOrCreateApplication: func(_ context.Context, _, _, _, _, _ string, _ time.Time) (int64, error) {
			return 10, nil
		},
		createStage: func(_ context.Context, appID int64, status domain.Status, _ string, _ bool, _ time.Time) (int64, error) {
			if appID != 10 {
				t.Errorf("appID = %d, want 10", appID)
			}
			if status != domain.StatusApplied {
				t.Errorf("status = %q, want %q", status, domain.StatusApplied)
			}
			return 20, nil
		},
	})

	body := `{"company":"Acme","role":"Engineer","platform":"LinkedIn","language":"en"}`
	req := httptest.NewRequest(http.MethodPost, "/api/applications", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.createApplication(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var got map[string]int64
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["application_id"] != 10 || got["stage_id"] != 20 {
		t.Fatalf("unexpected ids: %+v", got)
	}
}

func TestCreateApplication_DuplicateStage(t *testing.T) {
	h := newTestHandler(&mockStore{
		findOrCreateApplication: func(_ context.Context, _, _, _, _, _ string, _ time.Time) (int64, error) {
			return 10, nil
		},
		createStage: func(_ context.Context, _ int64, _ domain.Status, _ string, _ bool, _ time.Time) (int64, error) {
			return 0, domain.ErrDuplicateStage
		},
	})

	body := `{"company":"Acme","role":"Engineer"}`
	req := httptest.NewRequest(http.MethodPost, "/api/applications", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.createApplication(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// ── deleteApplication ─────────────────────────────────────────────────────────

func TestDeleteApplication_OK(t *testing.T) {
	h := newTestHandler(&mockStore{
		getStageByID: func(_ context.Context, id int64) (*domain.ApplicationStage, error) {
			return &domain.ApplicationStage{ID: id, LastEmailID: ""}, nil
		},
		deleteStage: func(_ context.Context, _ int64) error { return nil },
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/applications/5", nil)
	rec := httptest.NewRecorder()
	routeWith(http.MethodDelete, "/api/applications/{id}", h.deleteApplication).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestDeleteApplication_WithEmail_RecordsCorrection(t *testing.T) {
	correctionRecorded := false
	h := newTestHandler(&mockStore{
		getStageByID: func(_ context.Context, id int64) (*domain.ApplicationStage, error) {
			return &domain.ApplicationStage{ID: id, LastEmailID: "email-abc", Status: domain.StatusInterview}, nil
		},
		getThreadEmailSubjectAndBody: func(_ context.Context, _ string) (string, string) {
			return "Subject", "Body"
		},
		addCorrection: func(_ context.Context, emailID, _, _ string, _ domain.Status, correct string) error {
			if emailID != "email-abc" || correct != "skip" {
				t.Errorf("unexpected correction: emailID=%s correct=%s", emailID, correct)
			}
			correctionRecorded = true
			return nil
		},
		deleteStage: func(_ context.Context, _ int64) error { return nil },
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/applications/5", nil)
	rec := httptest.NewRecorder()
	routeWith(http.MethodDelete, "/api/applications/{id}", h.deleteApplication).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if !correctionRecorded {
		t.Fatal("expected correction to be recorded")
	}
}

// ── correctApplication ────────────────────────────────────────────────────────

func TestCorrectApplication_OK(t *testing.T) {
	h := newTestHandler(&mockStore{
		getStageByID: func(_ context.Context, _ int64) (*domain.ApplicationStage, error) {
			return &domain.ApplicationStage{ID: 7, Status: domain.StatusInterview, LastEmailID: "e1"}, nil
		},
		getThreadEmailSubjectAndBody: func(_ context.Context, _ string) (string, string) {
			return "subj", "body"
		},
		addCorrection:     func(_ context.Context, _, _, _ string, _ domain.Status, _ string) error { return nil },
		updateStageStatus: func(_ context.Context, _ int64, _ domain.Status, _ string) error { return nil },
	})

	body := `{"status":"offer"}`
	req := httptest.NewRequest(http.MethodPost, "/api/applications/7/correct", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	routeWith(http.MethodPost, "/api/applications/{id}/correct", h.correctApplication).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// ── markReviewed ──────────────────────────────────────────────────────────────

func TestMarkReviewed_OK(t *testing.T) {
	var calledWith int64
	h := newTestHandler(&mockStore{
		markStageReviewed: func(_ context.Context, id int64) error {
			calledWith = id
			return nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/applications/3/reviewed", nil)
	rec := httptest.NewRecorder()
	routeWith(http.MethodPost, "/api/applications/{id}/reviewed", h.markReviewed).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calledWith != 3 {
		t.Fatalf("markStageReviewed called with %d, want 3", calledWith)
	}
}

// ── addCorrectionRule ─────────────────────────────────────────────────────────

func TestAddCorrectionRule_OK(t *testing.T) {
	var savedRule string
	h := newTestHandler(&mockStore{
		addCorrectionRule: func(_ context.Context, rule string) error {
			savedRule = rule
			return nil
		},
	})

	body := `{"rule":"if subject contains 'coding challenge' classify as ai_interview"}`
	req := httptest.NewRequest(http.MethodPost, "/api/corrections/rule", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.addCorrectionRule(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if savedRule == "" {
		t.Fatal("rule was not saved")
	}
}

func TestAddCorrectionRule_EmptyRule(t *testing.T) {
	h := newTestHandler(&mockStore{})

	body := `{"rule":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/corrections/rule", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.addCorrectionRule(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
