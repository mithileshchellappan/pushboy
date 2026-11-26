package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestTenantResourcesRequireBearerAndScope(t *testing.T) {
	for _, route := range []struct {
		method, path, wrongScope string
	}{
		{http.MethodPost, "/v2/users", "topics:write"},
		{http.MethodGet, "/v2/users/" + uuid.NewString(), "users:write"},
		{http.MethodPost, "/v2/topics", "users:write"},
		{http.MethodGet, "/v2/topics/news", "topics:write"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			store := keyStoreStub{authenticate: func(_ context.Context, _ string) (storage.ServerKeyAccess, error) {
				return storage.ServerKeyAccess{TenantID: uuid.New(), Scopes: []string{route.wrongScope}}, nil
			}}
			router := New(nil, nil, nil, store).setupRouter()
			for _, tc := range []struct {
				bearer string
				want   int
			}{
				{"", http.StatusUnauthorized},
				{"Bearer valid", http.StatusForbidden},
			} {
				req := httptest.NewRequest(route.method, route.path+"?key=valid", nil)
				req.Header.Set("Authorization", tc.bearer)
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)
				if res.Code != tc.want {
					t.Fatalf("Authorization %q: status = %d, want %d", tc.bearer, res.Code, tc.want)
				}
			}
		})
	}
}

