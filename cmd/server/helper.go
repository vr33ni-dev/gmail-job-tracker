package main

import (
	"log"

	"github.com/vr33ni-dev/gmail-job-tracker/internal/auth"
)

func loadUserInfo() (email, name string) {
	if !auth.IsConnected() {
		return
	}
	token, err := auth.LoadToken()
	if err != nil {
		log.Printf("could not load token: %v", err)
		return
	}
	userInfo, err := auth.FetchUserInfo(token)
	if err != nil {
		log.Printf("could not fetch user info: %v", err)
		return
	}
	return userInfo.Email, userInfo.Name
}
