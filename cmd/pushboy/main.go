package main

import (
	"context"
	"log"
	"net/http"
	"os"

	_ "github.com/joho/godotenv/autoload"

	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/fcm"
	"github.com/mithileshchellappan/pushboy/internal/server"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/worker"
)

func main() {
	store, err := storage.NewSQLStore("./pushboy.db")
	if err != nil {
		log.Fatalf("Cannot create store: %v", err)
	}

	apnsKeyID := os.Getenv("APNS_KEY_ID")
	apnsTeamID := os.Getenv("APNS_TEAM_ID")
	apnsTopicID := os.Getenv("APNS_TOPIC_ID")

	p8Bytes, err := os.ReadFile("config/AuthKey_" + apnsKeyID + ".p8")

	if err != nil {
		//TODO: Change to Fatalf
		log.Printf("Cannot read APNS key: %v", err)
	}

	apnsClient := apns.NewClient(p8Bytes, apnsKeyID, apnsTeamID, apnsTopicID, false)

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

	workerPool := worker.NewPool(pushboyService, 5, 100)
	workerPool.Start()

	httpServer := server.New(pushboyService, workerPool)
	router := httpServer.Start()

	addr := ":8080"
	log.Printf("Starting server on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Could not start server: %v", err)
	}
}
