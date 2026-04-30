package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/domain"
	syncsvc "github.com/vr33ni-dev/gmail-job-tracker/internal/sync"
)

type Handler struct {
	store *db.Store
	sync  *syncsvc.Service // nil until Gmail is connected
}

func NewHandler(store *db.Store, sync *syncsvc.Service) *Handler {
	return &Handler{store: store, sync: sync}
}

func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Get("/api/applications", h.listApplications)
	r.Post("/api/applications", h.createApplication)
	r.Post("/api/sync", h.triggerSync)

	return r
}

func (h *Handler) listApplications(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.ListApplications(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if apps == nil {
		apps = []domain.Application{}
	}
	writeJSON(w, apps)
}

func (h *Handler) createApplication(w http.ResponseWriter, r *http.Request) {
	var app domain.Application
	if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertApplication(r.Context(), &app); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, app)
}

func (h *Handler) triggerSync(w http.ResponseWriter, r *http.Request) {
	if h.sync == nil {
		http.Error(w, `{"error":"gmail not connected"}`, http.StatusServiceUnavailable)
		return
	}
	go func() {
		if err := h.sync.Run(context.Background()); err != nil {
			log.Printf("sync error: %v", err)
		}
	}()
	writeJSON(w, map[string]string{"status": "sync triggered"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
