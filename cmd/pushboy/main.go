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
	// case "sqlite":
	// 	store, err = storage.NewSQLStore(cfg.DatabaseURL)
	// 	if err != nil {
	// 		log.Fatalf("Cannot create store: %v", err)
	// 	}
	case "postgres":
		store, err = storage.NewPostgresStore(cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("Cannot create store: %v", err)
		}
	default:
		log.Fatalf("Unsupported database driver: %s", cfg.DatabaseDriver)
		return
	}

	// Initialize dispatchers - only add successfully created clients
	dispatchers := make(map[string]dispatch.Dispatcher)

	// Initialize APNS Client
	if cfg.APNSKeyID != "" {
		p8Bytes, err := os.ReadFile("keys/AuthKey_" + cfg.APNSKeyID + ".p8")
		if err != nil {
			log.Printf("APNS disabled: cannot read key file: %v", err)
		} else {
			apnsClient := apns.NewClient(p8Bytes, cfg.APNSKeyID, cfg.APNSTeamID, cfg.APNSTopicID, false)
			dispatchers["apns"] = apnsClient
			log.Println("APNS dispatcher initialized")
		}
	} else {
		log.Println("APNS disabled: APNS_KEY_ID not configured")
	}

	// Initialize FCM Client
	serviceAccountBytes, err := os.ReadFile("keys/service-account.json")
	if err != nil {
		log.Printf("FCM disabled: cannot read service account: %v", err)
	} else {
		fcmClient, err := fcm.NewClient(context.Background(), serviceAccountBytes)
		if err != nil {
			log.Printf("FCM disabled: cannot create client: %v", err)
		} else {
			dispatchers["fcm"] = fcmClient
			log.Println("FCM dispatcher initialized")
		}
	}

	if len(dispatchers) == 0 {
		log.Println("WARNING: No push notification dispatchers configured")
	}

	//Initializing Pushboy Service
	pushboyService := service.NewPushBoyService(store, dispatchers)
	//Initializing Worker Pool
	workerPool := worker.NewPool(store, dispatchers, cfg.WorkerCount, cfg.SenderCount, cfg.JobQueueSize)
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
		log.Printf("HTTP server shutdown error: %v", err)
	}

	workerPool.Stop()

	if err := store.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	}

	log.Println("Server exiting")
}
