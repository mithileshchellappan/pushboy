package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type ServerKeyStore interface {
	AuthenticateServerKey(context.Context, string) (storage.ServerKeyAccess, error)
	IssueServerKey(context.Context, storage.ServerKeyAccess, []string, *time.Time) (uuid.UUID, string, error)
	RevokeServerKey(context.Context, storage.ServerKeyAccess, uuid.UUID) error
}

func (s *Server) authenticateKeyManager(w http.ResponseWriter, r *http.Request) (storage.ServerKeyAccess, bool) {
	w.Header().Set("Cache-Control", "no-store")
	headers := r.Header.Values("Authorization")
	var parts []string
	if len(headers) == 1 {
		parts = strings.Fields(headers[0])
	}
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		keyManagementError(w, storage.ErrInvalidServerKey)
		return storage.ServerKeyAccess{}, false
	}
	if s.serverKeys == nil {
		keyManagementError(w, errors.New("key store unavailable"))
		return storage.ServerKeyAccess{}, false
	}
	actor, err := s.serverKeys.AuthenticateServerKey(r.Context(), parts[1])
	if err != nil {
		keyManagementError(w, err)
		return storage.ServerKeyAccess{}, false
	}
	if !slices.Contains(actor.Scopes, "keys:manage") {
		keyManagementError(w, storage.ErrInsufficientKeyScope)
		return storage.ServerKeyAccess{}, false
	}
	return actor, true
}

func (s *Server) handleIssueServerKey(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.authenticateKeyManager(w, r)
	if !ok {
		return
	}
	var req struct {
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Scopes == nil || decoder.Decode(&struct{}{}) != io.EOF {
		keyManagementError(w, storage.ErrInvalidKeyRequest)
		return
	}
	id, key, err := s.serverKeys.IssueServerKey(r.Context(), actor, req.Scopes, req.ExpiresAt)
	if err != nil {
		keyManagementError(w, err)
		return
	}
	s.respond(w, r, struct {
		ID  uuid.UUID `json:"id"`
		Key string    `json:"key"`
	}{id, key}, http.StatusCreated)
}

func (s *Server) handleRevokeServerKey(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.authenticateKeyManager(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "keyID"))
	if err != nil {
		keyManagementError(w, storage.Errors.NotFound)
		return
	}
	if err := s.serverKeys.RevokeServerKey(r.Context(), actor, id); err != nil {
		keyManagementError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func keyManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidServerKey):
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	case errors.Is(err, storage.ErrInsufficientKeyScope):
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, storage.ErrInvalidKeyRequest):
		http.Error(w, "Invalid key request", http.StatusBadRequest)
	case errors.Is(err, storage.Errors.NotFound):
		http.Error(w, "Key not found", http.StatusNotFound)
	default:
		http.Error(w, "Key management unavailable", http.StatusInternalServerError)
	}
}
