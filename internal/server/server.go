package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/mithileshchellappan/pushboy/internal/service"
)

type Server struct {
	service *service.PushboyService
}

func New(s *service.PushboyService) *Server {
	return &Server{service: s}
}

func (s *Server) Start(addr string) error {
	log.Printf("Starting server on %s", addr)

	mux := http.NewServeMux()

	mux.HandleFunc("/topics", s.handleCreateTopic)
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})

	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	topic, err := s.service.CreateTopic(r.Context(), req.Name)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("Error creating topic %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(topic)

}
