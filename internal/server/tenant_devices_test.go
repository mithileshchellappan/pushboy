package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type installationStoreStub struct {
	keyStoreStub
	enroll func(context.Context, uuid.UUID, uuid.UUID, storage.InstallationObservations) (storage.TenantInstallation, bool, error)
}

func (s installationStoreStub) EnrollInstallation(ctx context.Context, tenantID, id uuid.UUID, observations storage.InstallationObservations) (storage.TenantInstallation, bool, error) {
	return s.enroll(ctx, tenantID, id, observations)
}

func TestInstallationEnrollmentHTTP(t *testing.T) {
	tenantID, id := uuid.New(), uuid.New()
	device := storage.TenantInstallation{ID: id, UID: uuid.New(), OwnershipVersion: 1}
	calls := 0
	store := installationStoreStub{
		keyStoreStub: keyStoreStub{authenticate: func(_ context.Context, key string) (storage.ServerKeyAccess, error) {
			if key != "backend-key" {
				return storage.ServerKeyAccess{}, storage.ErrInvalidServerKey
			}
			return storage.ServerKeyAccess{TenantID: tenantID, Scopes: []string{"devices:write"}}, nil
		}},
		enroll: func(_ context.Context, tenant, installation uuid.UUID, observations storage.InstallationObservations) (storage.TenantInstallation, bool, error) {
			if tenant != tenantID || installation != id {
				t.Fatalf("unexpected enrollment tenant %v or ID %v", tenant, installation)
			}
			calls++
			if calls == 1 {
				if observations.OS == nil || *observations.OS != "iOS" || observations.AppVersion != nil ||
					observations.Location == nil || observations.Location.Latitude != 0 || observations.Location.Longitude != 1 ||
					observations.Location.Source != "gps" || !observations.Location.ObservedAt.Equal(time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)) {
					t.Fatalf("unexpected observations: %+v", observations)
				}
			} else if observations != (storage.InstallationObservations{}) {
				t.Fatalf("expected an empty heartbeat, received %+v", observations)
			}
			if calls == 3 {
				return storage.TenantInstallation{}, false, storage.ErrTenantInstallationRetired
			}
			return device, calls == 1, nil
		},
	}
	router := New(nil, nil, nil, store, nil).setupRouter()
	request := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v2/devices/"+id+"?tenantId="+uuid.NewString(), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer backend-key")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	for _, tc := range []struct {
		id, body string
	}{
		{"not-an-id", `{}`},
		{uuid.Nil.String(), `{}`},
		{id.String(), ``},
		{id.String(), `null`},
		{id.String(), `[]`},
		{id.String(), `{} {}`},
		{id.String(), `{"uid":"caller-chosen"}`},
		{id.String(), `{"ownershipVersion":2}`},
		{id.String(), `{"traits":{}}`},
		{id.String(), `{"tenantId":"caller-chosen"}`},
		{id.String(), `{"OS":"iOS"}`},
		{id.String(), `{"os":"iOS","\u006fs":"Android"}`},
		{id.String(), `{"os":"\ud800"}`},
		{id.String(), `{"os":null}`},
		{id.String(), `{"location":null}`},
		{id.String(), `{"location":{}}`},
		{id.String(), `{"location":{"latitude":0,"longitude":0,"source":"gps"}}`},
		{id.String(), `{"location":{"latitude":0,"longitude":0,"source":null,"observedAt":"2026-11-26T08:00:00Z"}}`},
		{id.String(), `{"location":{"latitude":0,"longitude":0,"source":"gps","source":"geoip","observedAt":"2026-11-26T08:00:00Z"}}`},
		{id.String(), `{"location":{"latitude":0,"longitude":0,"source":"gps","observedAt":"yesterday"}}`},
		{id.String(), fmt.Sprintf(`{"os":"%s"}`, strings.Repeat("a", maxTenantRequestBytes))},
	} {
		if res := request(tc.id, tc.body); res.Code != http.StatusBadRequest {
			t.Fatalf("invalid enrollment request returned %d: %s", res.Code, res.Body)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid enrollment called storage %d times", calls)
	}

	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{strings.ToUpper(id.String()), `{"os":"iOS","location":{"latitude":0,"longitude":1,"source":"gps","observedAt":"2026-11-27\u005400:00:00\u002b16:00"}}`, http.StatusCreated},
		{strings.Replace(id.String(), "-", "%2D", 1), `{}`, http.StatusOK},
	} {
		res := request(tc.path, tc.body)
		if res.Code != tc.status || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status = %d, body = %s, want %d", res.Code, res.Body, tc.status)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || len(body) != 3 {
			t.Fatalf("unexpected association response: %s, %v", res.Body, err)
		}
		var got storage.TenantInstallation
		if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || got != device {
			t.Fatalf("association = %+v, %v, want %+v", got, err, device)
		}
	}
	if res := request(id.String(), `{}`); res.Code != http.StatusGone {
		t.Fatalf("status = %d, body = %s, want 410", res.Code, res.Body)
	}
}
