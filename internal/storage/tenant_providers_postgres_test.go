package storage

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/google/uuid"
)

const testProviderKeys = `{"old":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","new":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="}`

func testAPNsCredentials(t *testing.T, curve elliptic.Curve) json.RawMessage {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := json.Marshal(apnsCredentials{
		KeyID: "A1B2C3D4E5", TeamID: "Z1Y2X3W4V5",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func testFCMCredentials(t *testing.T) fcmCredentials {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return fcmCredentials{
		Type: "service_account", ProjectID: "example-project", PrivateKeyID: "synthetic-key",
		PrivateKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		ClientEmail: "pushboy@example-project.iam.gserviceaccount.com", TokenURI: googleServiceAccountTokenURL,
	}
}

func TestTenantProviderOfflineValidation(t *testing.T) {
	ctx := t.Context()
	apns := TenantProviderNamespace{"apns", "sandbox", "com.example.app"}
	if _, err := validateProviderCredentials(ctx, apns, testAPNsCredentials(t, elliptic.P256())); err != nil {
		t.Fatal(err)
	}
	if _, err := validateProviderCredentials(ctx, apns, testAPNsCredentials(t, elliptic.P384())); !errors.Is(err, ErrInvalidTenantResource) {
		t.Fatalf("APNs with a non-P-256 key: %v", err)
	}
	valid := testFCMCredentials(t)
	for _, tc := range []struct {
		name   string
		change func(*fcmCredentials)
		valid  bool
	}{
		{"service account", func(*fcmCredentials) {}, true},
		{"different sender project", func(c *fcmCredentials) { c.ProjectID = "different-project" }, true},
		{"invalid sender project", func(c *fcmCredentials) { c.ProjectID = "../invalid-project" }, false},
		{"alternate endpoint", func(c *fcmCredentials) { c.TokenURI = "http://localhost/token" }, false},
		{"external account", func(c *fcmCredentials) { c.Type = "external_account" }, false},
		{"unparseable key", func(c *fcmCredentials) { c.PrivateKey = "invalid" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.change(&c)
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validateProviderCredentials(ctx, TenantProviderNamespace{"fcm", "production", valid.ProjectID}, raw)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrInvalidTenantResource) {
				t.Fatalf("valid %t, error %v", tc.valid, err)
			}
		})
	}
}

func TestPostgresTenantProviderEncryptionAndRotation(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	first := createTenantResourceTestTenant(t, store)
	second := createTenantResourceTestTenant(t, store)
	t.Cleanup(func() {
		if _, err := store.db.Exec(`DELETE FROM provider_configs WHERE tenant_id IN ($1, $2)`, first, second); err != nil {
			t.Error(err)
		}
	})
	providers, err := NewTenantProviderStore(store, "old", testProviderKeys)
	if err != nil {
		t.Fatal(err)
	}
	rotating, err := NewTenantProviderStore(store, "new", testProviderKeys)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewTenantProviderStore(store, "new", `{"new":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="}`)
	if err != nil {
		t.Fatal(err)
	}
	namespace := TenantProviderNamespace{"apns", "sandbox", "com.example.app"}
	credentials := testAPNsCredentials(t, elliptic.P256())
	firstRoute, err := providers.Create(ctx, first, namespace, true, credentials)
	if err != nil || !firstRoute.Ready {
		t.Fatalf("create first route: %+v, %v", firstRoute, err)
	}
	otherRoute, err := providers.Create(ctx, second, namespace, true, credentials)
	if err != nil || firstRoute.ID == otherRoute.ID {
		t.Fatalf("create independent route: %+v, %v", otherRoute, err)
	}
	if _, err := providers.Get(ctx, second, firstRoute.ID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if _, err := providers.Update(ctx, second, firstRoute.ID, 1, nil, credentials); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant rotation: %v", err)
	}
	if _, err := providers.Create(ctx, first, namespace, true, credentials); !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("duplicate route: %v", err)
	}

	readEncrypted := func(tenantID, routeID uuid.UUID) encryptedProviderCredentials {
		t.Helper()
		var encrypted encryptedProviderCredentials
		if err := store.db.QueryRow(`
			SELECT credential_key_id, credential_nonce, credential_ciphertext
			FROM provider_configs WHERE tenant_id = $1 AND id = $2`, tenantID, routeID,
		).Scan(&encrypted.keyID, &encrypted.nonce, &encrypted.ciphertext); err != nil {
			t.Fatal(err)
		}
		return encrypted
	}
	old := readEncrypted(first, firstRoute.ID)
	other := readEncrypted(second, otherRoute.ID)
	if old.keyID != "old" || bytes.Equal(old.nonce, other.nonce) || bytes.Contains(old.ciphertext, []byte("PRIVATE KEY")) ||
		bytes.Contains(old.ciphertext, []byte("A1B2C3D4E5")) {
		t.Fatal("expected distinct nonces and ciphertext on disk")
	}
	if ready, err := restored.Get(ctx, first, firstRoute.ID); err != nil || ready.Ready || ready.ReadinessReason != "encryption_key_unavailable" {
		t.Fatalf("restore with a missing old key: %+v, %v", ready, err)
	}
	fcmCreds := testFCMCredentials(t)
	fcmJSON, err := json.Marshal(fcmCreds)
	if err != nil {
		t.Fatal(err)
	}
	fcmRoute, err := rotating.Create(ctx, first, TenantProviderNamespace{"fcm", "production", "example-target"}, true, fcmJSON)
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := restored.List(ctx, first)
	if err != nil || len(mixed) != 2 || mixed[0].ID != firstRoute.ID || mixed[0].Ready || mixed[1].ID != fcmRoute.ID || !mixed[1].Ready || mixed[1].ApplicationID != "example-target" {
		t.Fatalf("unavailable APNs key alongside independently ready FCM: %+v, %v", mixed, err)
	}

	if ready, err := rotating.Get(ctx, first, firstRoute.ID); err != nil || !ready.Ready {
		t.Fatalf("read old route with staged keyring: %+v, %v", ready, err)
	}
	if _, err := store.db.Exec(`
		UPDATE provider_configs SET credential_nonce = $3, credential_ciphertext = $4
		WHERE tenant_id = $1 AND id = $2`, second, otherRoute.ID, old.nonce, old.ciphertext,
	); err != nil {
		t.Fatal(err)
	}
	if ready, err := providers.Get(ctx, second, otherRoute.ID); err != nil || ready.Ready || ready.ReadinessReason != "credential_unreadable" {
		t.Fatalf("substituted ciphertext: %+v, %v", ready, err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := rotating.Update(ctx, first, firstRoute.ID, 1, nil, credentials)
			results <- err
		}()
	}
	a, b := <-results, <-results
	if !(a == nil && errors.Is(b, ErrProviderVersionConflict) || b == nil && errors.Is(a, ErrProviderVersionConflict)) {
		t.Fatalf("concurrent rotation errors = %v, %v", a, b)
	}
	current := readEncrypted(first, firstRoute.ID)
	if current.keyID != "new" || bytes.Equal(old.nonce, current.nonce) || bytes.Equal(old.ciphertext, current.ciphertext) {
		t.Fatal("rotation did not use the new key and a fresh nonce")
	}
	routes, err := restored.List(ctx, first)
	if err != nil || len(routes) != 2 || routes[0].ID != firstRoute.ID || routes[0].CredentialVersion != 2 || !routes[0].Ready {
		t.Fatalf("read rotated route with only the new key: %+v, %v", routes, err)
	}
}
