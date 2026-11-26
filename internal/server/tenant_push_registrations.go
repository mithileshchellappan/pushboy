package server

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type pushRegistrationRequest storage.TenantPushRegistrationChange

func (req *pushRegistrationRequest) UnmarshalJSON(body []byte) error {
	var operationID, providerConfigID string
	var expectedID *string
	var expectedVersion *int64
	err := decodeInstallationObject(body, map[string]any{
		"operationId":                 &operationID,
		"providerConfigId":            &providerConfigID,
		"token":                       &req.Token,
		"expectedRegistrationId":      &expectedID,
		"expectedRegistrationVersion": &expectedVersion,
	})
	if err != nil || len(operationID) != 36 || len(providerConfigID) != 36 || expectedVersion == nil ||
		*expectedVersion < 0 || (expectedID == nil) != (*expectedVersion == 0) || req.Token == "" {
		return storage.ErrInvalidTenantResource
	}
	req.OperationID, err = uuid.Parse(operationID)
	if err != nil || req.OperationID == uuid.Nil {
		return storage.ErrInvalidTenantResource
	}
	req.ProviderConfigID, err = uuid.Parse(providerConfigID)
	if err != nil || req.ProviderConfigID == uuid.Nil {
		return storage.ErrInvalidTenantResource
	}
	if expectedID != nil {
		id, err := uuid.Parse(*expectedID)
		if err != nil || len(*expectedID) != 36 || id == uuid.Nil {
			return storage.ErrInvalidTenantResource
		}
		req.ExpectedRegistrationID = &id
	}
	req.ExpectedRegistrationVersion = *expectedVersion
	return nil
}

func (s *Server) authorizePushRegistration(w http.ResponseWriter, r *http.Request, scope string) (uuid.UUID, uuid.UUID, error) {
	actor, err := s.authenticateServerKey(w, r, scope)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	param, err := decodedPathParam(r, "installationUUID")
	id, parseErr := uuid.Parse(param)
	if err != nil || parseErr != nil || len(param) != 36 || id == uuid.Nil {
		return uuid.Nil, uuid.Nil, storage.ErrInvalidTenantResource
	}
	return actor.TenantID, id, nil
}

func (s *Server) handleGetPushRegistration(w http.ResponseWriter, r *http.Request) {
	tenantID, deviceID, err := s.authorizePushRegistration(w, r, "read")
	if err != nil {
		tenantError(w, err, false)
		return
	}
	registration, err := s.tenants.GetPushRegistration(r.Context(), tenantID, deviceID)
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, registration, http.StatusOK)
}

func (s *Server) handleRegisterPush(w http.ResponseWriter, r *http.Request) {
	tenantID, deviceID, err := s.authorizePushRegistration(w, r, "devices:write")
	if err != nil {
		tenantError(w, err, false)
		return
	}
	var req pushRegistrationRequest
	if !decodeTenantResourceRequest(w, r, &req) {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	registration, err := s.tenants.RegisterPush(r.Context(), tenantID, deviceID, storage.TenantPushRegistrationChange(req))
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, registration, http.StatusOK)
}
