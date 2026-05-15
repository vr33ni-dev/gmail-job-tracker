package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/api"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"
	llmclient "github.com/vr33ni-dev/gmail-job-tracker/internal/llm"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/gmail"
	syncsvc "github.com/vr33ni-dev/gmail-job-tracker/internal/sync"
)

func main() {
	_ = godotenv.Load()
	ctx := context.Background()

	// DB
	store, err := db.New(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := goose.Up(store.DB(), "migrations"); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	// LLM
	llm := llmclient.NewClient(store)

	// declare svc before routes so callback can reference it
	var svc *syncsvc.Service

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)

	// Auth routes
	r.Get("/auth/login", auth.LoginHandler)
	r.Get("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		auth.CallbackHandler(w, r)
		if svc == nil {
			token, _ := auth.LoadToken()
			gmailClient, err := gmail.NewClient(ctx, token, auth.Config())
			if err == nil {
				svc = syncsvc.NewService(store, gmailClient, llm)
				go svc.RunLoop(ctx, 15*time.Minute)
				log.Println("gmail sync initialized after auth")
			}
		}
	})

	// Status endpoint
	r.Get("/auth/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if auth.IsConnected() {
			_, _ = w.Write([]byte(`{"connected":true}`))
		} else {
			_, _ = w.Write([]byte(`{"connected":false}`))
		}
	})

	// Initialize sync if already connected
	if auth.IsConnected() {
		token, _ := auth.LoadToken()
		gmailClient, err := gmail.NewClient(ctx, token, auth.Config())
		if err != nil {
			log.Printf("gmail init failed: %v", err)
		} else {
			svc = syncsvc.NewService(store, gmailClient, llm)
			go svc.RunLoop(ctx, 15*time.Minute)
			log.Println("gmail sync running every 15 minutes")
		}
	} else {
		log.Println("gmail not connected — opening browser to authenticate...")
		go func() {
			time.Sleep(500 * time.Millisecond)
			if err := exec.Command("open", "http://localhost:8080/auth/login").Start(); err != nil {
				log.Printf("open browser: %v", err)
			}
		}()
	}

	// Mount app API routes
	appHandler := api.NewHandler(store, svc)
	r.Mount("/", appHandler.Router())

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
