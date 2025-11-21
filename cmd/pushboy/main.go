package main

import (
	"context"
	"log"
	"net/http"
	"os"

	_ "github.com/joho/godotenv/autoload"

	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/config"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/fcm"
	"github.com/mithileshchellappan/pushboy/internal/server"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/worker"
)

func main() {
	cfg := config.Load()

	var store storage.Store
	var err error

	//Initializing Store with Database Driver
	switch cfg.DatabaseDriver {
	case "sqlite":
		store, err = storage.NewSQLStore(cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("Cannot create store: %v", err)
		}
	default:
		log.Fatalf("Unsupported database driver: %s", cfg.DatabaseDriver)
		return
	}

	//Initializing APNS Client
	p8Bytes, err := os.ReadFile("config/AuthKey_" + cfg.APNSKeyID + ".p8")
	if err != nil {
		//TODO: Change to Fatalf
		log.Printf("Cannot read APNS key: %v", err)
	}
	apnsClient := apns.NewClient(p8Bytes, cfg.APNSKeyID, cfg.APNSTeamID, cfg.APNSTopicID, false)

	//Initializing FCM Client
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

	//Initializing Pushboy Service
	pushboyService := service.NewPushBoyService(store, dispatchers)

	//Initializing Worker Pool
	workerPool := worker.NewPool(pushboyService, cfg.WorkerCount, 100)
	workerPool.Start()

	//Initializing HTTP Server
	httpServer := server.New(pushboyService, workerPool)
	router := httpServer.Start()

	addr := cfg.ServerPort
	log.Printf("Starting server on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Could not start server: %v", err)
	}
}
