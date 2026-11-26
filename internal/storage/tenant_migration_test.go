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
	if err := m.Migrate(17); err != nil {
		t.Fatalf("apply migration 17: %v", err)
	}

	db, err := sql.Open("postgres", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &PostgresStore{db: db}

	var key string
	if err := store.BootstrapTenant(ctx, "Rollback test", func(_ Tenant, secret string) error {
		key = secret
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = m.Steps(-1)
	var migrationErr database.Error
	var postgresErr *pq.Error
	if !errors.As(err, &migrationErr) || !errors.As(migrationErr.OrigErr, &postgresErr) || postgresErr.Code != "55000" {
		t.Fatalf("rollback with tenant data = %v", err)
	}
	if _, err := store.AuthenticateServerKey(ctx, key); err != nil {
		t.Fatalf("bootstrap key after blocked rollback: %v", err)
	}
	if version, dirty, err := m.Version(); err != nil || version != 16 || !dirty {
		t.Fatalf("version after blocked rollback = %d, dirty = %t, error = %v", version, dirty, err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM tenant_api_keys; DELETE FROM tenants"); err != nil {
		t.Fatalf("empty test tenant tables: %v", err)
	}
	if err := m.Force(17); err != nil {
		t.Fatalf("restore recorded migration version: %v", err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("rollback with empty tenant tables: %v", err)
	}
	if version, dirty, err := m.Version(); err != nil || version != 16 || dirty {
		t.Fatalf("version after empty rollback = %d, dirty = %t, error = %v", version, dirty, err)
	}
	var tablesRemain bool
	err = db.QueryRowContext(ctx, `
		SELECT to_regclass('tenants') IS NOT NULL OR to_regclass('tenant_api_keys') IS NOT NULL
	`).Scan(&tablesRemain)
	if err != nil || tablesRemain {
		t.Fatalf("tenant tables after empty rollback = %t, error = %v", tablesRemain, err)
	}
}
