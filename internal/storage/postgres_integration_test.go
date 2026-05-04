package storage

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func newTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("change to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatalf("create postgres store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	_, err = store.db.ExecContext(
		context.Background(),
		`TRUNCATE delivery_receipts, publish_jobs, user_topic_subscriptions, tokens, topics, users RESTART IDENTITY CASCADE`,
	)
	if err != nil {
		t.Fatalf("clean postgres test database: %v", err)
	}

	return store
}

func TestPostgresNotificationEligibility(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-2 * time.Hour)
	payload := &model.NotificationPayload{Title: "test", Body: "body"}

	if _, err := store.CreateUser(ctx, &User{ID: "user-1"}); err != nil {
		t.Fatalf("create user-1: %v", err)
	}
	if _, err := store.CreateUser(ctx, &User{ID: "user-2"}); err != nil {
		t.Fatalf("create user-2: %v", err)
	}
	if err := store.CreateTopic(ctx, &Topic{ID: "topic-1", Name: "Topic 1"}); err != nil {
		t.Fatalf("create topic: %v", err)
	}

	oldTopicJob := &PublishJob{
		ID:           "job-old-topic",
		TopicID:      "topic-1",
		Payload:      payload,
		Status:       "COMPLETED",
		CreatedAt:    base.Format(time.RFC3339),
		TotalCount:   1,
		SuccessCount: 1,
	}
	if _, err := store.CreatePublishJob(ctx, oldTopicJob); err != nil {
		t.Fatalf("create old topic job: %v", err)
	}

	if _, err := store.SubscribeUserToTopic(ctx, &UserTopicSubscription{UserID: "user-1", TopicID: "topic-1"}); err != nil {
		t.Fatalf("subscribe user: %v", err)
	}

	directJob := &PublishJob{
		ID:        "job-direct",
		UserID:    "user-1",
		Payload:   payload,
		Status:    "QUEUED",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := store.CreateUserPublishJob(ctx, directJob); err != nil {
		t.Fatalf("create direct job: %v", err)
	}

	scheduledJob := &PublishJob{
		ID:          "job-scheduled-topic",
		TopicID:     "topic-1",
		Payload:     payload,
		Status:      "SCHEDULED",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		ScheduledAt: time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
	}
	if _, err := store.CreatePublishJob(ctx, scheduledJob); err != nil {
		t.Fatalf("create scheduled topic job: %v", err)
	}

	jobs, err := store.ListUserNotifications(ctx, "user-1", NotificationListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list user notifications: %v", err)
	}

	seen := map[string]bool{}
	for _, job := range jobs {
		seen[job.ID] = true
	}
	if !seen["job-direct"] {
		t.Fatalf("direct user job was not returned: %+v", jobs)
	}
	if !seen["job-scheduled-topic"] {
		t.Fatalf("eligible scheduled topic job was not returned: %+v", jobs)
	}
	if seen["job-old-topic"] {
		t.Fatalf("topic job before subscription should not be returned: %+v", jobs)
	}

	user2Jobs, err := store.ListUserNotifications(ctx, "user-2", NotificationListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list unsubscribed user notifications: %v", err)
	}
	if len(user2Jobs) != 0 {
		t.Fatalf("unsubscribed user should not see topic jobs: %+v", user2Jobs)
	}

	scheduledOnly, err := store.ListUserNotifications(ctx, "user-1", NotificationListQuery{
		Limit:  10,
		Status: "SCHEDULED",
	})
	if err != nil {
		t.Fatalf("list scheduled notifications: %v", err)
	}
	if len(scheduledOnly) != 1 || scheduledOnly[0].ID != "job-scheduled-topic" {
		t.Fatalf("unexpected scheduled filter result: %+v", scheduledOnly)
	}
}

func TestPostgresTimestampColumnsAreTimestamptz(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	expected := map[string]string{
		"users.created_at":                    "timestamp with time zone",
		"tokens.created_at":                   "timestamp with time zone",
		"user_topic_subscriptions.created_at": "timestamp with time zone",
		"publish_jobs.created_at":             "timestamp with time zone",
		"delivery_receipts.dispatched_at":     "timestamp with time zone",
	}

	rows, err := store.db.QueryContext(
		ctx,
		`SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'public'
			AND (table_name, column_name) IN (
				('users', 'created_at'),
				('tokens', 'created_at'),
				('user_topic_subscriptions', 'created_at'),
				('publish_jobs', 'created_at'),
				('delivery_receipts', 'dispatched_at')
			)`,
	)
	if err != nil {
		t.Fatalf("query timestamp columns: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tableName string
		var columnName string
		var dataType string
		if err := rows.Scan(&tableName, &columnName, &dataType); err != nil {
			t.Fatalf("scan timestamp column: %v", err)
		}
		key := tableName + "." + columnName
		if expected[key] != dataType {
			t.Fatalf("%s: expected %q, got %q", key, expected[key], dataType)
		}
		delete(expected, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("timestamp column rows: %v", err)
	}
	if len(expected) != 0 {
		t.Fatalf("missing timestamp columns: %+v", expected)
	}
}
