package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type provisionLAChannelRequest struct {
	TopicID string `json:"topicId"`
}

func laChannelActivityID(r *http.Request) (string, error) {
	escapedPath := r.URL.EscapedPath()
	lastSlash := strings.LastIndexByte(escapedPath, '/')
	return url.PathUnescape(escapedPath[lastSlash+1:])
}

func toLAChannelResponse(channel *storage.LiveActivityChannel) map[string]any {
	return map[string]any{
		"activityId": channel.ActivityID,
		"topicId":    channel.TopicID,
		"channelId":  channel.ChannelID,
		"createdAt":  formatAPITime(channel.CreatedAt),
	}
}

func (s *Server) handleProvisionLAChannel(w http.ResponseWriter, r *http.Request) {
	activityID, err := laChannelActivityID(r)
	if err != nil {
		http.Error(w, "Invalid activityId", http.StatusBadRequest)
		return
	}

	var request provisionLAChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if request.TopicID == "" {
		http.Error(w, "topicId is required", http.StatusBadRequest)
		return
	}

	channel, created, err := s.service.ProvisionLAChannel(
		r.Context(),
		activityID,
		request.TopicID,
	)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrLAChannelUnavailable):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case errors.Is(err, service.ErrLAChannelConflict):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Topic not found", http.StatusNotFound)
		case errors.Is(err, service.ErrLAChannelProviderFailed):
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		log.Printf("Error provisioning live activity channel: %v", err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.respond(w, r, toLAChannelResponse(channel), status)
}

func (s *Server) handleGetLAChannel(w http.ResponseWriter, r *http.Request) {
	activityID, err := laChannelActivityID(r)
	if err != nil {
		http.Error(w, "Invalid activityId", http.StatusBadRequest)
		return
	}

	channel, err := s.service.GetLAChannel(r.Context(), activityID)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Live activity channel not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.respond(w, r, toLAChannelResponse(channel), http.StatusOK)
}

func (s *Server) handleDeleteLAChannel(w http.ResponseWriter, r *http.Request) {
	activityID, err := laChannelActivityID(r)
	if err != nil {
		http.Error(w, "Invalid activityId", http.StatusBadRequest)
		return
	}

	if err := s.service.DeleteLAChannel(r.Context(), activityID); err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Live activity channel not found", http.StatusNotFound)
		case errors.Is(err, service.ErrLAChannelUnavailable):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case errors.Is(err, service.ErrLAChannelProviderFailed):
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
