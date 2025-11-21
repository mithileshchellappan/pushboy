package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	p8Bytes, err := os.ReadFile("keys/AuthKey_" + cfg.APNSKeyID + ".p8")
	if err != nil {
		//TODO: Change to Fatalf
		log.Printf("Cannot read APNS key: %v", err)
	}
	apnsClient := apns.NewClient(p8Bytes, cfg.APNSKeyID, cfg.APNSTeamID, cfg.APNSTopicID, false)

	//Initializing FCM Client
	serviceAccountBytes, err := os.ReadFile("keys/service-account.json")
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
	go pushboyService.StartAggregator(context.Background())
	//Initializing Worker Pool
	workerPool := worker.NewPool(pushboyService, cfg.WorkerCount, 100)
	workerPool.Start()

	//Initializing HTTP Server
	httpServer := server.New(pushboyService, workerPool)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		//Running HTTP server as go routine to avoid blocking the main thread
		if err := httpServer.Start(cfg.ServerPort); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Could not start server: %v", err)
		}
	}()
	<-ctx.Done()

	log.Println("Shutdown signal received, stopping app")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutting down")
	}

	workerPool.Stop()

	log.Println("Server exiting")
}
