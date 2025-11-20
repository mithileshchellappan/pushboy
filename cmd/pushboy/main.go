package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/joho/godotenv/autoload"

	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/fcm"
	"github.com/mithileshchellappan/pushboy/internal/server"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func main() {
	store, err := storage.NewSQLStore("./pushboy.db")
	if err != nil {
		log.Fatalf("Cannot create store: %v", err)
	}

	apnsKeyID := os.Getenv("APNS_KEY_ID")
	apnsTeamID := os.Getenv("APNS_TEAM_ID")

	p8Bytes, err := os.ReadFile("config/AuthKey_" + apnsKeyID + ".p8")

	if err != nil {
		//TODO: Change to Fatalf
		log.Printf("Cannot read APNS key: %v", err)
	}

	apnsClient := apns.NewClient(p8Bytes, apnsKeyID, apnsTeamID, false)

	serviceAccountBytes, err := os.ReadFile("config/service-account.json")
	if err != nil {
		log.Printf("Cannot read service account: %v", err)
	}
	fcmClient, err := fcm.NewClient(context.Background(), serviceAccountBytes)
	if err != nil {
		log.Printf("Cannot create FCM client: %v", err)
	}

	dispatchers := map[string]dispatch.Dispatcher{
		"apns": apnsClient,
		"fcm":  fcmClient,
	}

	pushboyService := service.NewPushBoyService(store, dispatchers)

	httpServer := server.New(pushboyService)
	router := httpServer.Start()

	addr := ":8080"
	log.Printf("Starting server on %s", addr)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			<-ticker.C

			log.Println("WORKER: Checking for pending jobs...")

			ctx := context.Background()

			if err := pushboyService.ProcessPendingJobs(ctx); err != nil {
				log.Printf("WORKER: Error processing pending jobs: %v", err)
			}

		}
	}()
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Could not start server: %v", err)
	}
}
