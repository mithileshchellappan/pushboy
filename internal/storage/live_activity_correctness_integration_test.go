package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestLAStartRejectsDifferentScopeOrType(t *testing.T) {
	for _, scope := range []string{"user", "topic"} {
		t.Run(scope, func(t *testing.T) {
			f := newLACorrectnessFixture(t, scope)
			request := *f.job
			request.ID = uuid.NewString()
			result, err := f.store.CreateOrGetLAStartJob(context.Background(), &request, nil)
			if err != nil || result.Created || result.Job.ID != f.job.ID {
				t.Fatalf("repeat start = %+v, %v; want original job", result, err)
			}
			for _, change := range []string{"owner", "type"} {
				t.Run(change, func(t *testing.T) {
					request := *f.job
					request.ID = uuid.NewString()
					if change == "type" {
						request.ActivityType = "OtherAttributes"
					} else if scope == "user" {
						request.UserID = uuid.NewString()
					} else {
						request.TopicID = uuid.NewString()
					}
					_, err := f.store.CreateOrGetLAStartJob(context.Background(), &request, nil)
					if !errors.Is(err, Errors.Conflict) {
						t.Fatalf("different %s start error = %v, want conflict", change, err)
					}
				})
			}
		})
	}
}

func TestLAEmptyStartFailsAndReleasesScope(t *testing.T) {
	f := newLACorrectnessFixture(t, "user")
	dispatch := f.dispatch(t, model.LiveActivityActionStart)
	if err := f.store.CompleteLADispatchEnqueue(context.Background(), dispatch.ID, 0); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.GetLAJob(context.Background(), f.job.ID)
	if err != nil || job.Status != model.LiveActivityJobStatusFailed || job.ClosedAt == nil {
		t.Fatalf("empty start job = %+v, %v; want terminal FAILED", job, err)
	}
	var status string
	var completedAt sql.NullTime
	if err := f.store.db.QueryRow(`SELECT status, completed_at FROM live_activity_dispatches WHERE id = $1`, dispatch.ID).Scan(&status, &completedAt); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || !completedAt.Valid {
		t.Fatalf("empty start dispatch = %s, completed=%v; want FAILED", status, completedAt.Valid)
	}
	request := *f.job
	request.ID, request.ActivityID = uuid.NewString(), uuid.NewString()
	result, err := f.store.CreateOrGetLAStartJob(context.Background(), &request, nil)
	if err != nil || !result.Created {
		t.Fatalf("new activity after empty start = %+v, %v", result, err)
	}
}

func TestLAEmptyStartFailureIsAtomic(t *testing.T) {
	f := newLACorrectnessFixture(t, "user")
	dispatch := f.dispatch(t, model.LiveActivityActionStart)
	function := "reject_empty_start_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := f.store.db.Exec(`CREATE FUNCTION ` + function + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.id = '` + f.job.ID + `' THEN RAISE EXCEPTION 'synthetic job failure'; END IF;
		RETURN NEW;
	END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.store.db.Exec(`DROP FUNCTION ` + function + `() CASCADE`) })
	if _, err := f.store.db.Exec(`CREATE TRIGGER ` + function + ` BEFORE UPDATE ON live_activity_jobs FOR EACH ROW EXECUTE FUNCTION ` + function + `()`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CompleteLADispatchEnqueue(context.Background(), dispatch.ID, 0); err == nil {
		t.Fatal("empty start completion succeeded despite failed job transition")
	}
	var status string
	if err := f.store.db.QueryRow(`SELECT status FROM live_activity_dispatches WHERE id = $1`, dispatch.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IN_PROGRESS" {
		t.Fatalf("dispatch status after rollback = %s, want IN_PROGRESS", status)
	}
}

func TestLAEmptyStartDoesNotUseUnrelatedChannel(t *testing.T) {
	f := newLACorrectnessFixture(t, "topic")
	otherTopic := uuid.NewString()
	if err := f.store.CreateTopic(context.Background(), &Topic{ID: otherTopic, Name: otherTopic}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.store.db.Exec(`DELETE FROM live_activity_channels WHERE activity_id = $1`, f.job.ActivityID)
		_ = f.store.DeleteTopic(context.Background(), otherTopic)
	})
	if _, err := f.store.db.Exec(`INSERT INTO live_activity_channels(activity_id, topic_id, channel_id, created_at)
		VALUES($1, $2, $3, NOW())`, f.job.ActivityID, otherTopic, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	dispatch := f.dispatch(t, model.LiveActivityActionStart)
	if err := f.store.CompleteLADispatchEnqueue(context.Background(), dispatch.ID, 0); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.GetLAJob(context.Background(), f.job.ID)
	if err != nil || job.Status != model.LiveActivityJobStatusFailed {
		t.Fatalf("job with unrelated channel = %+v, %v; want FAILED", job, err)
	}
}

