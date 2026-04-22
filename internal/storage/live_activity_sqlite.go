package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

var errLiveActivitySQLiteUnsupported = fmt.Errorf("live activity is only supported on postgres in this build")

func IsLiveActivityUnsupported(err error) bool {
	return errors.Is(err, errLiveActivitySQLiteUnsupported)
}

func (s *SQLiteStore) UpsertLiveActivityToken(ctx context.Context, token *LiveActivityToken) (*LiveActivityToken, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) InvalidateLiveActivityToken(ctx context.Context, tokenID string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) SubscribeUserToLiveActivityTopic(ctx context.Context, sub *LiveActivityUserTopicSubscription) (*LiveActivityUserTopicSubscription, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CreateOrGetLiveActivityStartJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, bool, error) {
	return nil, false, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLiveActivityJob(ctx context.Context, jobID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLiveActivityJobByActivityID(ctx context.Context, activityID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FindLiveActivityJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FindLiveActivityJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) UpdateLiveActivityJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) MarkLiveActivityJobClosingIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) MarkLiveActivityJobClosedIfClosing(ctx context.Context, jobID string, reason model.LiveActivityClosedReason) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) MarkLiveActivityJobFailedIfActive(ctx context.Context, jobID string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) ReopenLiveActivityJobIfClosing(ctx context.Context, jobID string) error {
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

func (s *SQLiteStore) InvalidateExpiredLiveActivityUpdateTokens(ctx context.Context, limit int) (int, error) {
	return 0, errLiveActivitySQLiteUnsupported
}
