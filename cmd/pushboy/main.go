package main

import (
	"log"

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

	if err := httpServer.Start(":8080"); err != nil {
		log.Fatalf("Could not start server: %v", err)
	}
}