func TestLAEmptyStartWaitsForChannelProvisioning(t *testing.T) {
	f := newLACorrectnessFixture(t, "topic")
	dispatch := f.dispatch(t, model.LiveActivityActionStart)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	providerEntered, releaseProvider := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseProvider) })
	t.Cleanup(release)
	provisioned, completed := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, err := f.other.EnsureLAChannel(ctx, f.job.ActivityID, f.job.TopicID, func(context.Context) (string, error) {
			close(providerEntered)
			<-releaseProvider
			return uuid.NewString(), nil
		})
		provisioned <- err
	}()
	select {
	case <-providerEntered:
	case err := <-provisioned:
		t.Fatalf("provisioning stopped before calling provider: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { completed <- f.store.CompleteLADispatchEnqueue(ctx, dispatch.ID, 0) }()
	waiting, waitErr := f.harness.waitForLockWaiter(f.job.ActivityID, 2*time.Second)
	release()
	provisionErr, completionErr := <-provisioned, <-completed
	if waitErr != nil || !waiting {
		t.Fatalf("empty completion waited for channel lock = %v, error = %v", waiting, waitErr)
	}
	if provisionErr != nil || completionErr != nil {
		t.Fatalf("provisioning error = %v, completion error = %v", provisionErr, completionErr)
	}
	job, err := f.store.GetLAJob(ctx, f.job.ID)
	if err != nil || job.Status != model.LiveActivityJobStatusActive {
		t.Fatalf("job after provisioning = %+v, %v; want ACTIVE", job, err)
	}
	var status string
	if err := f.store.db.QueryRowContext(ctx, `SELECT status FROM live_activity_dispatches WHERE id = $1`, dispatch.ID).Scan(&status); err != nil || status != "COMPLETED" {
		t.Fatalf("empty channel-backed dispatch = %q, %v; want COMPLETED", status, err)
	}
}

func TestEnsureLAChannelRejectsTerminalJob(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	for _, test := range []struct {
		name      string
		status    model.LiveActivityJobStatus
		closedAt  *time.Time
		expiresAt *time.Time
	}{
		{name: "empty start failed", status: model.LiveActivityJobStatusFailed},
		{name: "closed", status: model.LiveActivityJobStatusClosed, closedAt: &past},
		{name: "expired", status: model.LiveActivityJobStatusActive, expiresAt: &past},
		{name: "closed marker", status: model.LiveActivityJobStatusActive, closedAt: &past},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newLACorrectnessFixture(t, "topic")
			if test.name == "empty start failed" {
				dispatch := f.dispatch(t, model.LiveActivityActionStart)
				if err := f.store.CompleteLADispatchEnqueue(context.Background(), dispatch.ID, 0); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.store.db.Exec(`UPDATE live_activity_jobs SET status = $1, closed_at = $2, expires_at = $3 WHERE id = $4`, test.status, test.closedAt, test.expiresAt, f.job.ID); err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, created, err := f.store.EnsureLAChannel(context.Background(), f.job.ActivityID, f.job.TopicID, func(context.Context) (string, error) {
				calls++
				return uuid.NewString(), nil
			})
			if !errors.Is(err, Errors.Conflict) || created || calls != 0 {
				t.Fatalf("terminal provisioning: created=%v, calls=%d, error=%v; want conflict without provider call", created, calls, err)
			}
		})
	}
}

