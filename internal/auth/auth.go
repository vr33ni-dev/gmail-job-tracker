package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
)

var tokenFile = "token.json"

func Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		RedirectURL:  "http://localhost:8080/auth/callback",
		Scopes:       []string{gmail.GmailReadonlyScope},
		Endpoint:     google.Endpoint,
	}
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	url := Config().AuthCodeURL("state", oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func CallbackHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	token, err := Config().Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, fmt.Sprintf("token exchange failed: %v", err), http.StatusInternalServerError)
		return
	}

	if err := saveToken(token); err != nil {
		http.Error(w, fmt.Sprintf("save token failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<html><body style="font-family:sans-serif;padding:40px;background:#080a10;color:#e2e8f0">
		<h2>✅ Gmail connected!</h2>
		<p>You can close this tab and go back to JobTracker.</p>
	</body></html>`)
}

func LoadToken() (*oauth2.Token, error) {
	f, err := os.Open(tokenFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var token oauth2.Token
	return &token, json.NewDecoder(f).Decode(&token)
}

func saveToken(token *oauth2.Token) error {
	f, err := os.Create(tokenFile)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

func IsConnected() bool {
	_, err := LoadToken()
	return err == nil
}
