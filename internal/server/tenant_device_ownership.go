package server

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type installationOwnershipRequest struct {
	change storage.InstallationOwnershipChange
	target storage.InstallationIdentification
}

func (req *installationOwnershipRequest) UnmarshalJSON(body []byte) error {
	var uid *string
	var operationID string
	var identity json.RawMessage
	err := decodeInstallationObject(body, map[string]any{
		"uid":                      &uid,
		"identity":                 &identity,
		"operationId":              &operationID,
		"expectedOwnershipVersion": &req.change.ExpectedOwnershipVersion,
	})
	if err != nil || len(operationID) != 36 || req.change.ExpectedOwnershipVersion < 1 {
		return storage.ErrInvalidTenantResource
	}
	req.change.OperationID, err = uuid.Parse(operationID)
	if err != nil || req.change.OperationID == uuid.Nil {
		return storage.ErrInvalidTenantResource
	}
	if uid != nil {
		parsed, err := uuid.Parse(*uid)
		if err != nil || len(*uid) != 36 || parsed == uuid.Nil {
			return storage.ErrInvalidTenantResource
		}
		req.target.UID = &parsed
	}
	if identity != nil {
		var target storage.TenantUserIdentity
		fields := map[string]any{
			"namespace":  &target.Namespace,
			"externalId": &target.ExternalID,
		}
		if err := decodeInstallationObject(identity, fields); err != nil || len(fields) != 0 || target.Namespace == "" || target.ExternalID == "" {
			return storage.ErrInvalidTenantResource
		}
		req.target.Identity = &target
	}
	return nil
}

func (s *Server) handleIdentifyInstallation(w http.ResponseWriter, r *http.Request) {
	s.handleInstallationOwnership(w, r, true)
}

func (s *Server) handleLogoutInstallation(w http.ResponseWriter, r *http.Request) {
	s.handleInstallationOwnership(w, r, false)
}

func (s *Server) handleInstallationOwnership(w http.ResponseWriter, r *http.Request, identify bool) {
	scopes := []string{"devices:write"}
	if identify {
		scopes = append(scopes, "users:write")
	}
	actor, err := s.authenticateServerKey(w, r, scopes...)
	if err != nil {
		tenantError(w, err, false)
		return
	}
	param, err := decodedPathParam(r, "installationUUID")
	id, parseErr := uuid.Parse(param)
	if err != nil || parseErr != nil || len(param) != 36 || id == uuid.Nil {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	var req installationOwnershipRequest
	if !decodeTenantResourceRequest(w, r, &req) {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	var device storage.TenantInstallation
	if identify {
		if (req.target.UID == nil) == (req.target.Identity == nil) {
			tenantError(w, storage.ErrInvalidTenantResource, false)
			return
		}
		device, err = s.tenants.IdentifyInstallation(r.Context(), actor.TenantID, id, req.change, req.target)
	} else {
		if req.target.UID != nil || req.target.Identity != nil {
			tenantError(w, storage.ErrInvalidTenantResource, false)
			return
		}
		device, err = s.tenants.LogoutInstallation(r.Context(), actor.TenantID, id, req.change)
	}
	if err != nil {
		tenantError(w, err, false)
		return
	}
	s.respond(w, r, device, http.StatusOK)
}
