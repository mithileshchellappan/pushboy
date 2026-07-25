package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

const liveActivityFanoutTestDriverName = "pushboy_la_fanout_test"

var (
	liveActivityFanoutScenarioID int64
	liveActivityFanoutScenarios  sync.Map
)

func init() {
	sql.Register(liveActivityFanoutTestDriverName, liveActivityFanoutDriver{})
}

func TestGetLATokenBatchForDispatchDefersAssociationFilteringWhileStartPending(t *testing.T) {
	scenario := &liveActivityFanoutScenario{
		scope: liveActivityDispatchFanoutScope{
			action:               model.LiveActivityActionUpdate,
			activityID:           "activity-1",
			userID:               "user-1",
			startDispatchPending: true,
		},
		tokenRows: liveActivityFanoutTokenRows(t),
	}
	store := newLiveActivityFanoutTestStore(t, scenario)

	batch, err := store.GetLATokenBatchForDispatch(context.Background(), "dispatch-update", "", 10)
	if err != nil {
		t.Fatalf("GetLATokenBatchForDispatch error = %v", err)
	}
	if len(batch.Tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(batch.Tokens))
	}
	if got := scenario.tokenQueryKinds(); len(got) != 1 || got[0] != "user" {
		t.Fatalf("token query kinds = %v, want [user]", got)
	}
}

func TestGetLAStartTokenBatchReturnsBroadcastChannelCapability(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	scenario := &liveActivityFanoutScenario{
		scope: liveActivityDispatchFanoutScope{
			action: model.LiveActivityActionStart,
			userID: "user-1",
		},
		tokenRows: [][]driver.Value{{
			"token-1",
			"user-1",
			string(model.APNS),
			string(model.LiveActivityTokenTypeStart),
			"push-to-start-token",
			true,
			now,
			now,
			nil,
			nil,
		}},
	}
	store := newLiveActivityFanoutTestStore(t, scenario)

	batch, err := store.GetLATokenBatchForDispatch(context.Background(), "dispatch-start", "", 10)
	if err != nil {
		t.Fatalf("GetLATokenBatchForDispatch error = %v", err)
	}
	if len(batch.Tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(batch.Tokens))
	}
	if !batch.Tokens[0].SupportsBroadcastChannels {
		t.Fatal("SupportsBroadcastChannels = false, want true")
	}
}

func TestGetLATokenBatchForDispatchRequiresAssociationAfterStartCompletes(t *testing.T) {
	scenario := &liveActivityFanoutScenario{
		scope: liveActivityDispatchFanoutScope{
			action:     model.LiveActivityActionUpdate,
			activityID: "activity-1",
			userID:     "user-1",
		},
		tokenRows: liveActivityFanoutTokenRows(t),
	}
	store := newLiveActivityFanoutTestStore(t, scenario)

	batch, err := store.GetLATokenBatchForDispatch(context.Background(), "dispatch-update", "", 10)
	if err != nil {
		t.Fatalf("GetLATokenBatchForDispatch error = %v", err)
	}
	if len(batch.Tokens) != 0 {
		t.Fatalf("tokens len = %d, want 0 for unassociated token after start completion", len(batch.Tokens))
	}
	if got := scenario.tokenQueryKinds(); len(got) != 1 || got[0] != "activity_user" {
		t.Fatalf("token query kinds = %v, want [activity_user]", got)
	}
}

func TestGetLATokenBatchForDispatchReturnsAssociatedTokensAfterStartCompletes(t *testing.T) {
	scenario := &liveActivityFanoutScenario{
		scope: liveActivityDispatchFanoutScope{
			action:     model.LiveActivityActionUpdate,
			activityID: "activity-1",
			userID:     "user-1",
		},
		tokenRows:           liveActivityFanoutTokenRows(t),
		tokenHasAssociation: true,
	}
	store := newLiveActivityFanoutTestStore(t, scenario)

	batch, err := store.GetLATokenBatchForDispatch(context.Background(), "dispatch-update", "", 10)
	if err != nil {
		t.Fatalf("GetLATokenBatchForDispatch error = %v", err)
	}
	if len(batch.Tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(batch.Tokens))
	}
	if got := scenario.tokenQueryKinds(); len(got) != 1 || got[0] != "activity_user" {
		t.Fatalf("token query kinds = %v, want [activity_user]", got)
	}
}

