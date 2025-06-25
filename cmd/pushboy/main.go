package main

import (
	"log"
	"net/http"

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

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Could not start server: %v", err)
	}
}
