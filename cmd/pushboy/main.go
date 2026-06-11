package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/joho/godotenv/autoload"

	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/config"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/fcm"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/scheduler"
	"github.com/mithileshchellappan/pushboy/internal/server"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/workers"
)

func main() {
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workerCtx, workerStop := context.WithCancel(context.Background())
	defer workerStop()

	store, err := storage.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Cannot create store: %v", err)
	}

	// Initialize dispatchers - only add successfully created clients
	dispatchers := make(map[model.Platform]dispatch.Dispatcher)

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
			apnsClient := apns.NewClient(p8Bytes, cfg.APNSKeyID, cfg.APNSTeamID, cfg.APNSBundleID, cfg.APNSUseSandbox)
			dispatchers[model.APNS] = apnsClient
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
		fcmClient, err := fcm.NewClient(ctx, serviceAccountBytes)
		if err != nil {
			log.Printf("FCM disabled: cannot create client: %v", err)
		} else {
			dispatchers[model.FCM] = fcmClient
			log.Println("FCM dispatcher initialized")
		}
	}

	if len(dispatchers) == 0 {
		log.Println("WARNING: No push notification dispatchers configured")
	}

	// Ensure broadcast topic exists
	var broadcastTopicID string
	broadcastTopicName := strings.TrimSpace(cfg.BroadcastTopicName)
	if broadcastTopicName != "" {
		broadcastTopicID = strings.ToLower(broadcastTopicName)

		if _, err := store.GetTopicByID(ctx, broadcastTopicID); err != nil {
			if !errors.Is(err, storage.Errors.NotFound) {
				log.Fatalf("Failed to check broadcast topic: %v", err)
			}

			topic := &storage.Topic{
				ID:   broadcastTopicID,
				Name: broadcastTopicName,
			}
			if err := store.CreateTopic(ctx, topic); err != nil {
				if errors.Is(err, storage.Errors.AlreadyExists) {
					log.Fatalf("Broadcast topic '%s' exists with a conflicting id. Clean the bad row from topics and restart.", broadcastTopicName)
				}
				log.Fatalf("Failed to create broadcast topic: %v", err)
			}

			log.Printf("Created broadcast topic '%s' (id: %s)", broadcastTopicName, broadcastTopicID)
		} else {
			log.Printf("Broadcast topic '%s' already exists (id: %s)", broadcastTopicName, broadcastTopicID)
		}
	}

	pushboyService := service.NewPushBoyService(store, broadcastTopicID)

	jobPipeline := pipeline.NewMemoryPipeline[model.JobItem](cfg.JobQueueSize)
	taskPipeline := pipeline.NewMemoryPipeline[model.SendTask](cfg.JobQueueSize)
	dlqPipeline := pipeline.NewMemoryPipeline[model.SendOutcome](cfg.JobQueueSize) //TODO: change this to a different queue size for dlq

	laJobPipeline := pipeline.NewMemoryPipeline[model.JobItem](cfg.LAQueueSize)
	laTaskPipeline := pipeline.NewMemoryPipeline[model.SendTask](cfg.LAQueueSize)
	laDlqPipeline := pipeline.NewMemoryPipeline[model.SendOutcome](cfg.LAQueueSize) //TODO: change this to a different queue size for dlq

	scheduler := scheduler.New(store, jobPipeline, 10)
	scheduler.Start(workerCtx)

	var masterWg sync.WaitGroup
	var masters = make(map[int]workers.MasterWorker)

	var senderWg sync.WaitGroup
	var senders = make(map[int]workers.SenderWorker)

	var laMasterWg sync.WaitGroup
	var laMasters = make(map[int]workers.MasterWorker)

	var laSendersWg sync.WaitGroup
	var laSenders = make(map[int]workers.SenderWorker)

	for i := range cfg.WorkerCount {
		masterWg.Add(1)
		master := workers.NewMaster(store, jobPipeline, taskPipeline, cfg.BatchSize)
		masters[i] = master
		go func(m workers.MasterWorker) {
			defer masterWg.Done()
			m.Start(workerCtx)
		}(master)

	}

	for i := range cfg.LAWorkerCount {
		laMasterWg.Add(1)
		master := workers.NewMaster(store, laJobPipeline, laTaskPipeline, cfg.BatchSize)
		laMasters[i] = master
		go func(m workers.MasterWorker) {
			defer laMasterWg.Done()
			m.Start(workerCtx)
		}(master)
	}

	for i := range cfg.SenderCount {
		senderWg.Add(1)
		sender := workers.NewSender(store, taskPipeline, dlqPipeline, dispatchers, cfg.BatchSize)
		senders[i] = sender
		go func(m workers.SenderWorker) {
			defer senderWg.Done()
			m.Start(workerCtx)
		}(sender)
	}

	for i := range cfg.LASenderCount {
		laSendersWg.Add(1)
		sender := workers.NewSender(store, laTaskPipeline, laDlqPipeline, dispatchers, cfg.BatchSize)
		laSenders[i] = sender
		go func(m workers.SenderWorker) {
			defer laSendersWg.Done()
			m.Start(workerCtx)
		}(sender)
	}

	outcomeWorker := workers.NewOutcomeWorker(store, dlqPipeline, 1000, 10)
	var outcomeWg sync.WaitGroup
	//If required more DLQ outcome workers change it into a looped go routines
	outcomeWg.Add(1)
	go func(o workers.OutcomeWorker) {
		defer outcomeWg.Done()
		o.Start(workerCtx)
	}(outcomeWorker)

	laOutcomeWorker := workers.NewOutcomeWorker(store, laDlqPipeline, 1000, 10)
	var laOutcomeWg sync.WaitGroup

	laOutcomeWg.Add(1)
	go func(o workers.OutcomeWorker) {
		defer laOutcomeWg.Done()
		o.Start(workerCtx)
	}(laOutcomeWorker)

	httpServer := server.New(pushboyService, jobPipeline, laJobPipeline)

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
	gracefulDone := make(chan struct{})

	var drainWg sync.WaitGroup
	drainWg.Add(2)

	go func() {
		defer drainWg.Done()
		drainChain(shutdownCtx, []drainStage{
			{name: "job pipeline", pipe: jobPipeline, workers: &masterWg},
			{name: "task pipeline", pipe: taskPipeline, workers: &senderWg},
			{name: "DLQ pipeline", pipe: dlqPipeline, workers: &outcomeWg},
		})
	}()

	go func() {
		defer drainWg.Done()
		drainChain(shutdownCtx, []drainStage{
			{name: "LA job pipeline", pipe: laJobPipeline, workers: &laMasterWg},
			{name: "LA task pipeline", pipe: laTaskPipeline, workers: &laSendersWg},
			{name: "LA DLQ pipeline", pipe: laDlqPipeline, workers: &laOutcomeWg},
		})
	}()

	go func() {
		drainWg.Wait()
		if err := store.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		}
		close(gracefulDone)
	}()

	select {
	case <-gracefulDone:
		log.Printf("Gracefully Shutdown all workers.")
	case <-shutdownCtx.Done():
		log.Printf("Timed out waiting for outcome worker shutdown: %v", shutdownCtx.Err())
		workerStop()
	}

	log.Println("Pushboy exiting. See ya!")
}
