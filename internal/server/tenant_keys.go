package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type TenantStore interface {
	AuthenticateServerKey(context.Context, string) (storage.ServerKeyAccess, error)
	IssueServerKey(context.Context, storage.ServerKeyAccess, []string, *time.Time) (uuid.UUID, string, error)
	RevokeServerKey(context.Context, storage.ServerKeyAccess, uuid.UUID) error
	CreateTenantUser(context.Context, uuid.UUID, *storage.TenantUserIdentity, *uuid.UUID) (storage.TenantUser, error)
	GetTenantUser(context.Context, uuid.UUID, uuid.UUID) (storage.TenantUser, error)
	EnrollInstallation(context.Context, uuid.UUID, uuid.UUID, storage.InstallationObservations) (storage.TenantInstallation, bool, error)
	CreateTenantTopic(context.Context, uuid.UUID, string, string) (storage.TenantTopic, error)
	GetTenantTopic(context.Context, uuid.UUID, string) (storage.TenantTopic, error)
}

func (s *Server) authenticateServerKey(w http.ResponseWriter, r *http.Request, scope string) (storage.ServerKeyAccess, error) {
	w.Header().Set("Cache-Control", "no-store")
	headers := r.Header.Values("Authorization")
	var parts []string
	if len(headers) == 1 {
		parts = strings.Fields(headers[0])
	}
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return storage.ServerKeyAccess{}, storage.ErrInvalidServerKey
	}
	if s.tenants == nil {
		return storage.ServerKeyAccess{}, errors.New("tenant store unavailable")
	}
	actor, err := s.tenants.AuthenticateServerKey(r.Context(), parts[1])
	if err != nil {
		return storage.ServerKeyAccess{}, err
	}
	if !slices.Contains(actor.Scopes, scope) {
		return storage.ServerKeyAccess{}, storage.ErrInsufficientKeyScope
	}
	return actor, nil
}

func (s *Server) handleIssueServerKey(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "keys:manage")
	if err != nil {
		tenantError(w, err, true)
		return
	}
	var req struct {
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !decodeTenantRequest(http.MaxBytesReader(w, r.Body, maxTenantRequestBytes), &req) || req.Scopes == nil {
		tenantError(w, storage.ErrInvalidKeyRequest, true)
		return
	}
	id, key, err := s.tenants.IssueServerKey(r.Context(), actor, req.Scopes, req.ExpiresAt)
	if err != nil {
		tenantError(w, err, true)
		return
	}
	s.respond(w, r, struct {
		ID  uuid.UUID `json:"id"`
		Key string    `json:"key"`
	}{id, key}, http.StatusCreated)
}

func (s *Server) handleRevokeServerKey(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "keys:manage")
	if err != nil {
		tenantError(w, err, true)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "keyID"))
	if err != nil {
		tenantError(w, storage.Errors.NotFound, true)
		return
	}
	if err := s.tenants.RevokeServerKey(r.Context(), actor, id); err != nil {
		tenantError(w, err, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tenantError(w http.ResponseWriter, err error, keyManagement bool) {
	notFound, unavailable := "Resource not found", "Tenant resources unavailable"
	if keyManagement {
		notFound, unavailable = "Key not found", "Key management unavailable"
	}
	switch {
	case errors.Is(err, storage.ErrInvalidServerKey):
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	case errors.Is(err, storage.ErrInsufficientKeyScope):
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, storage.ErrInvalidKeyRequest):
		http.Error(w, "Invalid key request", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidTenantResource):
		http.Error(w, "Invalid resource request", http.StatusBadRequest)
	case errors.Is(err, storage.ErrTenantInstallationRetired):
		http.Error(w, "Installation retired", http.StatusGone)
	case errors.Is(err, storage.Errors.NotFound):
		http.Error(w, notFound, http.StatusNotFound)
	case errors.Is(err, storage.Errors.AlreadyExists):
		http.Error(w, "Resource already exists", http.StatusConflict)
	default:
		http.Error(w, unavailable, http.StatusInternalServerError)
	}
}
