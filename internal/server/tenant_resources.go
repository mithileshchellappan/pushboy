package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

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

type createTenantUserRequest struct {
	Identity map[string]string
}

func (req *createTenantUserRequest) UnmarshalJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return storage.ErrInvalidTenantResource
	}
	if !decoder.More() {
		return nil
	}
	token, err = decoder.Token()
	if err != nil || token != "identity" {
		return storage.ErrInvalidTenantResource
	}
	if err := decoder.Decode(&req.Identity); err != nil {
		return err
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return storage.ErrInvalidTenantResource
	}
	return nil
}

func (s *Server) handleCreateTenantUser(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "users:write")
	if err != nil {
		tenantError(w, err, false)
		return
	}

	var requestID *uuid.UUID
	if keys := r.Header.Values("Idempotency-Key"); len(keys) != 0 {
		if len(keys) != 1 || len(keys[0]) != 36 {
			tenantError(w, storage.ErrInvalidTenantResource, false)
			return
		}
		id, err := uuid.Parse(keys[0])
		if err != nil {
			tenantError(w, storage.ErrInvalidTenantResource, false)
			return
		}
		requestID = &id
	}

	var req createTenantUserRequest
	if !decodeTenantResourceRequest(w, r, &req) {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	var identity *storage.TenantUserIdentity
	if fields := req.Identity; fields != nil {
		namespace, externalID := fields["namespace"], fields["externalId"]
		if len(fields) != 2 || namespace == "" || externalID == "" {
			tenantError(w, storage.ErrInvalidTenantResource, false)
			return
		}
		identity = &storage.TenantUserIdentity{Namespace: namespace, ExternalID: externalID}
	}
	user, err := s.tenants.CreateTenantUser(r.Context(), actor.TenantID, identity, requestID)
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

	param, err := decodedPathParam(r, "uid")
	if err != nil || len(param) != 36 {
		tenantError(w, storage.Errors.NotFound, false)
		return
	}
	uid, err := uuid.Parse(param)
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

	var req map[string]string
	if !decodeTenantResourceRequest(w, r, &req) || len(req) != 2 || req["id"] == "" || req["name"] == "" {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	topic, err := s.tenants.CreateTenantTopic(r.Context(), actor.TenantID, req["id"], req["name"])
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
