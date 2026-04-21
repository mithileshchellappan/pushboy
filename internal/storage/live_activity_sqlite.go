package storage

import (
	"context"
	"fmt"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

var errLiveActivitySQLiteUnsupported = fmt.Errorf("live activity is only supported on postgres in this build")

func (s *SQLiteStore) UpsertLiveActivityToken(ctx context.Context, token *LiveActivityToken) (*LiveActivityToken, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) InvalidateLiveActivityToken(ctx context.Context, tokenID string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) SubscribeUserToLiveActivityTopic(ctx context.Context, sub *LiveActivityUserTopicSubscription) (*LiveActivityUserTopicSubscription, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CreateLiveActivityJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLiveActivityJob(ctx context.Context, jobID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FindLiveActivityJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FindLiveActivityJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) UpdateLiveActivityJob(ctx context.Context, job *LiveActivityJob) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CreateLiveActivityDispatch(ctx context.Context, dispatch *LiveActivityDispatch) (*LiveActivityDispatch, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) UpdateLiveActivityDispatchStatus(ctx context.Context, dispatchID string, status string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLiveActivityTokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*LiveActivityTokenBatch, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FinalizeLiveActivityDispatch(ctx context.Context, dispatchID string, totalCount int) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) ApplyLiveActivityOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error {
	return errLiveActivitySQLiteUnsupported
}
