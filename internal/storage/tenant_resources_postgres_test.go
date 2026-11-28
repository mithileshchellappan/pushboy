package storage

import (
	"context"
	"errors"
	"strconv"
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
	firstUser, err := store.CreateTenantUser(ctx, first, &identity, nil)
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
		if _, err := store.CreateTenantUser(ctx, first, &variant, nil); err != nil {
			t.Fatalf("case-sensitive identity creation: %v", err)
		}
	}
	codePoints := make([]rune, 128+512)
	for i := range codePoints {
		codePoints[i] = rune(0x10000 + i*104729%0x100000)
	}
	longIdentity := TenantUserIdentity{Namespace: string(codePoints[:128]), ExternalID: string(codePoints[128:])}
	if _, err := store.CreateTenantUser(ctx, first, &longIdentity, nil); err != nil {
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
		_, results[i] = store.CreateTenantUser(ctx, tenantID, &identity, nil)
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

func TestTenantUserCreationReplay(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	first := createTenantResourceTestTenant(t, store)
	second := createTenantResourceTestTenant(t, store)
	identity := TenantUserIdentity{Namespace: "account:42", ExternalID: " Person "}
	requestID := uuid.New()
	user, err := store.CreateTenantUser(ctx, first, &identity, &requestID)
	if err != nil || user.UID == uuid.Nil || user.Kind != "identified" {
		t.Fatalf("create identified user = %+v, %v", user, err)
	}
	if got, err := store.CreateTenantUser(ctx, first, &identity, &requestID); err != nil || got != user {
		t.Fatalf("identified replay = %+v, %v, want %+v", got, err, user)
	}
	for _, conflictingIdentity := range []*TenantUserIdentity{
		nil,
		{Namespace: "account", ExternalID: "42: Person "},
		{Namespace: "account:42", ExternalID: " person "},
		{Namespace: "account:42", ExternalID: "Person"},
	} {
		if _, err := store.CreateTenantUser(ctx, first, conflictingIdentity, &requestID); !errors.Is(err, Errors.AlreadyExists) {
			t.Fatalf("replay with conflicting identity %+v = %v, want AlreadyExists", conflictingIdentity, err)
		}
	}
	other, err := store.CreateTenantUser(ctx, second, &identity, &requestID)
	if err != nil || other.UID == uuid.Nil || other.UID == user.UID || other.Kind != user.Kind {
		t.Fatalf("same key in second tenant = %+v, %v", other, err)
	}

	anonymousRequestID := uuid.New()
	if _, err := store.CreateTenantUser(ctx, first, &identity, &anonymousRequestID); !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("claim existing identity with a new key = %v, want AlreadyExists", err)
	}
	anonymous, err := store.CreateTenantUser(ctx, first, nil, &anonymousRequestID)
	if err != nil || anonymous.UID == uuid.Nil || anonymous.UID == user.UID || anonymous.Kind != "anonymous" {
		t.Fatalf("create anonymous user with reusable key = %+v, %v", anonymous, err)
	}
	if got, err := store.CreateTenantUser(ctx, first, nil, &anonymousRequestID); err != nil || got != anonymous {
		t.Fatalf("anonymous replay = %+v, %v, want %+v", got, err, anonymous)
	}
	if _, err := store.CreateTenantUser(ctx, first, &identity, &anonymousRequestID); !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("replay anonymous request with an identity = %v, want AlreadyExists", err)
	}
	for _, query := range []string{
		`UPDATE tenant_user_identities SET external_id = 'new external ID' WHERE tenant_id = $1 AND user_uid = $2`,
		`UPDATE tenant_users SET kind = 'anonymous' WHERE tenant_id = $1 AND uid = $2`,
	} {
		if _, err := store.db.ExecContext(ctx, query, first, user.UID); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.CreateTenantUser(ctx, first, &identity, &requestID); err != nil || got != user {
		t.Fatalf("replay after user changes = %+v, %v, want original %+v", got, err, user)
	}
}

func TestTenantUserCreationReplayWhenConflictingRowDisappears(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant := createTenantResourceTestTenant(t, store)
	requestID := uuid.New()
	identity := TenantUserIdentity{Namespace: "account", ExternalID: "account-42"}
	user, err := store.CreateTenantUser(ctx, tenant, &identity, &requestID)
	if err != nil {
		t.Fatal(err)
	}

	var backendPID int
	if err := store.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatal(err)
	}
	name := "delete_replayed_user_" + strings.ReplaceAll(user.UID.String(), "-", "")
	_, err = store.db.ExecContext(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			DELETE FROM tenant_users WHERE tenant_id = '`+tenant.String()+`' AND uid = '`+user.UID.String()+`';
			RETURN NULL;
		END $$;
		CREATE TRIGGER `+name+` AFTER INSERT ON tenant_users FOR EACH STATEMENT
		WHEN (pg_backend_pid() = `+strconv.Itoa(backendPID)+`) EXECUTE FUNCTION `+name+`()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := store.db.Exec(`DROP FUNCTION ` + name + `() CASCADE`); err != nil {
			t.Error(err)
		}
	})

	replacement, err := store.CreateTenantUser(ctx, tenant, &identity, &requestID)
	if err != nil || replacement.UID == uuid.Nil || replacement.UID == user.UID {
		t.Fatalf("replay after deletion returned %+v, %v", replacement, err)
	}
	var identityUID uuid.UUID
	err = store.db.QueryRowContext(ctx, `
		SELECT user_uid FROM tenant_user_identities WHERE tenant_id = $1 AND namespace = $2 AND external_id = $3`,
		tenant, identity.Namespace, identity.ExternalID).Scan(&identityUID)
	if err != nil || identityUID != replacement.UID {
		t.Fatalf("identity after replay = %s, %v, want %s", identityUID, err, replacement.UID)
	}
}

func TestConcurrentTenantUserCreationReplaysSameRequest(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenantID := createTenantResourceTestTenant(t, store)
	identity := TenantUserIdentity{Namespace: "account", ExternalID: "account-42"}
	requestID := uuid.New()
	const callers = 8
	users := make([]TenantUser, callers)
	errs := make([]error, callers)
	runLAChannelConcurrent(callers, func(i int) {
		users[i], errs[i] = store.CreateTenantUser(ctx, tenantID, &identity, &requestID)
	})
	for i, err := range errs {
		if err != nil || users[i].UID == uuid.Nil || users[i].Kind != "identified" || users[i] != users[0] {
			t.Fatalf("create attempt %d = %+v, %v, want same identified user across attempts", i, users[i], err)
		}
	}
	var userCount, identityCount int
	err := store.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM tenant_users WHERE tenant_id = $1),
		       (SELECT count(*) FROM tenant_user_identities WHERE tenant_id = $1)`, tenantID,
	).Scan(&userCount, &identityCount)
	if err != nil || userCount != 1 || identityCount != 1 {
		t.Fatalf("stored users = %d, identities = %d, err = %v, want 1 of each", userCount, identityCount, err)
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
		if _, err := store.CreateTenantUser(ctx, tenantID, &identity, nil); !errors.Is(err, ErrInvalidTenantResource) {
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
