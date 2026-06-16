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
	if got := scenario.tokenQueryKinds(); len(got) != 1 || got[0] != "activity" {
		t.Fatalf("token query kinds = %v, want [activity]", got)
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
	if got := scenario.tokenQueryKinds(); len(got) != 1 || got[0] != "activity" {
		t.Fatalf("token query kinds = %v, want [activity]", got)
	}
}

type liveActivityFanoutScenario struct {
	mu                  sync.Mutex
	scope               liveActivityDispatchFanoutScope
	tokenRows           [][]driver.Value
	tokenHasAssociation bool
	tokenQueries        []string
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
	case strings.Contains(query, "SELECT lad.action") && strings.Contains(query, "start_dispatch_pending"):
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
			queryKind = "activity"
			wantArgs = 3
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
		if queryKind == "activity" && !c.scenario.tokenHasAssociation {
			values = nil
		}
		return &liveActivityFanoutRows{
			columns: []string{"id", "user_id", "platform", "token_type", "token", "created_at", "last_seen_at", "expires_at", "invalidated_at"},
			values:  values,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
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
