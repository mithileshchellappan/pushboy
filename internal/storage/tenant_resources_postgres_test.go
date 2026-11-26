package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

func createTenantResourceTestTenant(t *testing.T, store *PostgresStore) uuid.UUID {
	t.Helper()
	tenantID := uuid.New()
	if _, err := store.db.Exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenantID, "resources-"+tenantID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM tenant_topics WHERE tenant_id = $1`,
			`DELETE FROM tenant_users WHERE tenant_id = $1`,
			`DELETE FROM tenants WHERE id = $1`,
		} {
			if _, err := store.db.Exec(query, tenantID); err != nil {
				t.Error(err)
			}
		}
	})
	return tenantID
}

func TestTenantResourceIsolationAndLegacyCoexistence(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	first := createTenantResourceTestTenant(t, store)
	second := createTenantResourceTestTenant(t, store)

	legacyUserID := uuid.New()
	if _, err := store.CreateUser(ctx, &User{ID: legacyUserID.String()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.DeleteUser(context.Background(), legacyUserID.String()); err != nil {
			t.Error(err)
		}
	})
	legacyTopic := Topic{ID: uuid.NewString(), Name: "legacy " + legacyUserID.String()}
	if err := store.CreateTopic(ctx, &legacyTopic); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.DeleteTopic(context.Background(), legacyTopic.ID); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.GetTenantUser(ctx, first, legacyUserID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy user lookup = %v, want NotFound", err)
	}
	if _, err := store.GetTenantTopic(ctx, first, legacyTopic.ID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy topic lookup = %v, want NotFound", err)
	}

	identity := TenantUserIdentity{Namespace: "account", ExternalID: " Account-42 "}
	firstUser, err := store.CreateTenantUser(ctx, first, &identity)
	if err != nil || firstUser.Kind != "identified" || firstUser.UID == uuid.Nil {
		t.Fatalf("identified user = %+v, err = %v", firstUser, err)
	}
	if _, err := store.GetUser(ctx, firstUser.UID.String()); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("tenant user visible through legacy lookup = %v, want NotFound", err)
	}
	var gotIdentity TenantUserIdentity
	err = store.db.QueryRow(`
		SELECT namespace, external_id FROM tenant_user_identities
		WHERE tenant_id = $1 AND user_uid = $2`, first, firstUser.UID,
	).Scan(&gotIdentity.Namespace, &gotIdentity.ExternalID)
	if err != nil || gotIdentity != identity {
		t.Fatalf("stored identity = %+v, err = %v", gotIdentity, err)
	}
	for _, variant := range []TenantUserIdentity{
		{Namespace: "account", ExternalID: " account-42 "},
		{Namespace: "Account", ExternalID: identity.ExternalID},
	} {
		if _, err := store.CreateTenantUser(ctx, first, &variant); err != nil {
			t.Fatalf("case-sensitive identity creation: %v", err)
		}
	}
	codePoints := make([]rune, 128+512)
	for i := range codePoints {
		codePoints[i] = rune(0x10000 + i*104729%0x100000)
	}
	longIdentity := TenantUserIdentity{Namespace: string(codePoints[:128]), ExternalID: string(codePoints[128:])}
	if _, err := store.CreateTenantUser(ctx, first, &longIdentity); err != nil {
		t.Fatalf("create a maximum-length identity: %v", err)
	}
	_, err = store.db.Exec(`
		INSERT INTO tenant_user_identities (tenant_id, user_uid, namespace, external_id)
		VALUES ($1, $2, 'other', 'other')`, second, firstUser.UID)
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23503" {
		t.Fatalf("cross-tenant identity reference = %v, want foreign key violation", err)
	}

	sharedTopic := TenantTopic{ID: legacyTopic.ID, Name: " Tenant updates "}
	for _, tenantID := range []uuid.UUID{first, second} {
		if _, err := store.CreateTenantTopic(ctx, tenantID, sharedTopic.ID, sharedTopic.Name); err != nil {
			t.Fatalf("create shared topic ID and name across tenants: %v", err)
		}
	}
	if _, err := store.CreateTenantTopic(ctx, first, sharedTopic.ID, "Different Name"); !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("duplicate topic ID = %v, want AlreadyExists", err)
	}
	if _, err := store.CreateTenantTopic(ctx, first, "different-id", sharedTopic.Name); !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("duplicate topic name = %v, want AlreadyExists", err)
	}

	if got, err := store.GetTopicByID(ctx, legacyTopic.ID); err != nil || *got != legacyTopic {
		t.Fatalf("legacy topic after v2 writes = %+v, err = %v", got, err)
	}
}

func TestConcurrentTenantUserCreationRollsBackDuplicateIdentities(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenantID := createTenantResourceTestTenant(t, store)
	identity := TenantUserIdentity{Namespace: "account", ExternalID: "account-42"}
	const callers = 8
	results := make([]error, callers)
	runLAChannelConcurrent(callers, func(i int) {
		_, results[i] = store.CreateTenantUser(ctx, tenantID, &identity)
	})

	var created int
	for _, err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, Errors.AlreadyExists):
		default:
			t.Fatalf("unexpected create result: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("successful creates = %d, want 1", created)
	}
	var users, identities int
	err := store.db.QueryRow(`
		SELECT (SELECT count(*) FROM tenant_users WHERE tenant_id = $1),
		       (SELECT count(*) FROM tenant_user_identities WHERE tenant_id = $1)`, tenantID,
	).Scan(&users, &identities)
	if err != nil || users != 1 || identities != 1 {
		t.Fatalf("stored users = %d, identities = %d, err = %v, want 1 of each", users, identities, err)
	}
}

func TestTenantResourceCreateRejectsInvalidFields(t *testing.T) {
	ctx := t.Context()
	store := &PostgresStore{}
	tenantID := uuid.New()
	for _, identity := range []TenantUserIdentity{
		{Namespace: "LeGaCy", ExternalID: "id"},
		{Namespace: "account", ExternalID: " \t"},
		{Namespace: "account", ExternalID: strings.Repeat("界", 513)},
		{Namespace: "account", ExternalID: "id\x00"},
	} {
		if _, err := store.CreateTenantUser(ctx, tenantID, &identity); !errors.Is(err, ErrInvalidTenantResource) {
			t.Fatalf("invalid tenant user create = %v, want ErrInvalidTenantResource", err)
		}
	}
	for _, topic := range []TenantTopic{
		{ID: "", Name: "Updates"},
		{ID: ".", Name: "Updates"},
		{ID: "..", Name: "Updates"},
		{ID: strings.Repeat("界", 129), Name: "Updates"},
		{ID: "updates", Name: strings.Repeat("界", 256)},
		{ID: "updates", Name: "Updates\x00"},
	} {
		if _, err := store.CreateTenantTopic(ctx, tenantID, topic.ID, topic.Name); !errors.Is(err, ErrInvalidTenantResource) {
			t.Fatalf("invalid tenant topic create = %v, want ErrInvalidTenantResource", err)
		}
	}
}
