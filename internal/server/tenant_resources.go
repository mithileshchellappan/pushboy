package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

const maxTenantRequestBytes = 16 << 10

func decodeTenantRequest(body io.Reader, req any) bool {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(req) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func decodeTenantResourceRequest(w http.ResponseWriter, r *http.Request, req any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTenantRequestBytes))
	return err == nil && validJSONUnicode(body) && decodeTenantRequest(bytes.NewReader(body), req)
}

func validJSONUnicode(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for i := bytes.IndexByte(body, '\\'); i >= 0; i = bytes.IndexByte(body, '\\') {
		body = body[i+1:]
		if len(body) == 0 {
			return false
		}
		if body[0] != 'u' {
			body = body[1:]
			continue
		}
		if len(body) < 5 {
			return false
		}
		unit, err := strconv.ParseUint(string(body[1:5]), 16, 16)
		if err != nil {
			return false
		}
		body = body[5:]
		if unit < 0xd800 || unit > 0xdfff {
			continue
		}
		if unit >= 0xdc00 || !bytes.HasPrefix(body, []byte(`\u`)) || len(body) < 6 {
			return false
		}
		low, err := strconv.ParseUint(string(body[2:6]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		body = body[6:]
	}
	return true
}

func (s *Server) handleCreateTenantUser(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "users:write")
	if err != nil {
		tenantError(w, err, false)
		return
	}

	var req *struct {
		Identity *storage.TenantUserIdentity `json:"identity"`
	}
	if !decodeTenantResourceRequest(w, r, &req) || req == nil {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	user, err := s.tenants.CreateTenantUser(r.Context(), actor.TenantID, req.Identity)
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, user, http.StatusCreated)
}

func (s *Server) handleGetTenantUser(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "read")
	if err != nil {
		tenantError(w, err, false)
		return
	}

	uid, err := uuid.Parse(chi.URLParam(r, "uid"))
	if err != nil {
		tenantError(w, storage.Errors.NotFound, false)
		return
	}
	user, err := s.tenants.GetTenantUser(r.Context(), actor.TenantID, uid)
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, user, http.StatusOK)
}

func (s *Server) handleCreateTenantTopic(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "topics:write")
	if err != nil {
		tenantError(w, err, false)
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodeTenantResourceRequest(w, r, &req) {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	topic, err := s.tenants.CreateTenantTopic(r.Context(), actor.TenantID, req.ID, req.Name)
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, topic, http.StatusCreated)
}

func (s *Server) handleGetTenantTopic(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "read")
	if err != nil {
		tenantError(w, err, false)
		return
	}

	id, err := decodedPathParam(r, "topicID")
	if err != nil || !utf8.ValidString(id) || strings.ContainsRune(id, 0) {
		tenantError(w, storage.Errors.NotFound, false)
		return
	}
	topic, err := s.tenants.GetTenantTopic(r.Context(), actor.TenantID, id)
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, topic, http.StatusOK)
}
