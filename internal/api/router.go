package api

import (
	"context"
	"log"
	"net/http"
	"os/exec"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/gmail"
	llmClient "github.com/vr33ni-dev/gmail-job-tracker/internal/llm"
	syncSvc "github.com/vr33ni-dev/gmail-job-tracker/internal/sync"

	"golang.org/x/oauth2"
)

func NewRouter(ctx context.Context, store *db.Store, cfg *oauth2.Config, userEmail, userName string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Use(corsMiddleware)

	llm := llmClient.NewClient(store)
	h := &Handler{store: store, llm: llm}

	startSync := func(email, name string) {
		token, _ := auth.LoadToken()
		gmailClient, err := gmail.NewClient(ctx, token, cfg)
		if err != nil {
			log.Printf("gmail init: %v", err)
			return
		}
		h.sync = syncSvc.NewService(store, gmailClient, llm, email, name)
		go h.sync.RunLoop(ctx, 15*time.Minute)
		if t, err := store.LastPollTime(ctx); err == nil {
			log.Printf("gmail sync started (running every 15 min) - last sync on: %s", t.Format("2006-01-02 15:04"))
		} else {
			log.Println("gmail sync started (running every 15 min)")
		}
	}

	if auth.IsConnected() {
		startSync(userEmail, userName)
	} else {
		log.Println("gmail not connected — opening browser to authenticate...")
		go func() {
			time.Sleep(500 * time.Millisecond)
			// use env var in the future
			exec.Command("open", "http://localhost:8080/auth/login").Start()
		}()
	}

	// Auth routes
	r.Get("/auth/login", auth.LoginHandler)
	r.Get("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		auth.CallbackHandler(w, r)
		token, err := auth.LoadToken()
		if err != nil {
			log.Printf("failed to load token after callback: %v", err)
			http.Error(w, "authentication failed — could not load token", http.StatusInternalServerError)
			return
		}
		log.Printf("gmail connected successfully, token expires at %s", token.Expiry.Format("2006-01-02 15:04"))

		if h.sync == nil {
			startSync(userEmail, userName)
		}

	})

	// Status endpoint
	r.Get("/auth/status", h.authStatus)

	r.Get("/api/applications", h.listApplications)
	r.Get("/api/applications/{id}", h.getApplicationById)
	r.Post("/api/applications", h.createApplication)
	r.Get("/api/applications/{id}/events", h.listEvents)
	r.Post("/api/sync", h.triggerSync)
	r.Post("/api/sync/company", h.syncCompany)
	r.Post("/api/applications/{id}/correct", h.correctApplication)
	r.Post("/api/applications/{id}/reviewed", h.markReviewed)
	r.Delete("/api/applications/{id}", h.deleteApplication)
	r.Post("/api/applications/{id}/demote", h.demoteApplication)
	r.Get("/api/applications/{id}/journey", h.getJourney)
	r.Post("/api/thread-emails/{id}/promote", h.promoteThreadEmail)
	r.Post("/api/corrections/rule", h.addCorrectionRule)
	r.Post("/api/applications/{id}/suggest-rule", h.suggestRule)
	return r
}
