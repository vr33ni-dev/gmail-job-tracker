package api

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"
)

func NewRouter(h *Handler, onAuthDone func()) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Use(corsMiddleware)

	// Auth routes
	r.Get("/auth/login", auth.LoginHandler)
	r.Get("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		auth.CallbackHandler(w, r)
		token, err := auth.LoadToken()
		if err != nil {
			http.Error(w, "authentication failed", http.StatusInternalServerError)
			return
		}
		log.Printf("gmail connected, token expires %s", token.Expiry.Format("2006-01-02 15:04"))
		if onAuthDone != nil {
			onAuthDone()
		}
	})

	// Status endpoint
	r.Get("/auth/status", h.authStatus)

	r.Route("/api/applications", func(r chi.Router) {
		r.Get("/", h.listApplications)
		r.Post("/", h.createApplication)
		r.Get("/{id}", h.getApplicationById)
		r.Get("/{id}/notes", h.getNotesByApplicationID)
		r.Post("/{id}/correct", h.correctApplication)
		r.Post("/{id}/reviewed", h.markReviewed)
		r.Delete("/{id}", h.deleteApplication)
		r.Post("/{id}/demote", h.demoteApplication)
		r.Get("/{id}/journey", h.getJourney)
		r.Post("/{id}/suggest-rule", h.suggestRule)
	})

	r.Route("/api/sync", func(r chi.Router) {
		r.Post("/", h.triggerSync)
		r.Post("/company", h.syncCompany)
		r.Post("/heal", h.triggerSelfHeal)
	})

	r.Post("/api/thread-emails/{id}/promote", h.promoteThreadEmail)
	r.Post("/api/corrections/rule", h.addCorrectionRule)
	return r
}
