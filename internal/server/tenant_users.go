package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authorizeTenantUser(w, r, "users:write")
	if !ok {
		return
	}

	type createUserRequest struct {
		Identity  *storage.TenantUserIdentity `json:"identity"`
		RequestID *string                     `json:"requestId"`
	}
	req := &createUserRequest{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		err = decoder.Decode(new(json.RawMessage))
	}
	if req == nil || !errors.Is(err, io.EOF) {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var requestID *uuid.UUID
	if req.RequestID != nil {
		id, err := uuid.Parse(*req.RequestID)
		if err != nil || id == uuid.Nil || id.String() != *req.RequestID {
			http.Error(w, "Invalid requestId", http.StatusBadRequest)
			return
		}
		requestID = &id
	}

	user, err := s.tenantUsers.CreateTenantUser(r.Context(), tenantID, req.Identity, requestID)
	switch {
	case errors.Is(err, storage.ErrInvalidTenantResource):
		http.Error(w, "Invalid user identity", http.StatusBadRequest)
	case errors.Is(err, storage.Errors.AlreadyExists):
		http.Error(w, "User already exists", http.StatusConflict)
	case err != nil:
		http.Error(w, "Error creating user", http.StatusInternalServerError)
		log.Printf("Error creating tenant user: %v", err)
	default:
		s.respond(w, r, user, http.StatusCreated)
	}
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authorizeTenantUser(w, r, "read")
	if !ok {
		return
	}

	route := "/v1/users?tenant=" + tenantID.String()
	query, limit, err := pageQueryFromRequest(r, route)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if query.Cursor.ID != "" {
		if uid, err := uuid.Parse(query.Cursor.ID); err != nil || uid.String() != query.Cursor.ID || uid == uuid.Nil {
			http.Error(w, "Invalid cursor", http.StatusBadRequest)
			return
		}
	}

	users, err := s.tenantUsers.ListTenantUsers(r.Context(), tenantID, query)
	if err != nil {
		http.Error(w, "Error listing users", http.StatusInternalServerError)
		log.Printf("Error listing tenant users: %v", err)
		return
	}

	response := makeMappedListResponse(users, limit, route, "", func(user storage.TenantUserListItem) (time.Time, string) {
		return user.CreatedAt, user.UID.String()
	}, func(user storage.TenantUserListItem) storage.TenantUser {
		return user.TenantUser
	})
	s.respond(w, r, response, http.StatusOK)
}

func tenantUserUID(r *http.Request) (uuid.UUID, bool) {
	rawUID := chi.URLParam(r, "userID")
	uid, err := uuid.Parse(rawUID)
	return uid, err == nil && len(rawUID) == 36 && uid != uuid.Nil
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authorizeTenantUser(w, r, "read")
	if !ok {
		return
	}
	uid, ok := tenantUserUID(r)
	if !ok {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	user, err := s.tenantUsers.GetTenantUser(r.Context(), tenantID, uid)
	switch {
	case errors.Is(err, storage.Errors.NotFound):
		http.Error(w, "User not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, "Error getting user", http.StatusInternalServerError)
		log.Printf("Error getting tenant user: %v", err)
	default:
		s.respond(w, r, user, http.StatusOK)
	}
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authorizeTenantUser(w, r, "users:write")
	if !ok {
		return
	}
	uid, ok := tenantUserUID(r)
	if !ok {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	err := s.tenantUsers.DeleteTenantUser(r.Context(), tenantID, uid)
	switch {
	case errors.Is(err, storage.Errors.NotFound):
		http.Error(w, "User not found", http.StatusNotFound)
	case errors.Is(err, storage.Errors.Conflict):
		http.Error(w, "User is still referenced by an installation or notification job", http.StatusConflict)
	case err != nil:
		http.Error(w, "Error deleting user", http.StatusInternalServerError)
		log.Printf("Error deleting tenant user: %v", err)
	default:
		s.respond(w, r, nil, http.StatusNoContent)
	}
}