func TestEnsureLAChannelRechecksExpiryAfterLockWait(t *testing.T) {
	f := newLACorrectnessFixture(t, "topic")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockLAActivity(ctx, tx, f.job.ActivityID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	calls := 0
	go func() {
		_, _, err := f.other.EnsureLAChannel(ctx, f.job.ActivityID, f.job.TopicID, func(context.Context) (string, error) {
			calls++
			return uuid.NewString(), nil
		})
		done <- err
	}()
	waiting, err := f.harness.waitForLockWaiter(f.job.ActivityID, 2*time.Second)
	if err != nil || !waiting {
		t.Fatalf("provisioning lock waiter = %v, %v", waiting, err)
	}
	// Expire after the waiter's transaction began, without waiting on wall time.
	if _, err := f.store.db.ExecContext(ctx, `UPDATE live_activity_jobs SET expires_at = statement_timestamp() WHERE id = $1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, Errors.Conflict) || calls != 0 {
		t.Fatalf("expired provisioning: calls=%d, error=%v; want conflict", calls, err)
	}
}

func TestLATokenPagerReadsOneCohortSnapshot(t *testing.T) {
	for _, scope := range []string{"user", "topic"} {
		t.Run(scope, func(t *testing.T) {
			f := newLACorrectnessFixture(t, scope)
			start := f.dispatch(t, model.LiveActivityActionStart)
			update := f.dispatch(t, model.LiveActivityActionUpdate)
			first := f.token(t, "a", model.FCM, model.LiveActivityTokenTypeUpdate, f.job.ActivityID)
			connector, err := pq.NewConnector(f.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			transitioned := false
			db := sql.OpenDB(&laSnapshotConnector{Connector: connector, afterCohortRead: func() {
				transitioned = true
				if err := f.other.UpdateLADispatchStatus(context.Background(), start.ID, "COMPLETED"); err != nil {
					t.Fatal(err)
				}
				f.token(t, "b", model.FCM, model.LiveActivityTokenTypeUpdate, "")
			}})
			t.Cleanup(func() { _ = db.Close() })
			store := &PostgresStore{db: db}
			pager, err := store.NewLATokenPager(context.Background(), update.ID)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := pager.Next(context.Background(), "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if !transitioned {
				t.Fatal("test did not interleave start completion with cohort selection")
			}
			if len(batch.Tokens) != 1 || batch.Tokens[0].ID != first.ID {
				t.Fatalf("cohort tokens = %+v; want only original associated token %s", batch.Tokens, first.ID)
			}
		})
	}
}

func TestLATokenPagerRefreshesCohortBetweenPages(t *testing.T) {
	f := newLACorrectnessFixture(t, "user")
	start := f.dispatch(t, model.LiveActivityActionStart)
	update := f.dispatch(t, model.LiveActivityActionUpdate)
	a := f.token(t, "a", model.FCM, model.LiveActivityTokenTypeUpdate, f.job.ActivityID)
	f.token(t, "b", model.FCM, model.LiveActivityTokenTypeUpdate, "")
	c := f.token(t, "c", model.APNS, model.LiveActivityTokenTypeUpdate, f.job.ActivityID)
	pager, err := f.store.NewLATokenPager(context.Background(), update.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := pager.Next(context.Background(), "", 1)
	if err != nil || !first.HasMore || len(first.Tokens) != 1 || first.Tokens[0].ID != a.ID {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if err := f.other.UpdateLADispatchStatus(context.Background(), start.ID, "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	second, err := pager.Next(context.Background(), first.NextCursor, 1)
	if err != nil || second.HasMore || len(second.Tokens) != 1 || second.Tokens[0].ID != c.ID {
		t.Fatalf("second page = %+v, %v; want only remaining associated token", second, err)
	}
}

type laCorrectnessFixture struct {
	harness     *laChannelDBHarness
	store       *PostgresStore
	other       *PostgresStore
	job         *LiveActivityJob
	userID      string
	databaseURL string
}

func newLACorrectnessFixture(t *testing.T, scope string) *laCorrectnessFixture {
	t.Helper()
	h := newLAChannelDBHarness(t)
	store := h.stores[0]
	userID := uuid.NewString()
	if _, err := store.CreateUser(context.Background(), &User{ID: userID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteUser(context.Background(), userID) })
	job := testLAStartJob(uuid.NewString(), "")
	if scope == "topic" {
		job.TopicID = h.topic()
		if _, err := store.SubscribeUserToLATopic(context.Background(), &LiveActivityUserTopicSubscription{
			ID: uuid.NewString(), UserID: userID, TopicID: job.TopicID,
		}); err != nil {
			t.Fatal(err)
		}
	} else {
		job.UserID = userID
	}
	result, err := store.CreateOrGetLAStartJob(context.Background(), job, nil)
	if err != nil || !result.Created {
		t.Fatalf("create job = %+v, %v", result, err)
	}
	t.Cleanup(func() {
		if _, err := store.db.Exec(`DELETE FROM live_activity_channels WHERE activity_id = $1`, job.ActivityID); err != nil {
			t.Errorf("delete fixture channel: %v", err)
		}
		_, _ = store.db.Exec(`DELETE FROM live_activity_jobs WHERE user_id = $1 OR topic_id = $2`, userID, nullableString(job.TopicID))
	})
	return &laCorrectnessFixture{harness: h, store: store, other: h.stores[1], job: job, userID: userID, databaseURL: os.Getenv("PUSHBOY_TEST_DATABASE_URL")}
}

func (f *laCorrectnessFixture) dispatch(t *testing.T, action model.LiveActivityAction) *LiveActivityDispatch {
	t.Helper()
	dispatch, err := f.store.CreateLADispatch(context.Background(), &LiveActivityDispatch{
		ID: uuid.NewString(), LiveActivityJobID: f.job.ID, Action: action,
		Payload: f.job.LatestPayload, Status: "IN_PROGRESS", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatch
}

func (f *laCorrectnessFixture) token(t *testing.T, suffix string, platform model.Platform, kind model.LiveActivityTokenType, activityID string) *LiveActivityToken {
	t.Helper()
	id := f.job.ID + "-" + suffix
	token, err := f.store.UpsertLiveActivityToken(context.Background(), &LiveActivityToken{
		ID: id, UserID: f.userID, Platform: platform, TokenType: kind,
		Token: "synthetic-" + id, ActivityID: activityID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// All results come from PostgreSQL; the wrapper only schedules a concurrent
// commit after a statement has acquired the cohort snapshot.
type laSnapshotConnector struct {
	driver.Connector
	afterCohortRead func()
	once            sync.Once
}

func (c *laSnapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &laSnapshotConn{Conn: conn, connector: c}, nil
}

type laSnapshotConn struct {
	driver.Conn
	connector *laSnapshotConnector
}

func (c *laSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && strings.Contains(query, "action = 'start'") {
		c.connector.once.Do(c.connector.afterCohortRead)
	}
	return rows, err
}
