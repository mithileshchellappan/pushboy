package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type CreateTopicRequest struct {
	Name string `json:"name"`
}

func main() {
	http.HandleFunc("/ping", handlePing)
	http.HandleFunc("/healthz", handleHealthz)
	http.HandleFunc("/topics", handleCreateTopic)

	addr := ":8080"

	log.Printf("Starting server on %s", addr)

	err := http.ListenAndServe(addr, nil)

	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "pong")
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status": "ok"}`)
}

func handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreateTopicRequest
	err := json.NewDecoder(r.Body).Decode(&req)

	if err != nil {
		http.Error(w, "Bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Created topic: %+v", req)

	w.WriteHeader(http.StatusCreated)
}
