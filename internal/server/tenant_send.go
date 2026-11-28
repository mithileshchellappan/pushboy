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
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type tenantUserStore interface {
	AuthenticateServerKey(context.Context, string) (storage.ServerKeyAccess, error)
	CreateTenantUser(context.Context, uuid.UUID, *storage.TenantUserIdentity, *uuid.UUID) (storage.TenantUser, error)
	ListTenantUsers(context.Context, uuid.UUID, storage.PageQuery) ([]storage.TenantUserListItem, error)
	GetTenantUser(context.Context, uuid.UUID, uuid.UUID) (storage.TenantUser, error)
	DeleteTenantUser(context.Context, uuid.UUID, uuid.UUID) error
	CreateTenantUserJob(context.Context, uuid.UUID, uuid.UUID, *model.NotificationPayload, *time.Time) (*storage.PublishJob, error)
	GetTenantUserJob(context.Context, uuid.UUID, string) (*storage.PublishJob, error)
	ListTenantUserJobReceipts(context.Context, uuid.UUID, string, string, int) ([]storage.TenantJobReceipt, error)
	FailTenantJobDispatch(context.Context, uuid.UUID, string) error
}

func (s *Server) authorizeTenantUser(w http.ResponseWriter, r *http.Request, scope string) (uuid.UUID, bool) {
	w.Header().Set("Cache-Control", "no-store")
	headers := r.Header.Values("Authorization")
	if len(headers) == 1 {
		fields := strings.Fields(headers[0])
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			if s.tenantUsers == nil {
				http.Error(w, "Key verification unavailable", http.StatusServiceUnavailable)
				return uuid.Nil, false
			}
			actor, err := s.tenantUsers.AuthenticateServerKey(r.Context(), fields[1])
			if err == nil {
				if !slices.Contains(actor.Scopes, scope) {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return uuid.Nil, false
				}
				return actor.TenantID, true
			}
			if !errors.Is(err, storage.ErrInvalidServerKey) {
				http.Error(w, "Key verification unavailable", http.StatusServiceUnavailable)
				return uuid.Nil, false
			}
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return uuid.Nil, false
}

func (s *Server) handleUserJobReceipts(w http.ResponseWriter, r *http.Request) {
	tid, ok := s.authorizeTenantUser(w, r, "read")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		http.Error(w, "Invalid limit", http.StatusBadRequest)
		return
	}
	cursor := q.Get("cursor")
	if cursor != "" {
		if id, e := uuid.Parse(cursor); e != nil || id.String() != cursor {
			http.Error(w, "Invalid cursor", http.StatusBadRequest)
			return
		}
	}
	items, err := s.tenantUsers.ListTenantUserJobReceipts(r.Context(), tid, chi.URLParam(r, "jobID"), cursor, limit+1)
	if errors.Is(err, storage.Errors.NotFound) {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Error reading receipts", http.StatusInternalServerError)
		return
	}
	result := listResponse[storage.TenantJobReceipt]{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		result.HasMore = true
		result.NextCursor = result.Items[limit-1].ID
	}
	s.respond(w, r, result, http.StatusOK)
}
