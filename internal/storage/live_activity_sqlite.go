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

func (s *SQLiteStore) SubscribeUserToLATopic(ctx context.Context, sub *LiveActivityUserTopicSubscription) (*LiveActivityUserTopicSubscription, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CreateOrGetLAStartJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, bool, error) {
	return nil, false, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLAJob(ctx context.Context, jobID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLAJobByActivityID(ctx context.Context, activityID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FindLAJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FindLAJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) UpdateLAJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) RollbackLAStartJob(ctx context.Context, jobID string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CloseLAJobIfActive(ctx context.Context, jobID string, updatedAt string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) FailLAJobIfActive(ctx context.Context, jobID string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CreateLADispatch(ctx context.Context, dispatch *LiveActivityDispatch) (*LiveActivityDispatch, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*LiveActivityTokenBatch, error) {
	return nil, errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) CompleteLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) RecordLAOutcomes(ctx context.Context, outcomes []model.SendOutcome) error {
	return errLiveActivitySQLiteUnsupported
}

func (s *SQLiteStore) InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error) {
	return 0, errLiveActivitySQLiteUnsupported
}
