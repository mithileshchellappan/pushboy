package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/joho/godotenv/autoload"

	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/config"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/fcm"
	"github.com/mithileshchellappan/pushboy/internal/scheduler"
	"github.com/mithileshchellappan/pushboy/internal/server"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/worker"
)

func main() {
	cfg := config.Load()

	var store storage.Store
	var err error

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
		apnsKeyPath := cfg.APNSKeyPath
		if apnsKeyPath == "" {
			apnsKeyPath = "keys/AuthKey_" + cfg.APNSKeyID + ".p8"
		}
		p8Bytes, err := os.ReadFile(apnsKeyPath)
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
	serviceAccountBytes, err := os.ReadFile(cfg.FCMKeyPath)
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

	// Ensure broadcast topic exists
	var broadcastTopicID string
	if cfg.BroadcastTopicName != "" {
		broadcastTopicID = strings.ToLower(cfg.BroadcastTopicName)
		topic := &storage.Topic{Name: cfg.BroadcastTopicName}
		if err := store.CreateTopic(context.Background(), topic); err != nil {
			if errors.Is(err, storage.Errors.AlreadyExists) {
				log.Printf("Broadcast topic '%s' already exists (id: %s)", cfg.BroadcastTopicName, broadcastTopicID)
			} else {
				log.Fatalf("Failed to create broadcast topic: %v", err)
			}
		} else {
			log.Printf("Created broadcast topic '%s' (id: %s)", cfg.BroadcastTopicName, broadcastTopicID)
		}
	}

	pushboyService := service.NewPushBoyService(store, dispatchers, broadcastTopicID)

	workerPool := worker.NewPool(store, dispatchers, cfg.WorkerCount, cfg.SenderCount, cfg.JobQueueSize, cfg.BatchSize)
	workerPool.Start()

	scheduler := scheduler.New(store, workerPool, 10)
	scheduler.Start()

	httpServer := server.New(pushboyService, workerPool)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
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

	scheduler.Stop()
	workerPool.Stop()

	if err := store.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	}

	log.Println("Server exiting")
}
