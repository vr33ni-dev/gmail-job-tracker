package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/api"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/gmail"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
	llmClient "github.com/vr33ni-dev/gmail-job-tracker/internal/llm"
	syncSvc "github.com/vr33ni-dev/gmail-job-tracker/internal/sync"
)

func main() {
	envFile := ".env"
	if os.Getenv("IS_DEMO") == "true" {
		envFile = ".env.demo"
	}
	_ = godotenv.Load(envFile)
	log.Printf("isDemo: %v", os.Getenv("IS_DEMO"))

	ctx := context.Background()
	store, err := db.New(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := goose.Up(store.DB(), "internal/db/migrations"); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	userEmail, userName := loadUserInfo()

	// init services
	llm := llmClient.NewClient(store)
	h := api.NewHandler(store, llm)

	// startSync — used both at startup and after auth callback
	startSync := func(email, name string) {
		token, _ := auth.LoadToken()
		gmailClient, err := gmail.NewClient(ctx, token, auth.Config())
		if err != nil {
			log.Printf("gmail init: %v", err)
			return
		}
		svc := syncSvc.NewService(store, gmailClient, llm, email, name)
		h.SetSync(svc)
		go svc.SyncLoop(ctx, 15*time.Minute)
		if t, err := store.LastPollTime(ctx); err == nil {
			log.Printf("gmail sync started (running every 15 min) - last sync on: %s", t.Format("2006-01-02 15:04"))
		} else {
			log.Println("gmail sync started (running every 15 min)")
		}
	}

	// onAuthDone hook — called by router after successful auth
	r := api.NewRouter(h, func() { startSync(userEmail, userName) })

	// start sync or open browser
	if auth.IsConnected() {
		startSync(userEmail, userName)
	} else {
		log.Println("gmail not connected — opening browser to authenticate...")
		go func() {
			time.Sleep(500 * time.Millisecond)
			exec.Command("open", "http://localhost:8080/auth/login").Start() // macOS only
		}()
	}

	// 9. start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