func TestGetLATokenBatchForDispatchKeepsTopicScopeWithAssociation(t *testing.T) {
	scenario := &liveActivityFanoutScenario{
		scope: liveActivityDispatchFanoutScope{
			action:     model.LiveActivityActionUpdate,
			activityID: "activity-1",
			topicID:    "topic-1",
		},
		tokenRows:           liveActivityFanoutTokenRows(t),
		tokenHasAssociation: true,
	}
	store := newLiveActivityFanoutTestStore(t, scenario)

	batch, err := store.GetLATokenBatchForDispatch(context.Background(), "dispatch-update", "", 10)
	if err != nil {
		t.Fatalf("GetLATokenBatchForDispatch error = %v", err)
	}
	if len(batch.Tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(batch.Tokens))
	}
	if got := scenario.tokenQueryKinds(); len(got) != 1 || got[0] != "activity_topic" {
		t.Fatalf("token query kinds = %v, want [activity_topic]", got)
	}
}

func TestSupersedeLADispatchIncludesSuccessfullyQueuedNewerDispatch(t *testing.T) {
	scenario := &liveActivityFanoutScenario{execRowsAffected: 1}
	store := newLiveActivityFanoutTestStore(t, scenario)

	superseded, err := store.SupersedeLADispatchIfStale(context.Background(), "dispatch-old", 42)
	if err != nil {
		t.Fatalf("SupersedeLADispatchIfStale error = %v", err)
	}
	if !superseded {
		t.Fatal("SupersedeLADispatchIfStale = false, want true")
	}

	query := strings.Join(strings.Fields(scenario.lastExecQuery()), " ")
	if !strings.Contains(query, "newer.status IN ('QUEUED', 'IN_PROGRESS', 'DISPATCHED', 'COMPLETED')") {
		t.Fatalf("supersede query does not include successfully queued newer dispatches: %s", query)
	}
	if strings.Contains(query, "newer.status IN ('ENQUEUE_PENDING'") {
		t.Fatalf("supersede query includes a newer dispatch before pipeline acceptance: %s", query)
	}
}

func TestMarkLADispatchEnqueuedOnlyPromotesPendingRows(t *testing.T) {
	scenario := &liveActivityFanoutScenario{execRowsAffected: 1}
	store := newLiveActivityFanoutTestStore(t, scenario)

	if err := store.MarkLADispatchEnqueued(context.Background(), "dispatch-pending"); err != nil {
		t.Fatalf("MarkLADispatchEnqueued error = %v", err)
	}

	query := strings.Join(strings.Fields(scenario.lastExecQuery()), " ")
	if !strings.Contains(query, "SET status = 'QUEUED'") || !strings.Contains(query, "status = 'ENQUEUE_PENDING'") {
		t.Fatalf("enqueue transition is not conditional from ENQUEUE_PENDING to QUEUED: %s", query)
	}
}

func TestFailLADispatchEnqueueRecordsPartialCountWithoutOverwritingTerminalStatus(t *testing.T) {
	scenario := &liveActivityFanoutScenario{execRowsAffected: 1}
	store := newLiveActivityFanoutTestStore(t, scenario)

	if err := store.FailLADispatchEnqueue(context.Background(), "dispatch-partial", 42); err != nil {
		t.Fatalf("FailLADispatchEnqueue error = %v", err)
	}

	query := strings.Join(strings.Fields(scenario.lastExecQuery()), " ")
	if !strings.Contains(query, "SET total_count = $2, status = 'FAILED', completed_at = NOW()") {
		t.Fatalf("failure query does not record partial count and terminal status: %s", query)
	}
	if !strings.Contains(query, "status IN ('ENQUEUE_PENDING', 'QUEUED', 'IN_PROGRESS')") {
		t.Fatalf("failure query can overwrite a terminal dispatch: %s", query)
	}
}

