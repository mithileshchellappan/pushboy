package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestPostgresLegacyPublishJob(t *testing.T) {
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	dsn, err := url.Parse(databaseURL)
	if err != nil || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") {
		t.Fatal("PUSHBOY_TEST_DATABASE_URL must be a PostgreSQL URL")
	}

	t.Chdir("../..")
	ctx := t.Context()
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	schema := pq.QuoteIdentifier("pushboy_test_" + uuid.NewString())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	params := dsn.Query()
	params.Set("search_path", schema)
	dsn.RawQuery = params.Encode()

	legacy, err := migrate.New("file://db/migrations/postgres", dsn.String())
	if err != nil {
		t.Fatalf("open legacy migrations: %v", err)
	}
	migrateErr := legacy.Migrate(1)
	sourceErr, databaseErr := legacy.Close()
	if err := errors.Join(migrateErr, sourceErr, databaseErr); err != nil {
		t.Fatalf("prepare schema at migration 1: %v", err)
	}

	const legacyTimestamp = "2026-01-02T03:04:05+02:00"
	wantTime := time.Date(2026, 1, 2, 1, 4, 5, 0, time.UTC)
	fixture, err := sql.Open("postgres", dsn.String())
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, seedErr := fixture.ExecContext(ctx, `
		INSERT INTO topics (id, name) VALUES ('legacy-topic', 'Legacy topic');
		INSERT INTO publish_jobs
			(id, topic_id, title, body, status, total_count, success_count, failure_count, created_at)
		VALUES ('legacy-job', 'legacy-topic', 'Legacy title', 'Legacy body', 'DISPATCHED', 2, 0, 1, '`+legacyTimestamp+`');
		INSERT INTO delivery_receipts
			(id, job_id, token_id, status, dispatched_at)
		VALUES ('legacy-receipt', 'legacy-job', 'legacy-token', 'FAILED', '`+legacyTimestamp+`');
	`)
	if err := errors.Join(seedErr, fixture.Close()); err != nil {
		t.Fatalf("seed legacy job and receipt: %v", err)
	}

	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("upgrade legacy database at startup: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	job, err := store.GetJobStatus(ctx, "legacy-job")
	if err != nil {
		t.Fatalf("read migrated job: %v", err)
	}
	if job.TopicID != "legacy-topic" || !job.CreatedAt.Equal(wantTime) {
		t.Fatalf("migrated job target and time = %q, %s", job.TopicID, job.CreatedAt)
	}
	if job.Payload == nil || job.Payload.Title != "Legacy title" || job.Payload.Body != "Legacy body" {
		t.Fatalf("migrated job payload = %+v", job.Payload)
	}

	// Force a late failure after the receipt and counters have been written.
	if _, err := store.db.ExecContext(ctx, `
		ALTER TABLE publish_jobs ADD CONSTRAINT reject_test_completion CHECK (status <> 'COMPLETED')
	`); err != nil {
		t.Fatalf("install completion failure: %v", err)
	}
	receipts := []model.DeliveryReceipt{{
		ID:           "new-receipt",
		JobID:        job.ID,
		TokenID:      "new-token",
		Status:       model.DeliveryStatusFailed,
		DispatchedAt: wantTime,
	}}
	var pgErr *pq.Error
	if err := store.ApplyPushOutcomeBatch(ctx, receipts); !errors.As(err, &pgErr) || pgErr.Constraint != "reject_test_completion" {
		t.Fatalf("apply batch with completion blocked: %v", err)
	}

	job, err = store.GetJobStatus(ctx, job.ID)
	if err != nil {
		t.Fatalf("read job after rollback: %v", err)
	}
	if job.Status != model.NotificationJobStatusDispatched || job.FailureCount != 1 || job.CompletedAt != nil {
		t.Fatalf("job after rollback = %s, %d failures, completion %v", job.Status, job.FailureCount, job.CompletedAt)
	}
	var legacyDispatchedAt time.Time
	if err := store.db.QueryRowContext(ctx, `
		SELECT dispatched_at FROM delivery_receipts WHERE id = 'legacy-receipt' AND job_id = 'legacy-job'
	`).Scan(&legacyDispatchedAt); err != nil || !legacyDispatchedAt.Equal(wantTime) {
		t.Fatalf("legacy receipt dispatch time = %s, error = %v", legacyDispatchedAt, err)
	}

	if _, err := store.db.ExecContext(ctx, "ALTER TABLE publish_jobs DROP CONSTRAINT reject_test_completion"); err != nil {
		t.Fatalf("remove completion failure: %v", err)
	}
	// Reusing the receipt ID also verifies that the rejected batch left no receipt behind.
	if err := store.ApplyPushOutcomeBatch(ctx, receipts); err != nil {
		t.Fatalf("retry batch: %v", err)
	}
	job, err = store.GetJobStatus(ctx, job.ID)
	if err != nil {
		t.Fatalf("read job after retry: %v", err)
	}
	if job.Status != model.NotificationJobStatusCompleted || job.FailureCount != 2 || job.CompletedAt == nil {
		t.Fatalf("job after retry = %s, %d failures, completion %v", job.Status, job.FailureCount, job.CompletedAt)
	}

	var receiptCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT count(*) FROM delivery_receipts WHERE job_id = 'legacy-job'
	`).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatalf("receipt count after retry = %d, error = %v", receiptCount, err)
	}
}
