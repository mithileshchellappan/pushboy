package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type enrollInstallationRequest storage.InstallationObservations

func (req *enrollInstallationRequest) UnmarshalJSON(body []byte) error {
	var location json.RawMessage
	err := decodeInstallationObject(body, map[string]any{
		"os":                     &req.OS,
		"osVersion":              &req.OSVersion,
		"appVersion":             &req.AppVersion,
		"locale":                 &req.Locale,
		"timezone":               &req.Timezone,
		"notificationPermission": &req.NotificationPermission,
		"location":               &location,
	})
	if err != nil || location == nil {
		return err
	}
	var observation storage.InstallationLocation
	var observedAt string
	fields := map[string]any{
		"latitude":   &observation.Latitude,
		"longitude":  &observation.Longitude,
		"source":     &observation.Source,
		"observedAt": &observedAt,
	}
	if err := decodeInstallationObject(location, fields); err != nil {
		return err
	}
	if len(fields) != 0 {
		return storage.ErrInvalidTenantResource
	}
	observation.ObservedAt, err = time.Parse(time.RFC3339, observedAt)
	if err != nil {
		return err
	}
	req.Location = &observation
	return nil
}

func decodeInstallationObject(body []byte, fields map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return storage.ErrInvalidTenantResource
	}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		field := fields[name]
		if err != nil || !ok || field == nil {
			return storage.ErrInvalidTenantResource
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(raw, []byte("null")) {
			return storage.ErrInvalidTenantResource
		}
		if err := json.Unmarshal(raw, field); err != nil {
			return err
		}
		delete(fields, name)
	}
	return nil
}

func (s *Server) handleEnrollInstallation(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateServerKey(w, r, "devices:write")
	if err != nil {
		tenantError(w, err, false)
		return
	}

	param, err := decodedPathParam(r, "installationUUID")
	if err != nil || len(param) != 36 {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	id, err := uuid.Parse(param)
	if err != nil || id == uuid.Nil {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}
	var req enrollInstallationRequest
	if !decodeTenantResourceRequest(w, r, &req) {
		tenantError(w, storage.ErrInvalidTenantResource, false)
		return
	}

	device, created, err := s.tenants.EnrollInstallation(r.Context(), actor.TenantID, id, storage.InstallationObservations(req))
	if err != nil {
		tenantError(w, err, false)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.respond(w, r, device, status)
}