func TestCompleteLADispatchEnqueueKeepsJobActiveWhenStartHasNoTargets(t *testing.T) {
	scenario := &liveActivityFanoutScenario{
		emptyDispatchAction: model.LiveActivityActionStart,
		emptyDispatchJobID:  "job-1",
		jobStatus:           model.LiveActivityJobStatusActive,
	}
	store := newLiveActivityFanoutTestStore(t, scenario)

	if err := store.CompleteLADispatchEnqueue(context.Background(), "dispatch-1", 0); err != nil {
		t.Fatalf("CompleteLADispatchEnqueue error = %v", err)
	}
	if got := scenario.currentJobStatus(); got != model.LiveActivityJobStatusActive {
		t.Fatalf("job status = %q, want %q", got, model.LiveActivityJobStatusActive)
	}
}

type liveActivityFanoutScenario struct {
	mu                  sync.Mutex
	scope               liveActivityDispatchFanoutScope
	tokenRows           [][]driver.Value
	tokenHasAssociation bool
	tokenQueries        []string
	execQueries         []string
	execRowsAffected    int64
	emptyDispatchAction model.LiveActivityAction
	emptyDispatchJobID  string
	jobStatus           model.LiveActivityJobStatus
}

func (s *liveActivityFanoutScenario) lastExecQuery() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.execQueries) == 0 {
		return ""
	}
	return s.execQueries[len(s.execQueries)-1]
}

func (s *liveActivityFanoutScenario) currentJobStatus() model.LiveActivityJobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobStatus
}

func (s *liveActivityFanoutScenario) tokenQueryKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	kinds := make([]string, len(s.tokenQueries))
	copy(kinds, s.tokenQueries)
	return kinds
}

func newLiveActivityFanoutTestStore(t *testing.T, scenario *liveActivityFanoutScenario) *PostgresStore {
	t.Helper()

	id := fmt.Sprintf("%d", atomic.AddInt64(&liveActivityFanoutScenarioID, 1))
	liveActivityFanoutScenarios.Store(id, scenario)
	t.Cleanup(func() {
		liveActivityFanoutScenarios.Delete(id)
	})

	db, err := sql.Open(liveActivityFanoutTestDriverName, id)
	if err != nil {
		t.Fatalf("open test sql driver: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close test db: %v", err)
		}
	})

	return &PostgresStore{db: db}
}

func liveActivityFanoutTokenRows(t *testing.T) [][]driver.Value {
	t.Helper()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	return [][]driver.Value{
		{
			"token-1",
			"user-1",
			string(model.FCM),
			string(model.LiveActivityTokenTypeUpdate),
			"fcm-token",
			false,
			now,
			now,
			nil,
			nil,
		},
	}
}

type liveActivityFanoutDriver struct{}

func (liveActivityFanoutDriver) Open(name string) (driver.Conn, error) {
	raw, ok := liveActivityFanoutScenarios.Load(name)
	if !ok {
		return nil, fmt.Errorf("unknown live activity fanout scenario %q", name)
	}
	return &liveActivityFanoutConn{scenario: raw.(*liveActivityFanoutScenario)}, nil
}

type liveActivityFanoutConn struct {
	scenario *liveActivityFanoutScenario
}

func (c *liveActivityFanoutConn) Prepare(query string) (driver.Stmt, error) {
	return nil, fmt.Errorf("Prepare is not implemented")
}

func (c *liveActivityFanoutConn) Close() error {
	return nil
}

func (c *liveActivityFanoutConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("Begin is not implemented")
}

