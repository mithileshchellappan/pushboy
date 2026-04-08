package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type laRegistrationRequest struct {
	Platform         string `json:"platform"`
	StartToken       string `json:"start_token"`
	AutoStartEnabled bool   `json:"auto_start_enabled"`
	Enabled          bool   `json:"enabled"`
}

type laActivityRequest struct {
	Kind            string           `json:"kind"`
	ExternalRef     string           `json:"external_ref,omitempty"`
	StartAttributes map[string]any   `json:"start_attributes,omitempty"`
	State           map[string]any   `json:"state,omitempty"`
	Alert           *storage.LAAlert `json:"alert,omitempty"`
}

type laUpdateTokenRequest struct {
	Platform   string `json:"platform"`
	Token      string `json:"token"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

func (s *Server) mountLARoutes(r chi.Router) {
	r.Route("/users/{userID}/la", func(r chi.Router) {
		r.Post("/start", s.handleLAStartForUser)
		r.Post("/tokens", s.handleLAUpsertUserUpdateToken)
		r.Post("/registrations", s.handleLACreateRegistration)
		r.Put("/registrations/{laRegistrationID}", s.handleLAUpdateRegistration)
		r.Delete("/registrations/{laRegistrationID}", s.handleLADeleteRegistration)
		r.Post("/subscriptions/{topicID}", s.handleLASubscribeUserToTopic)
		r.Delete("/subscriptions/{topicID}", s.handleLAUnsubscribeUserFromTopic)
	})

	r.Route("/la", func(r chi.Router) {
		r.Post("/topics/{topicID}/start", s.handleLAStartForTopic)
		r.Post("/topics/{topicID}/tokens", s.handleLAUpsertTopicUpdateToken)
		r.Post("/{laID}/update", s.handleLAUpdate)
		r.Post("/{laID}/end", s.handleLAEnd)
	})
}

func (s *Server) handleLACreateRegistration(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	var req laRegistrationRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	registration, err := s.service.CreateLARegistration(r.Context(), userID, req.Platform, req.StartToken, req.AutoStartEnabled, req.Enabled)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "User not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.AlreadyExists):
			http.Error(w, "LA registration already exists", http.StatusConflict)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA registration create not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error creating LA registration", http.StatusInternalServerError)
			log.Printf("Error creating LA registration: %v", err)
		}
		return
	}

	s.respond(w, r, registration, http.StatusCreated)
}

func (s *Server) handleLAUpdateRegistration(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	laRegistrationID := chi.URLParam(r, "laRegistrationID")
	if userID == "" || laRegistrationID == "" {
		http.Error(w, "userID and laRegistrationID are required", http.StatusBadRequest)
		return
	}

	var req laRegistrationRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	registration, err := s.service.UpdateLARegistration(r.Context(), userID, laRegistrationID, req.Platform, req.StartToken, req.AutoStartEnabled, req.Enabled)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "LA registration not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA registration update not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error updating LA registration", http.StatusInternalServerError)
			log.Printf("Error updating LA registration: %v", err)
		}
		return
	}

	s.respond(w, r, registration, http.StatusOK)
}

func (s *Server) handleLADeleteRegistration(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	laRegistrationID := chi.URLParam(r, "laRegistrationID")
	if userID == "" || laRegistrationID == "" {
		http.Error(w, "userID and laRegistrationID are required", http.StatusBadRequest)
		return
	}

	err := s.service.DeleteLARegistration(r.Context(), userID, laRegistrationID)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "LA registration not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA registration delete not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error deleting LA registration", http.StatusInternalServerError)
			log.Printf("Error deleting LA registration: %v", err)
		}
		return
	}

	s.respond(w, r, nil, http.StatusNoContent)
}

func (s *Server) handleLASubscribeUserToTopic(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	topicID := chi.URLParam(r, "topicID")
	if userID == "" || topicID == "" {
		http.Error(w, "userID and topicID are required", http.StatusBadRequest)
		return
	}

	preference, err := s.service.SubscribeUserToLATopic(r.Context(), userID, topicID)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "User or topic not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA topic subscribe not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error subscribing user to LA topic", http.StatusInternalServerError)
			log.Printf("Error subscribing user to LA topic: %v", err)
		}
		return
	}

	s.respond(w, r, preference, http.StatusCreated)
}

func (s *Server) handleLAUnsubscribeUserFromTopic(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	topicID := chi.URLParam(r, "topicID")
	if userID == "" || topicID == "" {
		http.Error(w, "userID and topicID are required", http.StatusBadRequest)
		return
	}

	err := s.service.UnsubscribeUserFromLATopic(r.Context(), userID, topicID)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "LA subscription not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA topic unsubscribe not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error unsubscribing user from LA topic", http.StatusInternalServerError)
			log.Printf("Error unsubscribing user from LA topic: %v", err)
		}
		return
	}

	s.respond(w, r, nil, http.StatusNoContent)
}

func (s *Server) handleLAStartForUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	var req laActivityRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	activity, err := s.service.StartLAForUser(r.Context(), userID, req.Kind, req.ExternalRef, req.StartAttributes, req.State, req.Alert)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "User not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.AlreadyExists):
			http.Error(w, "Active LA already exists", http.StatusConflict)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA user start not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error starting LA for user", http.StatusInternalServerError)
			log.Printf("Error starting LA for user: %v", err)
		}
		return
	}

	s.respond(w, r, activity, http.StatusCreated)
}

func (s *Server) handleLAUpsertUserUpdateToken(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	var req laUpdateTokenRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	updateToken, err := s.service.UpsertLAUpdateTokenForUser(r.Context(), userID, req.Platform, req.Token, req.TTLSeconds)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "User not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA update token upsert not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error upserting LA update token", http.StatusInternalServerError)
			log.Printf("Error upserting LA user update token: %v", err)
		}
		return
	}

	s.respond(w, r, updateToken, http.StatusCreated)
}

func (s *Server) handleLAStartForTopic(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	var req laActivityRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	activity, err := s.service.StartLAForTopic(r.Context(), topicID, req.Kind, req.ExternalRef, req.StartAttributes, req.State, req.Alert)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Topic not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.AlreadyExists):
			http.Error(w, "Active LA already exists for topic", http.StatusConflict)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA topic start not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error starting LA for topic", http.StatusInternalServerError)
			log.Printf("Error starting LA for topic: %v", err)
		}
		return
	}

	s.respond(w, r, activity, http.StatusCreated)
}

func (s *Server) handleLAUpsertTopicUpdateToken(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	var req laUpdateTokenRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	updateToken, err := s.service.UpsertLAUpdateTokenForTopic(r.Context(), topicID, req.Platform, req.Token, req.TTLSeconds)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Topic not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA update token upsert not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error upserting LA update token", http.StatusInternalServerError)
			log.Printf("Error upserting LA topic update token: %v", err)
		}
		return
	}

	s.respond(w, r, updateToken, http.StatusCreated)
}

func (s *Server) handleLAUpdate(w http.ResponseWriter, r *http.Request) {
	laID := chi.URLParam(r, "laID")
	if laID == "" {
		http.Error(w, "laID is required", http.StatusBadRequest)
		return
	}

	var req laActivityRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	activity, err := s.service.UpdateLA(r.Context(), laID, req.State, req.Alert)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "LA activity not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA update not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error updating LA", http.StatusInternalServerError)
			log.Printf("Error updating LA: %v", err)
		}
		return
	}

	s.respond(w, r, activity, http.StatusOK)
}

func (s *Server) handleLAEnd(w http.ResponseWriter, r *http.Request) {
	laID := chi.URLParam(r, "laID")
	if laID == "" {
		http.Error(w, "laID is required", http.StatusBadRequest)
		return
	}

	var req laActivityRequest
	if !s.decodeJSON(w, r.Body, &req) {
		return
	}

	activity, err := s.service.EndLA(r.Context(), laID, req.State, req.Alert)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "LA activity not found", http.StatusNotFound)
		case errors.Is(err, storage.Errors.NotImplemented):
			http.Error(w, "LA end not implemented", http.StatusNotImplemented)
		default:
			http.Error(w, "Error ending LA", http.StatusInternalServerError)
			log.Printf("Error ending LA: %v", err)
		}
		return
	}

	s.respond(w, r, activity, http.StatusOK)
}
