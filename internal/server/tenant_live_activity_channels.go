package server

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func (s *Server) tenantLAChannelScope(w http.ResponseWriter, r *http.Request, scope string) (uuid.UUID, string, bool) {
	actor, err := s.authenticateServerKey(w, r, scope)
	if err != nil {
		tenantError(w, err, false)
		return uuid.Nil, "", false
	}
	if s.tenantChannels == nil {
		tenantLAChannelError(w, storage.ErrProviderRouteUnavailable)
		return uuid.Nil, "", false
	}
	activityID, err := decodedPathParam(r, "activityID")
	if err != nil || strings.TrimSpace(activityID) == "" || activityID == "." || activityID == ".." ||
		strings.ContainsRune(activityID, 0) || !utf8.ValidString(activityID) || utf8.RuneCountInString(activityID) > 128 {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return uuid.Nil, "", false
	}
	return actor.TenantID, activityID, true
}

func (s *Server) handleProvisionTenantLAChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, activityID, ok := s.tenantLAChannelScope(w, r, "send")
	if !ok {
		return
	}
	var request struct {
		TopicID          string    `json:"topicId"`
		ProviderConfigID uuid.UUID `json:"providerConfigId"`
		Public           bool      `json:"public"`
	}
	if !decodeTenantResourceRequest(w, r, &request) || request.TopicID == "" || request.ProviderConfigID == uuid.Nil {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	ch, created, err := s.tenantChannels.Provision(r.Context(), tenantID, storage.TenantLAChannel{
		ActivityID: activityID, TopicID: request.TopicID, ProviderConfigID: request.ProviderConfigID,
	}, request.Public)
	if err != nil {
		tenantLAChannelError(w, err)
		return
	}
	status := http.StatusOK
	if ch.State != storage.TenantLAChannelActive {
		status = http.StatusConflict
	} else if created {
		status = http.StatusCreated
	}
	s.respond(w, r, tenantLAChannelResponse(ch), status)
}

func (s *Server) handleGetTenantLAChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, activityID, ok := s.tenantLAChannelScope(w, r, "read")
	if !ok {
		return
	}
	channel, err := s.tenantChannels.Get(r.Context(), tenantID, activityID)
	if err != nil {
		tenantLAChannelError(w, err)
		return
	}
	s.respond(w, r, tenantLAChannelResponse(channel), http.StatusOK)
}

func (s *Server) handleDeleteTenantLAChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, activityID, ok := s.tenantLAChannelScope(w, r, "send")
	if !ok {
		return
	}
	if err := s.tenantChannels.Delete(r.Context(), tenantID, activityID); err != nil {
		tenantLAChannelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tenantLAChannelResponse(channel storage.TenantLAChannel) storage.TenantLAChannel {
	if channel.State == storage.TenantLAChannelDeleting {
		channel.ChannelID = ""
	}
	return channel
}

func tenantLAChannelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrProviderAuthorization):
		http.Error(w, "Provider authorization failed", http.StatusBadGateway)
	case errors.Is(err, storage.ErrProviderRouteUnavailable):
		http.Error(w, "APNs route unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, storage.Errors.Conflict):
		http.Error(w, "Live Activity channel scope changed or conflicts", http.StatusConflict)
	case errors.Is(err, service.ErrLAChannelProviderFailed):
		http.Error(w, "APNs channel operation failed", http.StatusBadGateway)
	default:
		tenantError(w, err, false)
	}
}
