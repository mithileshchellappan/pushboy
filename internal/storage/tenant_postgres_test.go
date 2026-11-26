package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

func TestBootstrapTenantCreatesOneAppAndHashedKey(t *testing.T) {
	store := tenantKeyManagementStore(t)

	name := "bootstrap-" + uuid.NewString()
	var attemptedID uuid.UUID
	err := store.BootstrapTenant(t.Context(), name, func(created Tenant, _ string) error {
		attemptedID = created.ID
		return io.ErrClosedPipe
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failed handoff error = %v, want closed pipe", err)
	}
	var tenantExists, keyExists bool
	err = store.db.QueryRowContext(t.Context(), `
		SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1),
		       EXISTS (SELECT 1 FROM tenant_api_keys WHERE tenant_id = $1)
	`, attemptedID).Scan(&tenantExists, &keyExists)
	if err != nil || tenantExists || keyExists {
		t.Fatalf("after failed handoff: tenant = %t, key = %t, error = %v", tenantExists, keyExists, err)
	}

	var tenant Tenant
	var key string
	err = store.BootstrapTenant(t.Context(), name, func(created Tenant, secret string) error {
		tenant, key = created, secret
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := store.db.Exec(`DELETE FROM tenant_api_keys WHERE tenant_id = $1`, tenant.ID); err != nil {
			t.Errorf("delete test key: %v", err)
		}
		if _, err := store.db.Exec(`DELETE FROM tenants WHERE id = $1`, tenant.ID); err != nil {
			t.Errorf("delete test tenant: %v", err)
		}
	})
	keyIDText, secretText, ok := strings.Cut(key, ".")
	if !ok {
		t.Fatal("key separator missing")
	}
	keyID, err := uuid.Parse(keyIDText)
	if err != nil {
		t.Fatalf("key ID: %v", err)
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil || len(secret) != 32 {
		t.Fatalf("secret decoding: length = %d, err = %v", len(secret), err)
	}

	var storedTenantID uuid.UUID
	var storedHash []byte
	var scopes []string
	err = store.db.QueryRow(`
		SELECT t.id, k.secret_hash, k.scopes
		FROM tenants t JOIN tenant_api_keys k ON k.tenant_id = t.id
		WHERE t.name = $1 AND k.id = $2`, name, keyID,
	).Scan(&storedTenantID, &storedHash, pq.Array(&scopes))
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(secret)
	if tenant.ID != storedTenantID || tenant.Name != name || !bytes.Equal(storedHash, wantHash[:]) {
		t.Fatal("bootstrap did not persist the expected tenant and key digest")
	}
	wantScopes := []string{"read", "send", "users:write", "devices:write", "topics:write", "keys:manage", "providers:manage"}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("initial scopes = %v, want %v", scopes, wantScopes)
	}
	access, err := store.AuthenticateServerKey(context.Background(), key)
	if err != nil || access.TenantID != tenant.ID || access.KeyID != keyID || !slices.Equal(access.Scopes, wantScopes) {
		t.Fatalf("authenticate issued key: access = %+v, err = %v", access, err)
	}

	tampered := append([]byte(nil), secret...)
	tampered[0] ^= 1
	for _, invalid := range []string{
		"bad-key",
		uuid.NewString() + "." + secretText,
		keyIDText + "." + base64.RawURLEncoding.EncodeToString(tampered),
	} {
		if _, err := store.AuthenticateServerKey(context.Background(), invalid); !errors.Is(err, ErrInvalidServerKey) {
			t.Fatalf("invalid key error = %v, want ErrInvalidServerKey", err)
		}
	}

	for _, state := range []struct {
		name  string
		id    uuid.UUID
		set   string
		clear string
	}{
		{"revoked", keyID, `UPDATE tenant_api_keys SET revoked_at = statement_timestamp() WHERE id = $1`, `UPDATE tenant_api_keys SET revoked_at = NULL WHERE id = $1`},
		{"expired", keyID, `UPDATE tenant_api_keys SET created_at = statement_timestamp() - interval '2 seconds', expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, `UPDATE tenant_api_keys SET expires_at = NULL WHERE id = $1`},
		{"tenant disabled", tenant.ID, `UPDATE tenants SET disabled_at = statement_timestamp() WHERE id = $1`, `UPDATE tenants SET disabled_at = NULL WHERE id = $1`},
	} {
		t.Run(state.name, func(t *testing.T) {
			if _, err := store.db.Exec(state.set, state.id); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := store.db.Exec(state.clear, state.id); err != nil {
					t.Errorf("restore key state: %v", err)
				}
			})
			if _, err := store.AuthenticateServerKey(context.Background(), key); !errors.Is(err, ErrInvalidServerKey) {
				t.Fatalf("inactive key error = %v, want ErrInvalidServerKey", err)
			}
		})
	}

	err = store.BootstrapTenant(t.Context(), name, func(Tenant, string) error {
		t.Fatal("duplicate bootstrap handed off a key")
		return nil
	})
	if !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("duplicate name error = %v, want AlreadyExists", err)
	}
	var keys int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tenant_api_keys WHERE tenant_id = $1`, tenant.ID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys != 1 {
		t.Fatalf("keys after duplicate bootstrap = %d, want 1", keys)
	}
}

func tenantKeyManagementStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	store := newLAChannelTestStore(t, dsn)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func bootstrapKeyManagementTenant(t *testing.T, store *PostgresStore) (ServerKeyAccess, string) {
	t.Helper()
	var tenant Tenant
	var key string
	err := store.BootstrapTenant(t.Context(), "key-management-"+uuid.NewString(), func(created Tenant, secret string) error {
		tenant, key = created, secret
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := store.db.Exec(`DELETE FROM tenant_api_keys WHERE tenant_id = $1`, tenant.ID); err != nil {
			t.Errorf("delete test keys: %v", err)
		}
		if _, err := store.db.Exec(`DELETE FROM tenants WHERE id = $1`, tenant.ID); err != nil {
			t.Errorf("delete test tenant: %v", err)
		}
	})
	actor, err := store.AuthenticateServerKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return actor, key
}

func TestServerKeyRotationOverlapAndTenantIsolation(t *testing.T) {
	store := tenantKeyManagementStore(t)
	ctx := context.Background()
	first, firstKey := bootstrapKeyManagementTenant(t, store)
	other, otherKey := bootstrapKeyManagementTenant(t, store)
	id, replacement, err := store.IssueServerKey(ctx, first, first.Scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	access, err := store.AuthenticateServerKey(ctx, replacement)
	if err != nil || access.KeyID != id || access.TenantID != first.TenantID {
		t.Fatalf("authenticate replacement: %+v, %v", access, err)
	}
	if _, err := store.AuthenticateServerKey(ctx, firstKey); err != nil {
		t.Fatalf("old key must work during overlap: %v", err)
	}
	if err := store.RevokeServerKey(ctx, first, other.KeyID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant revocation = %v, want NotFound", err)
	}
	if _, err := store.AuthenticateServerKey(ctx, otherKey); err != nil {
		t.Fatalf("other tenant key changed: %v", err)
	}
	if err := store.RevokeServerKey(ctx, access, first.KeyID); err != nil {
		t.Fatal(err)
	}
	var revokedAt time.Time
	if err := store.db.QueryRow(`SELECT revoked_at FROM tenant_api_keys WHERE id = $1`, first.KeyID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeServerKey(ctx, access, first.KeyID); err != nil {
		t.Fatalf("repeat revocation: %v", err)
	}
	var repeatedAt time.Time
	if err := store.db.QueryRow(`SELECT revoked_at FROM tenant_api_keys WHERE id = $1`, first.KeyID).Scan(&repeatedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Equal(repeatedAt) {
		t.Fatal("repeat revocation changed original revocation time")
	}
	if _, err := store.AuthenticateServerKey(ctx, firstKey); !errors.Is(err, ErrInvalidServerKey) {
		t.Fatalf("old key after revoke = %v", err)
	}
	if _, err := store.AuthenticateServerKey(ctx, replacement); err != nil {
		t.Fatalf("replacement after revocation: %v", err)
	}
	forged := access
	forged.TenantID = other.TenantID
	if _, _, err := store.IssueServerKey(ctx, forged, []string{"read"}, nil); !errors.Is(err, ErrInvalidServerKey) {
		t.Fatalf("mismatched actor tenant = %v", err)
	}
	_, secretText, _ := strings.Cut(replacement, ".")
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(secret)
	var storedHash []byte
	if err := store.db.QueryRow(`SELECT secret_hash FROM tenant_api_keys WHERE id = $1`, id).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedHash, hash[:]) {
		t.Fatal("issued key digest does not match secret")
	}
}

func TestServerKeyManagementRevalidatesAuthorizationAndBoundsDelegation(t *testing.T) {
	store := tenantKeyManagementStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		update       string
		scopes       []string
		expiryOffset time.Duration
		want         error
	}{
		{name: "empty scopes", want: ErrInvalidKeyRequest},
		{name: "duplicate scopes", scopes: []string{"read", "read"}, want: ErrInvalidKeyRequest},
		{name: "unknown scope", scopes: []string{"admin"}, want: ErrInvalidKeyRequest},
		{name: "expired request", scopes: []string{"read"}, expiryOffset: -time.Hour, want: ErrInvalidKeyRequest},
		{name: "scope escalation", update: `UPDATE tenant_api_keys SET scopes = ARRAY['keys:manage', 'read'] WHERE id = $1`, scopes: []string{"send"}, want: ErrInsufficientKeyScope},
		{name: "missing management scope", update: `UPDATE tenant_api_keys SET scopes = ARRAY['read'] WHERE id = $1`, scopes: []string{"read"}, want: ErrInsufficientKeyScope},
		{name: "unbounded expiry escalation", update: `UPDATE tenant_api_keys SET expires_at = clock_timestamp() + interval '1 hour' WHERE id = $1`, scopes: []string{"read"}, want: ErrInsufficientKeyScope},
		{name: "later expiry escalation", update: `UPDATE tenant_api_keys SET expires_at = clock_timestamp() + interval '1 hour' WHERE id = $1`, scopes: []string{"read"}, expiryOffset: 2 * time.Hour, want: ErrInsufficientKeyScope},
		{name: "revoked actor", update: `UPDATE tenant_api_keys SET revoked_at = clock_timestamp() WHERE id = $1`, scopes: []string{"read"}, want: ErrInvalidServerKey},
		{name: "expired actor", update: `UPDATE tenant_api_keys SET created_at = clock_timestamp() - interval '2 hours', expires_at = clock_timestamp() - interval '1 hour' WHERE id = $1`, scopes: []string{"read"}, want: ErrInvalidServerKey},
		{name: "disabled tenant", update: `UPDATE tenants SET disabled_at = clock_timestamp() WHERE id = (SELECT tenant_id FROM tenant_api_keys WHERE id = $1)`, scopes: []string{"read"}, want: ErrInvalidServerKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, _ := bootstrapKeyManagementTenant(t, store)
			if tc.update != "" {
				if _, err := store.db.Exec(tc.update, actor.KeyID); err != nil {
					t.Fatal(err)
				}
			}
			var expiry *time.Time
			if tc.expiryOffset != 0 {
				value := time.Now().Add(tc.expiryOffset)
				expiry = &value
			}
			id, key, err := store.IssueServerKey(ctx, actor, tc.scopes, expiry)
			if !errors.Is(err, tc.want) || id != uuid.Nil || key != "" {
				t.Fatalf("issue error = %v, want %v, credential returned = %v", err, tc.want, id != uuid.Nil || key != "")
			}
			var count int
			if err := store.db.QueryRow(`SELECT count(*) FROM tenant_api_keys WHERE tenant_id = $1`, actor.TenantID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("failed issuance persisted %d keys, want 1", count)
			}
			if tc.want == ErrInvalidServerKey || tc.name == "missing management scope" {
				if err := store.RevokeServerKey(ctx, actor, actor.KeyID); !errors.Is(err, tc.want) {
					t.Fatalf("revoke with stale actor = %v, want %v", err, tc.want)
				}
			}
		})
	}
	actor, _ := bootstrapKeyManagementTenant(t, store)
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := store.db.Exec(`UPDATE tenant_api_keys SET expires_at = $2, scopes = ARRAY['keys:manage', 'read'] WHERE id = $1`, actor.KeyID, expires); err != nil {
		t.Fatal(err)
	}
	id, key, err := store.IssueServerKey(ctx, actor, []string{"keys:manage", "read"}, &expires)
	if err != nil {
		t.Fatalf("bounded delegation: %v", err)
	}
	var gotExpiry time.Time
	if err := store.db.QueryRow(`SELECT expires_at FROM tenant_api_keys WHERE id = $1`, id).Scan(&gotExpiry); err != nil {
		t.Fatal(err)
	}
	if !gotExpiry.Equal(expires) {
		t.Fatalf("expiry = %v, want %v", gotExpiry, expires)
	}
	limited, err := store.AuthenticateServerKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.IssueServerKey(ctx, limited, []string{"send"}, &expires); !errors.Is(err, ErrInsufficientKeyScope) {
		t.Fatalf("delegated escalation = %v", err)
	}
	if err := store.RevokeServerKey(ctx, actor, uuid.New()); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("missing target = %v", err)
	}
}

func TestServerKeyConcurrentRevocationAndIssuance(t *testing.T) {
	store := tenantKeyManagementStore(t)
	for i := 0; i < 8; i++ {
		actor, _ := bootstrapKeyManagementTenant(t, store)
		_, replacement, err := store.IssueServerKey(context.Background(), actor, actor.Scopes, nil)
		if err != nil {
			t.Fatal(err)
		}
		replacementActor, err := store.AuthenticateServerKey(context.Background(), replacement)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- store.RevokeServerKey(ctx, actor, replacementActor.KeyID) }()
		go func() { <-start; results <- store.RevokeServerKey(ctx, replacementActor, actor.KeyID) }()
		close(start)
		one, two := <-results, <-results
		cancel()
		if !(one == nil && errors.Is(two, ErrInvalidServerKey) || two == nil && errors.Is(one, ErrInvalidServerKey)) {
			t.Fatalf("reciprocal revocation: %v, %v", one, two)
		}
	}
	actor, _ := bootstrapKeyManagementTenant(t, store)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tenant_api_keys SET revoked_at = clock_timestamp() WHERE id = $1`, actor.KeyID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := store.IssueServerKey(ctx, actor, []string{"read"}, nil)
		result <- err
	}()
	<-started
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrInvalidServerKey) {
		t.Fatalf("issuance racing with committed revocation = %v", err)
	}
}
