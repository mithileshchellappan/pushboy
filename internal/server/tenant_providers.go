package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func (s *Server) authenticateTenantProvider(w http.ResponseWriter, r *http.Request, scope string) (uuid.UUID, bool) {
	actor, err := s.authenticateServerKey(w, r, scope)
	if err != nil {
		tenantError(w, err, false)
		return uuid.Nil, false
	}
	if s.providers == nil {
		http.Error(w, "Provider configuration unavailable", http.StatusServiceUnavailable)
		return uuid.Nil, false
	}
	return actor.TenantID, true
}

func (s *Server) handleCreateTenantProvider(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authenticateTenantProvider(w, r, "providers:manage")
	if !ok {
		return
	}

	var req struct {
		storage.TenantProviderNamespace
		Enabled     *bool           `json:"enabled"`
		Credentials json.RawMessage `json:"credentials"`
	}
	if !decodeTenantResourceRequest(w, r, &req) {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	route, err := s.providers.Create(r.Context(), tenantID, req.TenantProviderNamespace, enabled, req.Credentials)
	if err != nil {
		tenantProviderError(w, err)
		return
	}
	w.Header().Set("Location", "/v2/providers/"+route.ID.String())
	s.respond(w, r, route, http.StatusCreated)
}

func tenantProviderID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	value, err := decodedPathParam(r, "providerID")
	if err == nil && len(value) == 36 {
		id, err := uuid.Parse(value)
		if err == nil {
			return id, true
		}
	}
	tenantError(w, storage.Errors.NotFound, false)
	return uuid.Nil, false
}

func (s *Server) handleUpdateTenantProvider(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authenticateTenantProvider(w, r, "providers:manage")
	if !ok {
		return
	}
	id, ok := tenantProviderID(w, r)
	if !ok {
		return
	}

	var req struct {
		CredentialVersion int64           `json:"credentialVersion"`
		Enabled           *bool           `json:"enabled"`
		Credentials       json.RawMessage `json:"credentials"`
	}
	if !decodeTenantResourceRequest(w, r, &req) || req.CredentialVersion < 1 {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	route, err := s.providers.Update(r.Context(), tenantID, id, req.CredentialVersion, req.Enabled, req.Credentials)
	if err != nil {
		tenantProviderError(w, err)
		return
	}
	s.respond(w, r, route, http.StatusOK)
}

func (s *Server) handleGetTenantProvider(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authenticateTenantProvider(w, r, "read")
	if !ok {
		return
	}
	id, ok := tenantProviderID(w, r)
	if !ok {
		return
	}
	route, err := s.providers.Get(r.Context(), tenantID, id)
	if err != nil {
		tenantProviderError(w, err)
		return
	}
	s.respond(w, r, route, http.StatusOK)
}

func (s *Server) handleListTenantProviders(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authenticateTenantProvider(w, r, "read")
	if !ok {
		return
	}
	routes, err := s.providers.List(r.Context(), tenantID)
	if err != nil {
		tenantProviderError(w, err)
		return
	}
	s.respond(w, r, routes, http.StatusOK)
}

func tenantProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrProviderEncryptionUnavailable):
		http.Error(w, "Provider encryption key unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, storage.ErrProviderVersionConflict):
		http.Error(w, "Provider credential version changed", http.StatusConflict)
	default:
		tenantError(w, err, false)
	}
}
