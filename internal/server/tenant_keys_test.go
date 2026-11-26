package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type keyStoreStub struct {
	TenantStore
	authenticate func(context.Context, string) (storage.ServerKeyAccess, error)
	issue        func(context.Context, storage.ServerKeyAccess, []string, *time.Time) (uuid.UUID, string, error)
	revoke       func(context.Context, storage.ServerKeyAccess, uuid.UUID) error
}

func (s keyStoreStub) AuthenticateServerKey(ctx context.Context, key string) (storage.ServerKeyAccess, error) {
	return s.authenticate(ctx, key)
}
func (s keyStoreStub) IssueServerKey(ctx context.Context, actor storage.ServerKeyAccess, scopes []string, expiresAt *time.Time) (uuid.UUID, string, error) {
	return s.issue(ctx, actor, scopes, expiresAt)
}
func (s keyStoreStub) RevokeServerKey(ctx context.Context, actor storage.ServerKeyAccess, id uuid.UUID) error {
	return s.revoke(ctx, actor, id)
}

func TestKeyManagementRequiresExplicitScopedBearer(t *testing.T) {
	for _, tc := range []struct {
		name, authorization string
		authErr             error
		scopes              []string
		want                int
	}{
		{name: "missing", want: 401},
		{name: "wrong scheme", authorization: "Basic secret", want: 401},
		{name: "invalid", authorization: "Bearer invalid", authErr: storage.ErrInvalidServerKey, want: 401},
		{name: "insufficient scope", authorization: "Bearer valid", scopes: []string{"read"}, want: 403},
		{name: "database failure", authorization: "Bearer valid", authErr: errors.New("sensitive-credential"), want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := keyStoreStub{authenticate: func(context.Context, string) (storage.ServerKeyAccess, error) {
				return storage.ServerKeyAccess{Scopes: tc.scopes}, tc.authErr
			}}
			router := New(nil, nil, nil, store, nil).setupRouter()
			for _, route := range []struct{ method, path string }{
				{http.MethodPost, "/v2/keys"},
				{http.MethodDelete, "/v2/keys/" + uuid.NewString()},
			} {
				req := httptest.NewRequest(route.method, route.path+"?key=sensitive-credential", nil)
				req.Header.Set("Authorization", tc.authorization)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tc.want || strings.Contains(rec.Body.String(), "sensitive-credential") {
					t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
				}
				if tc.want == 401 && rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatal("missing challenge")
				}
			}
		})
	}
}

func TestIssueServerKeyContract(t *testing.T) {
	actor := storage.ServerKeyAccess{TenantID: uuid.New(), KeyID: uuid.New(), Scopes: []string{"keys:manage", "read"}}
	id := uuid.New()
	expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	var logs bytes.Buffer
	previousLogger := middleware.DefaultLogger
	middleware.DefaultLogger = middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: log.New(&logs, "", 0), NoColor: true})
	t.Cleanup(func() { middleware.DefaultLogger = previousLogger })
	calls := 0
	store := keyStoreStub{
		authenticate: func(_ context.Context, key string) (storage.ServerKeyAccess, error) {
			if key != "backend-key" {
				t.Fatal("unexpected key")
			}
			return actor, nil
		},
		issue: func(_ context.Context, got storage.ServerKeyAccess, scopes []string, expiresAt *time.Time) (uuid.UUID, string, error) {
			calls++
			if got.TenantID != actor.TenantID || got.KeyID != actor.KeyID || len(scopes) != 1 || scopes[0] != "read" || expiresAt == nil || !expiresAt.Equal(expiry) {
				t.Fatal("wrong key request")
			}
			return id, "new-secret", nil
		},
	}
	router := New(nil, nil, nil, store, nil).setupRouter()
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v2/keys?tenantId=untrusted&key=query-secret", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer backend-key")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := request(`{"scopes":["read"],"expiresAt":"2030-01-02T03:04:05Z"}`)
	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" || len(response) != 2 || response["id"] != id.String() || response["key"] != "new-secret" {
		t.Fatalf("unexpected response %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{}`, `{"scopes":null}`, `{"scopes":["read"],"tenantId":"untrusted"}`, `{"scopes":["read"]} {}`, `{"scopes":[],"expiresAt":"invalid"}`} {
		if rec := request(body); rec.Code != 400 {
			t.Fatalf("body %s: status %d", body, rec.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("issue calls = %d", calls)
	}
	if logs.Len() != 0 {
		t.Fatalf("key route emitted access logs: %s", logs.String())
	}
}

func TestRevokeServerKeyContract(t *testing.T) {
	actor := storage.ServerKeyAccess{TenantID: uuid.New(), KeyID: uuid.New(), Scopes: []string{"keys:manage"}}
	id := uuid.New()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"revoked or already revoked", nil, 204},
		{"foreign or missing", storage.Errors.NotFound, 404},
		{"actor revoked concurrently", storage.ErrInvalidServerKey, 401},
		{"scope removed concurrently", storage.ErrInsufficientKeyScope, 403},
		{"internal error", errors.New("secret-bearing-error"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := keyStoreStub{
				authenticate: func(context.Context, string) (storage.ServerKeyAccess, error) { return actor, nil },
				revoke: func(_ context.Context, got storage.ServerKeyAccess, target uuid.UUID) error {
					if got.TenantID != actor.TenantID || got.KeyID != actor.KeyID || target != id {
						t.Fatal("wrong revoke authority")
					}
					return tc.err
				},
			}
			router := New(nil, nil, nil, store, nil).setupRouter()
			req := httptest.NewRequest(http.MethodDelete, "/v2/keys/"+id.String(), nil)
			req.Header.Set("Authorization", "Bearer backend-key")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), "secret-bearing-error") || (tc.want == 204 && rec.Body.Len() != 0) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIssueServerKeyStorageRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid scopes or expiry", storage.ErrInvalidKeyRequest, 400},
		{"scope escalation", storage.ErrInsufficientKeyScope, 403},
		{"actor became inactive", storage.ErrInvalidServerKey, 401},
		{"database failure", errors.New("credential-must-not-leak"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := keyStoreStub{
				authenticate: func(context.Context, string) (storage.ServerKeyAccess, error) {
					return storage.ServerKeyAccess{Scopes: []string{"keys:manage"}}, nil
				},
				issue: func(context.Context, storage.ServerKeyAccess, []string, *time.Time) (uuid.UUID, string, error) {
					return uuid.Nil, "credential-must-not-leak", tc.err
				},
			}
			router := New(nil, nil, nil, store, nil).setupRouter()
			req := httptest.NewRequest(http.MethodPost, "/v2/keys", strings.NewReader(`{"scopes":["read"]}`))
			req.Header.Set("Authorization", "Bearer key")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), "credential-must-not-leak") || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}