func (c *liveActivityFanoutConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "UPDATE live_activity_dispatches") &&
		strings.Contains(query, "RETURNING action, live_activity_job_id"):
		c.scenario.mu.Lock()
		action := c.scenario.emptyDispatchAction
		jobID := c.scenario.emptyDispatchJobID
		c.scenario.mu.Unlock()
		return &liveActivityFanoutRows{
			columns: []string{"action", "live_activity_job_id"},
			values:  [][]driver.Value{{string(action), jobID}},
		}, nil
	case strings.Contains(query, "SELECT lad.action") && strings.Contains(query, "start_dispatch_pending"):
		if !strings.Contains(query, "'ENQUEUE_PENDING'") {
			return nil, fmt.Errorf("start-pending query ignores dispatches accepted by the API but not yet promoted to QUEUED: %s", query)
		}
		scope := c.scenario.scope
		return &liveActivityFanoutRows{
			columns: []string{"action", "activity_id", "user_id", "topic_id", "start_dispatch_pending"},
			values: [][]driver.Value{{
				string(scope.action),
				scope.activityID,
				scope.userID,
				scope.topicID,
				scope.startDispatchPending,
			}},
		}, nil
	case strings.Contains(query, "SELECT lat.id"):
		queryKind := "user"
		wantArgs := 4
		if strings.Contains(query, "live_activity_token_activities") {
			queryKind = "activity_user"
			if strings.Contains(query, "live_activity_user_topic_subscriptions") {
				queryKind = "activity_topic"
			} else if !strings.Contains(query, "lat.user_id = $2") {
				return nil, fmt.Errorf("activity token query is missing user/topic scope: %s", query)
			}
		} else if strings.Contains(query, "live_activity_user_topic_subscriptions") {
			queryKind = "topic"
		}
		if len(args) != wantArgs {
			return nil, fmt.Errorf("%s token query args len = %d, want %d", queryKind, len(args), wantArgs)
		}
		c.scenario.mu.Lock()
		c.scenario.tokenQueries = append(c.scenario.tokenQueries, queryKind)
		c.scenario.mu.Unlock()

		values := c.scenario.tokenRows
		if strings.HasPrefix(queryKind, "activity_") && !c.scenario.tokenHasAssociation {
			values = nil
		}
		return &liveActivityFanoutRows{
			columns: []string{"id", "user_id", "platform", "token_type", "token", "supports_broadcast_channels", "created_at", "last_seen_at", "expires_at", "invalidated_at"},
			values:  values,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (c *liveActivityFanoutConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "UPDATE live_activity_jobs") &&
		strings.Contains(query, "SET status = 'FAILED'") {
		c.scenario.mu.Lock()
		c.scenario.jobStatus = model.LiveActivityJobStatusFailed
		c.scenario.mu.Unlock()
		return driver.RowsAffected(1), nil
	}

	if !strings.Contains(query, "UPDATE live_activity_dispatches") ||
		(!strings.Contains(query, "SUPERSEDED") &&
			!strings.Contains(query, "status = 'FAILED'") &&
			!strings.Contains(query, "status = 'QUEUED'")) {
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	wantArgs := 2
	if strings.Contains(query, "status = 'QUEUED'") {
		wantArgs = 1
	}
	if len(args) != wantArgs {
		return nil, fmt.Errorf("dispatch exec args len = %d, want %d", len(args), wantArgs)
	}
	c.scenario.mu.Lock()
	c.scenario.execQueries = append(c.scenario.execQueries, query)
	rowsAffected := c.scenario.execRowsAffected
	c.scenario.mu.Unlock()
	return driver.RowsAffected(rowsAffected), nil
}

type liveActivityFanoutRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *liveActivityFanoutRows) Columns() []string {
	return r.columns
}

func (r *liveActivityFanoutRows) Close() error {
	return nil
}

func (r *liveActivityFanoutRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

var _ driver.QueryerContext = (*liveActivityFanoutConn)(nil)
var _ driver.ExecerContext = (*liveActivityFanoutConn)(nil)
