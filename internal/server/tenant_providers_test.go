package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestTenantProviderHTTPIsolation(t *testing.T) {
	dsn := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	t.Chdir("../..")
	store, err := storage.NewPostgresStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bootstrap := func() (storage.Tenant, string) {
		t.Helper()
		var tenant storage.Tenant
		var key string
		err := store.BootstrapTenant(t.Context(), "provider-http-"+uuid.NewString(), func(created storage.Tenant, secret string) error {
			tenant, key = created, secret
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, query := range []string{
				"DELETE FROM provider_configs WHERE tenant_id = $1",
				"DELETE FROM tenant_api_keys WHERE tenant_id = $1",
				"DELETE FROM tenants WHERE id = $1",
			} {
				if _, err := db.Exec(query, tenant.ID); err != nil {
					t.Error(err)
				}
			}
		})
		return tenant, key
	}
	_, keyA := bootstrap()
	_, keyB := bootstrap()
	actor, err := store.AuthenticateServerKey(t.Context(), keyA)
	if err != nil {
		t.Fatal(err)
	}
	_, readOnly, err := store.IssueServerKey(t.Context(), actor, []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, manageOnly, err := store.IssueServerKey(t.Context(), actor, []string{"providers:manage"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := storage.NewTenantProviderStore(store, "test-key", `{"test-key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	if err != nil {
		t.Fatal(err)
	}
	router := New(nil, nil, nil, store, providers, nil).setupRouter()
	request := func(handler http.Handler, method, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: status = %d, want %d, cache control = %q", method, path, res.Code, want, res.Header().Get("Cache-Control"))
		}
		for _, secret := range []string{"credentialKeyId", "credentialNonce", "credentialCiphertext", "PRIVATE KEY", "A1B2C3D4E5", "test-key"} {
			if strings.Contains(res.Body.String(), secret) {
				t.Fatalf("%s %s returned credential material", method, path)
			}
		}
		return res
	}

	const path = "/v2/providers"
	request(router, http.MethodPost, path, "", "", http.StatusUnauthorized)
	request(router, http.MethodPost, path, readOnly, "", http.StatusForbidden)
	request(router, http.MethodGet, path, manageOnly, "", http.StatusForbidden)
	request(router, http.MethodPost, path, keyA, `{"provider":"apns","environment":"production","applicationId":"com.example.app","credentials":{"privateKey":"synthetic-secret"}}`, http.StatusBadRequest)

	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(map[string]string{
		"keyId": "A1B2C3D4E5", "teamId": "Z1Y2X3W4V5",
		"privateKey": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	create := `{"provider":"apns","environment":"production","applicationId":"com.example.App","credentials":` + string(credentials) + `}`
	toRoute := func(res *httptest.ResponseRecorder) storage.TenantProvider {
		t.Helper()
		var route storage.TenantProvider
		if err := json.Unmarshal(res.Body.Bytes(), &route); err != nil {
			t.Fatal(err)
		}
		return route
	}
	a := toRoute(request(router, http.MethodPost, path, manageOnly, create, http.StatusCreated))
	b := toRoute(request(router, http.MethodPost, path, keyB, create, http.StatusCreated))
	if !a.Ready || !b.Ready || a.ID == b.ID || a.CredentialVersion != 1 || a.ApplicationID != "com.example.App" {
		t.Fatalf("ready isolated routes: %+v and %+v", a, b)
	}
	routePath := path + "/" + a.ID.String()
	request(router, http.MethodPost, path, keyA, create, http.StatusConflict)
	request(router, http.MethodPost, path, keyA, strings.Replace(create, "com.example.App", "com.example.app", 1), http.StatusConflict)
	request(router, http.MethodGet, routePath, keyB, "", http.StatusNotFound)
	rotation := `{"credentialVersion":1,"credentials":` + string(credentials) + `}`
	request(router, http.MethodPut, routePath, keyB, rotation, http.StatusNotFound)
	res := request(router, http.MethodGet, path, readOnly, "", http.StatusOK)
	var routes []storage.TenantProvider
	if err := json.Unmarshal(res.Body.Bytes(), &routes); err != nil || len(routes) != 1 || routes[0].ID != a.ID {
		t.Fatalf("tenant list = %+v, error = %v", routes, err)
	}
	rotated := toRoute(request(router, http.MethodPut, routePath, manageOnly, rotation, http.StatusOK))
	if !rotated.Ready || rotated.ID != a.ID || rotated.CredentialVersion != 2 {
		t.Fatalf("rotated route = %+v", rotated)
	}
	request(router, http.MethodPut, routePath, keyA, rotation, http.StatusConflict)

	malformed, err := storage.NewTenantProviderStore(store, "new-active", `{
		"test-key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"new-active":"synthetic-malformed-entry",
		"wrong-type":{"unused":"synthetic-object-entry"}
	}`)
	if err == nil || strings.Contains(err.Error(), "synthetic-") || strings.Contains(err.Error(), "new-active") {
		t.Fatalf("expected a redacted keyring diagnostic, got %v", err)
	}
	malformedRouter := New(nil, nil, nil, store, malformed, nil).setupRouter()
	retained := toRoute(request(malformedRouter, http.MethodGet, routePath, keyA, "", http.StatusOK))
	if !retained.Ready {
		t.Fatalf("valid retained key with malformed active key = %+v", retained)
	}
	request(malformedRouter, http.MethodPost, path, keyA, strings.Replace(create, "production", "sandbox", 1), http.StatusServiceUnavailable)

	unavailable, err := storage.NewTenantProviderStore(store, "", "")
	if err != nil {
		t.Fatal(err)
	}
	unavailableRouter := New(nil, nil, nil, store, unavailable, nil).setupRouter()
	missing := toRoute(request(unavailableRouter, http.MethodGet, routePath, keyA, "", http.StatusOK))
	if missing.Ready || missing.ReadinessReason != "encryption_key_unavailable" {
		t.Fatalf("missing key route = %+v", missing)
	}
	request(unavailableRouter, http.MethodPost, path, keyA, create, http.StatusServiceUnavailable)
	disabled := toRoute(request(unavailableRouter, http.MethodPut, routePath, keyA, `{"credentialVersion":2,"enabled":false}`, http.StatusOK))
	if disabled.Enabled || disabled.Ready || disabled.ReadinessReason != "disabled" || disabled.CredentialVersion != 2 {
		t.Fatalf("disabled route = %+v", disabled)
	}
	res = httptest.NewRecorder()
	malformedRouter.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", res.Code)
	}
}

func TestTenantProviderRequiresBearerAndScope(t *testing.T) {
	store := keyStoreStub{authenticate: func(context.Context, string) (storage.ServerKeyAccess, error) {
		return storage.ServerKeyAccess{TenantID: uuid.New(), Scopes: []string{"send"}}, nil
	}}
	router := New(nil, nil, nil, store, nil, nil).setupRouter()
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v2/providers"},
		{http.MethodGet, "/v2/providers/" + uuid.NewString()},
		{http.MethodPost, "/v2/providers"},
		{http.MethodPut, "/v2/providers/" + uuid.NewString()},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without bearer returned %d", route.method, route.path, response.Code)
		}
		req.Header.Set("Authorization", "Bearer test")
		response = httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s without required scope returned %d", route.method, route.path, response.Code)
		}
	}
}