func TestTenantUserAndTopicHTTPIsolation(t *testing.T) {
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	t.Chdir("../..")
	store, err := storage.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bootstrap := func() (storage.Tenant, string) {
		t.Helper()
		var tenant storage.Tenant
		var key string
		err := store.BootstrapTenant(t.Context(), "http-isolation-"+uuid.NewString(), func(created storage.Tenant, secret string) error {
			tenant, key = created, secret
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, query := range []string{
				"DELETE FROM tenant_topics WHERE tenant_id = $1",
				"DELETE FROM tenant_users WHERE tenant_id = $1",
				"DELETE FROM tenant_api_keys WHERE tenant_id = $1",
				"DELETE FROM tenants WHERE id = $1",
			} {
				if _, err := db.Exec(query, tenant.ID); err != nil {
					t.Errorf("remove test tenant data: %v", err)
				}
			}
		})
		return tenant, key
	}
	_, keyA := bootstrap()
	tenantB, keyB := bootstrap()
	router := New(nil, nil, nil, store).setupRouter()
	request := func(method, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: status = %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		return res
	}

	identity := `{"identity":{"namespace":"accounts","externalId":"account-42"}}`
	createUser := func(key, body, wantKind string) string {
		t.Helper()
		res := request(http.MethodPost, "/v2/users?tenantId="+tenantB.ID.String(), key, body, http.StatusCreated)
		var user map[string]string
		if err := json.Unmarshal(res.Body.Bytes(), &user); err != nil {
			t.Fatal(err)
		}
		uid, err := uuid.Parse(user["uid"])
		if err != nil || uid == uuid.Nil || user["kind"] != wantKind || len(user) != 2 {
			t.Fatalf("unexpected user representation: %v", user)
		}
		return uid.String()
	}
	userA := createUser(keyA, identity, "identified")
	request(http.MethodPost, "/v2/users", keyA, identity, http.StatusConflict)
	request(http.MethodGet, "/v2/users/"+userA+"?tenantId="+tenantB.ID.String(), keyA, "", http.StatusOK)
	request(http.MethodGet, "/v2/users/"+strings.ToUpper(userA), keyA, "", http.StatusOK)
	request(http.MethodGet, "/v2/users/"+strings.Replace(userA, "-", "%2D", 1), keyA, "", http.StatusOK)
	for _, malformed := range []string{
		"x" + userA + "y",
		"%7B" + userA + "%7D",
	} {
		request(http.MethodGet, "/v2/users/"+malformed, keyA, "", http.StatusNotFound)
	}
	foreign := request(http.MethodGet, "/v2/users/"+userA, keyB, "", http.StatusNotFound)
	missing := request(http.MethodGet, "/v2/users/"+uuid.NewString(), keyB, "", http.StatusNotFound)
	if foreign.Body.String() != missing.Body.String() {
		t.Fatal("foreign user response differs from missing user")
	}
	if userB := createUser(keyB, identity, "identified"); userA == userB {
		t.Fatal("tenants received the same canonical user ID")
	}
	createUser(keyA, `{}`, "anonymous")
	createUser(keyA, `{"identity":null}`, "anonymous")

	topicID := "release notes/mobile%"
	topicPath := "/v2/topics/" + url.PathEscape(topicID)
	topicBody := func(name string) string { return fmt.Sprintf(`{"id":%q,"name":%q}`, topicID, name) }
	request(http.MethodPost, "/v2/topics", keyA, topicBody("Tenant A"), http.StatusCreated)
	request(http.MethodPost, "/v2/topics", keyA, topicBody("Tenant A"), http.StatusConflict)
	request(http.MethodGet, topicPath, keyB, "", http.StatusNotFound)
	request(http.MethodGet, "/v2/topics/%FF", keyA, "", http.StatusNotFound)
	request(http.MethodPost, "/v2/topics", keyB, topicBody("Tenant B"), http.StatusCreated)
	for _, tenant := range []struct{ key, name string }{{keyA, "Tenant A"}, {keyB, "Tenant B"}} {
		res := request(http.MethodGet, topicPath+"?tenantId="+tenantB.ID.String(), tenant.key, "", http.StatusOK)
		var topic map[string]string
		if err := json.Unmarshal(res.Body.Bytes(), &topic); err != nil {
			t.Fatal(err)
		}
		if len(topic) != 2 || topic["id"] != topicID || topic["name"] != tenant.name {
			t.Fatalf("unexpected topic representation: %v", topic)
		}
	}

	userBody := func(id string) string {
		return `{"identity":{"namespace":"accounts","externalId":"` + id + `"}}`
	}
	createUser(keyA, userBody("�"), "identified")
	createUser(keyA, userBody(`\ud83d\ude80-\\ud800`), "identified")
	request(http.MethodPost, "/v2/users", keyA, userBody(`🚀-\\ud800`), http.StatusConflict)
	request(http.MethodPost, "/v2/topics", keyA, `{"id":"\ud83d\ude80-\ufffd","name":"Unicode topic"}`, http.StatusCreated)
	request(http.MethodGet, "/v2/topics/"+url.PathEscape("🚀-�"), keyA, "", http.StatusOK)

	for _, malformed := range []string{"\xff", `\ud800`, `\udc00`, `\ud800\u0020`, `\ud800\ud800`} {
		request(http.MethodPost, "/v2/users", keyA, userBody(malformed), http.StatusBadRequest)
	}

	for _, invalid := range []struct{ path, body string }{
		{"/v2/users", `null`},
		{"/v2/users", `{"uid":"caller-chosen"}`},
		{"/v2/users", `{"identity":{"namespace":"accounts","externalId":"account-42","tenantId":"caller-chosen"}}`},
		{"/v2/users", `{"Identity":{"namespace":"accounts","externalId":"case"}}`},
		{"/v2/users", `{"identity":{"Namespace":"accounts","externalId":"case"}}`},
		{"/v2/users", `{"identity":{"namespace":"accounts","ExternalID":"case"}}`},
		{"/v2/users", `{"identity":{"namespace":"accounts"},"identity":{"externalId":"split"}}`},
		{"/v2/topics", `{"id":"guides","name":"Guides","tenantId":"caller-chosen"}`},
		{"/v2/topics", `{"ID":"guides","name":"Guides"}`},
		{"/v2/topics", `{"id":"guides","NAME":"Guides"}`},
		{"/v2/topics", `{"id":"guides","name":"Guides"} {}`},
		{"/v2/topics", `{"id":"\ud800","name":"Invalid"}`},
	} {
		request(http.MethodPost, invalid.path, keyA, invalid.body, http.StatusBadRequest)
	}
}
