package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

func TestPostgresTenantMigrationRollback(t *testing.T) {
	dsn := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	databaseURL, err := url.Parse(dsn)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") {
		t.Fatal("PUSHBOY_TEST_DATABASE_URL must be a PostgreSQL URL")
	}

	ctx := t.Context()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := pq.QuoteIdentifier("tenant_rollback_" + uuid.NewString())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	params := databaseURL.Query()
	params.Set("search_path", schema)
	databaseURL.RawQuery = params.Encode()

	migrationDir, err := filepath.Abs("../../db/migrations/postgres")
	if err != nil {
		t.Fatal(err)
	}
	sourceURL := url.URL{Scheme: "file", Path: migrationDir}
	m, err := migrate.New(sourceURL.String(), databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sourceErr, databaseErr := m.Close(); sourceErr != nil || databaseErr != nil {
			t.Errorf("close migration runner: %v", errors.Join(sourceErr, databaseErr))
		}
	})
	if err := m.Migrate(18); err != nil {
		t.Fatalf("apply migration 18: %v", err)
	}

	db, err := sql.Open("postgres", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &PostgresStore{db: db}

	rollBack := func(from uint, blocked bool) {
		t.Helper()
		err := m.Steps(-1)
		if blocked {
			var migrationErr database.Error
			var postgresErr *pq.Error
			if !errors.As(err, &migrationErr) || !errors.As(migrationErr.OrigErr, &postgresErr) || postgresErr.Code != "55000" {
				t.Fatalf("rollback from %d with tenant data = %v", from, err)
			}
		} else if err != nil {
			t.Fatalf("rollback from %d with empty tables: %v", from, err)
		}
		if version, dirty, err := m.Version(); err != nil || version != from-1 || dirty != blocked {
			t.Fatalf("rollback from %d left version %d, dirty %t, error %v", from, version, dirty, err)
		}
	}

	var tenant Tenant
	var key string
	if err := store.BootstrapTenant(ctx, "Rollback test", func(created Tenant, secret string) error {
		tenant, key = created, secret
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	priorUID := uuid.New()
	if _, err := db.ExecContext(ctx, `INSERT INTO tenant_users (tenant_id, uid, kind) VALUES ($1, $2, 'identified')`, tenant.ID, priorUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO tenant_user_identities (tenant_id, user_uid, namespace, external_id)
		VALUES ($1, $2, 'accounts', 'account-42')`, tenant.ID, priorUID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTenantTopic(ctx, tenant.ID, "updates", "Updates"); err != nil {
		t.Fatal(err)
	}

	if err := m.Migrate(19); err != nil {
		t.Fatalf("upgrade from migration 18 with existing tenant resources: %v", err)
	}
	var metadataAbsent bool
	err = db.QueryRowContext(ctx, `
		SELECT creation_request_id IS NULL AND creation_request_digest IS NULL
		FROM tenant_users WHERE tenant_id = $1 AND uid = $2`, tenant.ID, priorUID,
	).Scan(&metadataAbsent)
	if err != nil || !metadataAbsent {
		t.Fatalf("request metadata absent from pre-upgrade user = %t, %v", metadataAbsent, err)
	}
	rollBack(19, false)
	if err := m.Migrate(19); err != nil {
		t.Fatalf("reapply migration 19: %v", err)
	}
	requestID := uuid.New()
	user, err := store.CreateTenantUser(ctx, tenant.ID, nil, &requestID)
	if err != nil {
		t.Fatal(err)
	}
	rollBack(19, true)
	if replay, err := store.CreateTenantUser(ctx, tenant.ID, nil, &requestID); err != nil || replay != user {
		t.Fatalf("replay after blocked rollback = %+v, %v, want %+v", replay, err, user)
	}
	if err := m.Force(19); err != nil {
		t.Fatalf("restore recorded migration version 19: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM tenant_users WHERE tenant_id = $1 AND uid = $2`, tenant.ID, user.UID); err != nil {
		t.Fatal(err)
	}
	rollBack(19, false)
	rollBack(18, true)
	var rowsRemain bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tenant_users u
			JOIN tenant_user_identities i ON i.tenant_id = u.tenant_id AND i.user_uid = u.uid
			WHERE u.tenant_id = $1 AND u.uid = $2 AND u.kind = 'identified'
				AND i.namespace = 'accounts' AND i.external_id = 'account-42'
		) AND EXISTS (
			SELECT 1 FROM tenant_topics WHERE tenant_id = $1 AND id = 'updates' AND name = 'Updates'
		)
	`, tenant.ID, priorUID).Scan(&rowsRemain)
	if err != nil || !rowsRemain {
		t.Fatalf("tenant rows after blocked rollback = %t, error = %v", rowsRemain, err)
	}
	if err := m.Force(18); err != nil {
		t.Fatalf("restore recorded migration version 18: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM tenant_topics; DELETE FROM tenant_users"); err != nil {
		t.Fatalf("empty test resource tables: %v", err)
	}
	rollBack(18, false)
	var tablesRemain bool
	err = db.QueryRowContext(ctx, `
		SELECT to_regclass('tenant_users') IS NOT NULL OR to_regclass('tenant_user_identities') IS NOT NULL
			OR to_regclass('tenant_topics') IS NOT NULL
	`).Scan(&tablesRemain)
	if err != nil || tablesRemain {
		t.Fatalf("tenant resource tables after empty rollback = %t, error = %v", tablesRemain, err)
	}

	rollBack(17, true)
	if _, err := store.AuthenticateServerKey(ctx, key); err != nil {
		t.Fatalf("bootstrap key after blocked rollback: %v", err)
	}
	if err := m.Force(17); err != nil {
		t.Fatalf("restore recorded migration version 17: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM tenant_api_keys; DELETE FROM tenants"); err != nil {
		t.Fatalf("empty test tenant tables: %v", err)
	}
	rollBack(17, false)
	err = db.QueryRowContext(ctx, `
		SELECT to_regclass('tenants') IS NOT NULL OR to_regclass('tenant_api_keys') IS NOT NULL
	`).Scan(&tablesRemain)
	if err != nil || tablesRemain {
		t.Fatalf("tenant tables after empty rollback = %t, error = %v", tablesRemain, err)
	}
}
