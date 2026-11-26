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
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type pushRegistrationStoreStub struct {
	keyStoreStub
	get func(uuid.UUID, uuid.UUID) (storage.TenantPushRegistration, error)
	put func(uuid.UUID, uuid.UUID, storage.TenantPushRegistrationChange) (storage.TenantPushRegistration, error)
}

func (s pushRegistrationStoreStub) GetPushRegistration(_ context.Context, tenantID, deviceID uuid.UUID) (storage.TenantPushRegistration, error) {
	return s.get(tenantID, deviceID)
}

func (s pushRegistrationStoreStub) RegisterPush(_ context.Context, tenantID, deviceID uuid.UUID, change storage.TenantPushRegistrationChange) (storage.TenantPushRegistration, error) {
	return s.put(tenantID, deviceID, change)
}

func TestPushRegistrationHTTPContract(t *testing.T) {
	tenantID, deviceID, routeID, operationID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	token := "fixture-private-endpoint"
	base := fmt.Sprintf(`"operationId":"%s","providerConfigId":"%s","token":"%s"`, operationID, routeID, token)
	request := base + `,"expectedRegistrationVersion":0`
	escapedToken := strings.Repeat(`\u0041`, storage.MaxPushRegistrationTokenBytes)
	escapedRequest := strings.Replace(request, token, escapedToken, 1)
	registration := storage.TenantPushRegistration{
		ID: uuid.New(), ProviderConfigID: routeID, Provider: model.FCM, RegistrationVersion: 1, Valid: false,
	}
	for _, tt := range []struct {
		name, method, key, scope, body string
		wantToken                      string
		status, calls                  int
		storeErr                       error
	}{
		{name: "missing key", method: "GET", status: 401},
		{name: "read current invalidated registration", method: "GET", key: "backend", scope: "read", status: 200, calls: 1},
		{name: "read scope required", method: "GET", key: "backend", scope: "devices:write", status: 403},
		{name: "write scope required", method: "PUT", key: "backend", scope: "read", body: request, status: 403},
		{name: "register", method: "PUT", key: "backend", scope: "devices:write", body: request, status: 200, calls: 1},
		{name: "accept fully escaped maximum token", method: "PUT", key: "backend", scope: "devices:write", body: escapedRequest, wantToken: strings.Repeat("A", storage.MaxPushRegistrationTokenBytes), status: 200, calls: 1},
		{name: "reject escaped oversized token", method: "PUT", key: "backend", scope: "devices:write", body: strings.Replace(escapedRequest, escapedToken, escapedToken+`\u0041`, 1), status: 400},
		{name: "reject oversized encoded body", method: "PUT", key: "backend", scope: "devices:write", body: request + strings.Repeat(" ", maxPushRegistrationRequestBytes), status: 400},
		{name: "explicit initial version required", method: "PUT", key: "backend", scope: "devices:write", body: base, status: 400},
		{name: "current ID required", method: "PUT", key: "backend", scope: "devices:write", body: base + `,"expectedRegistrationVersion":1`, status: 400},
		{name: "reject duplicate token", method: "PUT", key: "backend", scope: "devices:write", body: request + `,"token":"another"`, status: 400},
		{name: "reject supplied owner", method: "PUT", key: "backend", scope: "devices:write", body: request + `,"uid":"another"`, status: 400},
		{name: "conflicting endpoint", method: "PUT", key: "backend", scope: "devices:write", body: request, status: 409, calls: 1, storeErr: storage.ErrPushRegistrationConflict},
		{name: "redact storage error", method: "PUT", key: "backend", scope: "devices:write", body: request, status: 500, calls: 1, storeErr: fmt.Errorf("stored endpoint %s", token)},
		{name: "retired installation", method: "GET", key: "backend", scope: "read", status: 410, calls: 1, storeErr: storage.ErrTenantInstallationRetired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			wantToken := tt.wantToken
			if wantToken == "" {
				wantToken = token
			}
			get := func(tenant, device uuid.UUID) (storage.TenantPushRegistration, error) {
				calls++
				if tenant != tenantID || device != deviceID {
					t.Fatal("registration operation used wrong scope")
				}
				return registration, tt.storeErr
			}
			store := pushRegistrationStoreStub{
				keyStoreStub: keyStoreStub{authenticate: func(context.Context, string) (storage.ServerKeyAccess, error) {
					return storage.ServerKeyAccess{TenantID: tenantID, Scopes: []string{tt.scope}}, nil
				}},
				get: get,
				put: func(tenant, device uuid.UUID, change storage.TenantPushRegistrationChange) (storage.TenantPushRegistration, error) {
					if change.ProviderConfigID != routeID || change.OperationID != operationID || change.Token != wantToken ||
						change.ExpectedRegistrationID != nil || change.ExpectedRegistrationVersion != 0 {
						t.Fatal("unexpected registration request")
					}
					return get(tenant, device)
				},
			}
			router := (&Server{tenants: store}).setupRouter()
			req := httptest.NewRequest(tt.method, "/v2/devices/"+deviceID.String()+"/push-registration", strings.NewReader("{"+tt.body+"}"))
			if tt.key != "" {
				req.Header.Set("Authorization", "Bearer "+tt.key)
			}
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != tt.status || calls != tt.calls || strings.Contains(res.Body.String(), token) {
				t.Fatalf("registration status=%d calls=%d response=%q", res.Code, calls, res.Body.String())
			}
			if res.Code == http.StatusOK {
				var got storage.TenantPushRegistration
				if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || got != registration {
					t.Fatalf("registration payload: %+v, %v", got, err)
				}
			}
		})
	}
}
