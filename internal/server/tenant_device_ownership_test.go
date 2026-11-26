package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type installationOwnershipStoreStub struct {
	keyStoreStub
	change func(uuid.UUID, uuid.UUID, storage.InstallationOwnershipChange, storage.InstallationIdentification) (storage.TenantInstallation, error)
}

func (s installationOwnershipStoreStub) IdentifyInstallation(_ context.Context, tenantID, id uuid.UUID, change storage.InstallationOwnershipChange, target storage.InstallationIdentification) (storage.TenantInstallation, error) {
	return s.change(tenantID, id, change, target)
}

func (s installationOwnershipStoreStub) LogoutInstallation(_ context.Context, tenantID, id uuid.UUID, change storage.InstallationOwnershipChange) (storage.TenantInstallation, error) {
	return s.change(tenantID, id, change, storage.InstallationIdentification{})
}

func TestInstallationOwnershipHTTPContract(t *testing.T) {
	tenantID, installationID, requestID, uid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	version := fmt.Sprintf(`"operationId":"%s","expectedOwnershipVersion":4`, requestID)
	target := fmt.Sprintf(`"uid":"%s"`, uid)
	destination := storage.TenantInstallation{ID: installationID, UID: uid, OwnershipVersion: 5}
	for _, tc := range []struct {
		name, action, fields, authorization string
		scopes                              []string
		storeErr                            error
		want, calls                         int
	}{
		{name: "missing credential", action: "logout", fields: version, want: http.StatusUnauthorized},
		{name: "identify requires user scope", action: "identify", fields: version + "," + target, authorization: "Bearer scoped", scopes: []string{"devices:write"}, want: http.StatusForbidden},
		{name: "identify requires device scope", action: "identify", fields: version + "," + target, authorization: "Bearer scoped", scopes: []string{"users:write"}, want: http.StatusForbidden},
		{name: "identify by UID", action: "identify", fields: version + "," + target, authorization: "Bearer scoped", scopes: []string{"devices:write", "users:write"}, want: http.StatusOK, calls: 1},
		{name: "logout without user scope", action: "logout", fields: version, authorization: "Bearer scoped", scopes: []string{"devices:write"}, want: http.StatusOK, calls: 1},
		{name: "logout with empty target field", action: "logout", fields: version + `,"uid":""`, authorization: "Bearer scoped", scopes: []string{"devices:write"}, want: http.StatusBadRequest},
		{name: "reject repeated operation key", action: "logout", fields: version + fmt.Sprintf(`,"operationId":"%s"`, requestID), authorization: "Bearer scoped", scopes: []string{"devices:write"}, want: http.StatusBadRequest},
		{name: "reject repeated identity key", action: "identify", fields: version + `,"identity":{"namespace":"issuer","externalId":"1","externalId":"2"}`, authorization: "Bearer scoped", scopes: []string{"devices:write", "users:write"}, want: http.StatusBadRequest},
		{name: "reject supplied tenant", action: "logout", fields: version + `,"tenantId":"spoofed"`, authorization: "Bearer scoped", scopes: []string{"devices:write"}, want: http.StatusBadRequest},
		{name: "conflict", action: "logout", fields: version, authorization: "Bearer scoped", scopes: []string{"devices:write"}, storeErr: storage.ErrInstallationOwnershipConflict, want: http.StatusConflict, calls: 1},
		{name: "retired installation", action: "logout", fields: version, authorization: "Bearer scoped", scopes: []string{"devices:write"}, storeErr: storage.ErrTenantInstallationRetired, want: http.StatusGone, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authCalls, calls := 0, 0
			store := installationOwnershipStoreStub{
				keyStoreStub: keyStoreStub{authenticate: func(_ context.Context, key string) (storage.ServerKeyAccess, error) {
					authCalls++
					if key != "scoped" {
						t.Fatalf("unexpected key: %q", key)
					}
					return storage.ServerKeyAccess{TenantID: tenantID, Scopes: tc.scopes}, nil
				}},
				change: func(tenant, installation uuid.UUID, change storage.InstallationOwnershipChange, target storage.InstallationIdentification) (storage.TenantInstallation, error) {
					calls++
					if tenant != tenantID || installation != installationID || change.OperationID != requestID || change.ExpectedOwnershipVersion != 4 {
						t.Fatalf("unexpected ownership request: %s %s %+v", tenant, installation, change)
					}
					if tc.action == "identify" && (target.UID == nil || *target.UID != uid) {
						t.Fatalf("unexpected UID target: %+v", target)
					}
					return destination, tc.storeErr
				},
			}
			router := (&Server{tenants: store}).setupRouter()
			req := httptest.NewRequest(http.MethodPost, "/v2/devices/"+installationID.String()+"/"+tc.action, strings.NewReader("{"+tc.fields+"}"))
			req.Header.Set("Authorization", tc.authorization)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.want || calls != tc.calls || tc.authorization != "" && authCalls != 1 {
				t.Fatalf("status %d, auth calls %d, mutations %d, response %s", response.Code, authCalls, calls, response.Body.String())
			}
			if response.Code == http.StatusOK {
				var got storage.TenantInstallation
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got != destination {
					t.Fatalf("response = %+v, %v", got, err)
				}
			}
		})
	}
}
