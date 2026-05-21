package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/api"
	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/db"
)

func main() {
	envFile := ".env"
	if os.Getenv("IS_DEMO") == "true" {
		envFile = ".env.demo"
	}
	_ = godotenv.Load(envFile)

	ctx := context.Background()

	log.Printf("isDemo: %v", os.Getenv("IS_DEMO"))
	store, err := db.New(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := goose.Up(store.DB(), "internal/db/migrations"); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	var userEmail, userName string
	if auth.IsConnected() {
		if token, err := auth.LoadToken(); err == nil {
			if userInfo, err := auth.FetchUserInfo(token); err == nil {
				userEmail = userInfo.Email
				userName = userInfo.Name
			} else {
				log.Printf("could not fetch user info: %v", err)
			}
		} else {
			log.Printf("could not load token: %v", err)
		}
	}

	r := api.NewRouter(ctx, store, auth.Config(), userEmail, userName)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
