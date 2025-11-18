package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/server"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func main() {
	store, err := storage.NewSQLStore("./pushboy.db")
	if err != nil {
		log.Fatalf("Cannot create store: %v", err)
	}

	pushboyService := service.NewPushBoyService(store)

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
