package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableJSONRawMessage(value json.RawMessage) any {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return []byte(trimmed)
}

func scanLAToken(scanner interface{ Scan(dest ...any) error }, token *LiveActivityToken) error {
	var platform string
	var tokenType string
	var expiresAt sql.NullTime
	var invalidatedAt sql.NullTime

	if err := scanner.Scan(
		&token.ID,
		&token.UserID,
		&platform,
		&tokenType,
		&token.Token,
		&token.CreatedAt,
		&token.LastSeenAt,
		&expiresAt,
		&invalidatedAt,
	); err != nil {
		return err
	}

	token.Platform = model.Platform(platform)
	token.TokenType = model.LiveActivityTokenType(tokenType)
	token.CreatedAt = token.CreatedAt.UTC()
	token.LastSeenAt = token.LastSeenAt.UTC()
	token.ExpiresAt = nullTimePtr(expiresAt)
	token.InvalidatedAt = nullTimePtr(invalidatedAt)
	return nil
}

const laJobSelectColumns = `
	id,
	activity_id,
	activity_type,
	COALESCE(user_id, ''),
	COALESCE(topic_id, ''),
	status,
	latest_payload,
	COALESCE(options, '{}'::jsonb),
	created_at,
	updated_at,
	expires_at,
	closed_at`

func scanLAJob(scanner interface{ Scan(dest ...any) error }, job *LiveActivityJob) error {
	var status string
	var latestPayload []byte
	var options []byte
	var expiresAt sql.NullTime
	var closedAt sql.NullTime

	if err := scanner.Scan(
		&job.ID,
		&job.ActivityID,
		&job.ActivityType,
		&job.UserID,
		&job.TopicID,
		&status,
		&latestPayload,
		&options,
		&job.CreatedAt,
		&job.UpdatedAt,
		&expiresAt,
		&closedAt,
	); err != nil {
		return err
	}

	job.Status = model.LiveActivityJobStatus(status)
	job.LatestPayload = latestPayload
	job.Options = options
	job.CreatedAt = job.CreatedAt.UTC()
	job.UpdatedAt = job.UpdatedAt.UTC()
	job.ExpiresAt = nullTimePtr(expiresAt)
	job.ClosedAt = nullTimePtr(closedAt)
	return nil
}

func isLAInvalidToken(reason string) bool {
	return strings.Contains(reason, "BadDeviceToken") ||
		strings.Contains(reason, "ExpiredToken") ||
		strings.Contains(reason, "Unregistered") ||
		strings.Contains(reason, "registration-token-not-registered")
}

func laConstraint(err error) string {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Constraint
	}
	return ""
}

type liveActivityDispatchFanoutScope struct {
	action               model.LiveActivityAction
	activityID           string
	userID               string
	topicID              string
	startDispatchPending bool
}

func (scope liveActivityDispatchFanoutScope) requiresActivityAssociation() bool {
	return scope.action != model.LiveActivityActionStart &&
		!scope.startDispatchPending
}

func (s *PostgresStore) UpsertLiveActivityToken(ctx context.Context, token *LiveActivityToken) (*LiveActivityToken, error) {
	now := time.Now().UTC()
	if token.ID == "" {
		token.ID = token.Token
	}
	if token.CreatedAt.IsZero() {
		token.CreatedAt = now
	} else {
		token.CreatedAt = token.CreatedAt.UTC()
	}
	token.LastSeenAt = now
	activityID := strings.TrimSpace(token.ActivityID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("error starting LA token upsert transaction: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(
		ctx,
		`INSERT INTO live_activity_tokens(
			id, user_id, platform, token_type, token, created_at, last_seen_at, expires_at, invalidated_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, NULL)
		ON CONFLICT (platform, token_type, token)
		DO UPDATE SET
			user_id = EXCLUDED.user_id,
			last_seen_at = EXCLUDED.last_seen_at,
			expires_at = EXCLUDED.expires_at,
			invalidated_at = NULL
		RETURNING id, user_id, platform, token_type, token, created_at, last_seen_at, expires_at, invalidated_at`,
		token.ID,
		token.UserID,
		token.Platform,
		token.TokenType,
		token.Token,
		token.CreatedAt,
		token.LastSeenAt,
		optionalTime(token.ExpiresAt),
	)

	var stored LiveActivityToken
	if err := scanLAToken(row, &stored); err != nil {
		return nil, fmt.Errorf("error upserting LA token: %w", err)
	}

	if activityID != "" {
		if err := associateLiveActivityTokenTx(ctx, tx, activityID, stored.ID, now); err != nil {
			return nil, err
		}
		stored.ActivityID = activityID
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("error committing LA token upsert transaction: %w", err)
	}
	return &stored, nil
}

func associateLiveActivityTokenTx(ctx context.Context, tx *sql.Tx, activityID string, tokenID string, lastSeenAt time.Time) error {
	if activityID == "" {
		return fmt.Errorf("activityID is required for LA token association")
	}
	if tokenID == "" {
		return fmt.Errorf("tokenID is required for LA token association")
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO live_activity_token_activities(activity_id, token_id, created_at, last_seen_at)
		 VALUES($1, $2, $3, $3)
		 ON CONFLICT (activity_id, token_id)
		 DO UPDATE SET
		    last_seen_at = EXCLUDED.last_seen_at`,
		activityID,
		tokenID,
		lastSeenAt.UTC(),
	); err != nil {
		return fmt.Errorf("error associating LA token with activity: %w", err)
	}
	return nil
}

func (s *PostgresStore) InvalidateLiveActivityToken(ctx context.Context, userID string, tokenValue string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_tokens
		 SET invalidated_at = NOW()
		 WHERE user_id = $1
		   AND token = $2
		   AND invalidated_at IS NULL`,
		userID,
		tokenValue,
	)
	if err != nil {
		return fmt.Errorf("error invalidating LA token: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) SubscribeUserToLATopic(ctx context.Context, sub *LiveActivityUserTopicSubscription) (*LiveActivityUserTopicSubscription, error) {
	sub.CreatedAt = requiredTime(sub.CreatedAt)

	row := s.db.QueryRowContext(
		ctx,
		`INSERT INTO live_activity_user_topic_subscriptions(id, user_id, topic_id, created_at)
		VALUES($1, $2, $3, $4)
		ON CONFLICT (user_id, topic_id)
		DO UPDATE SET created_at = EXCLUDED.created_at
		RETURNING id, user_id, topic_id, created_at`,
		sub.ID,
		sub.UserID,
		sub.TopicID,
		sub.CreatedAt,
	)

	var stored LiveActivityUserTopicSubscription
	if err := row.Scan(&stored.ID, &stored.UserID, &stored.TopicID, &stored.CreatedAt); err != nil {
		return nil, fmt.Errorf("error subscribing user to LA topic: %w", err)
	}
	stored.CreatedAt = stored.CreatedAt.UTC()
	return &stored, nil
}

func (s *PostgresStore) CreateOrGetLAStartJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, bool, error) {
	job.CreatedAt = requiredTime(job.CreatedAt)
	job.UpdatedAt = requiredTime(job.UpdatedAt)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("error starting LA start transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, job.ActivityID); err != nil {
		return nil, false, fmt.Errorf("error locking LA activity id: %w", err)
	}
	if job.UserID != "" {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, job.UserID); err != nil {
			return nil, false, fmt.Errorf("error locking LA user scope: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM topics WHERE id = $1 FOR UPDATE`, job.TopicID); err != nil {
			return nil, false, fmt.Errorf("error locking LA topic scope: %w", err)
		}
	}

	var existing LiveActivityJob
	row := tx.QueryRowContext(
		ctx,
		`SELECT `+laJobSelectColumns+`
		 FROM live_activity_jobs
		 WHERE activity_id = $1`,
		job.ActivityID,
	)
	if err := scanLAJob(row, &existing); err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("error committing LA start transaction: %w", err)
		}
		return &existing, false, nil
	} else if err != sql.ErrNoRows {
		return nil, false, fmt.Errorf("error loading LA job by activity id: %w", err)
	}

	closeArgs := []any{job.ActivityType}
	closeQuery := `
		UPDATE live_activity_jobs
		SET status = 'CLOSED',
		    closed_at = NOW(),
		    updated_at = NOW()
		WHERE activity_type = $1
		  AND status = 'ACTIVE'
		  AND closed_at IS NULL`
	if job.UserID != "" {
		closeQuery += `
		  AND user_id = $2
		  AND topic_id IS NULL`
		closeArgs = append(closeArgs, job.UserID)
	} else {
		closeQuery += `
		  AND topic_id = $2
		  AND user_id IS NULL`
		closeArgs = append(closeArgs, job.TopicID)
	}
	if _, err := tx.ExecContext(ctx, closeQuery, closeArgs...); err != nil {
		return nil, false, fmt.Errorf("error closing existing LA jobs for scope: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO live_activity_jobs(
			id, activity_id, activity_type, user_id, topic_id, status, latest_payload, options, created_at, updated_at, expires_at, closed_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		job.ID,
		job.ActivityID,
		job.ActivityType,
		nullableString(job.UserID),
		nullableString(job.TopicID),
		job.Status,
		nullableJSONRawMessage(job.LatestPayload),
		nullableJSONRawMessage(job.Options),
		job.CreatedAt,
		job.UpdatedAt,
		optionalTime(job.ExpiresAt),
		optionalTime(job.ClosedAt),
	); err != nil {
		switch laConstraint(err) {
		case "idx_live_activity_jobs_activity_id":
			existingJob, getErr := s.GetLAJobByActivityID(ctx, job.ActivityID)
			return existingJob, false, getErr
		case "idx_live_activity_jobs_active_user":
			existingJob, getErr := s.FindLAJobByUserScope(ctx, job.ActivityType, job.UserID)
			return existingJob, false, getErr
		case "idx_live_activity_jobs_active_topic":
			existingJob, getErr := s.FindLAJobByTopicScope(ctx, job.ActivityType, job.TopicID)
			return existingJob, false, getErr
		default:
			return nil, false, fmt.Errorf("error creating LA start job: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("error committing LA start transaction: %w", err)
	}
	return job, true, nil
}

func (s *PostgresStore) GetLAJob(ctx context.Context, jobID string) (*LiveActivityJob, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT `+laJobSelectColumns+`
		 FROM live_activity_jobs
		 WHERE id = $1`,
		jobID,
	)

	var job LiveActivityJob
	if err := scanLAJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error getting LA job: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) GetLAJobByActivityID(ctx context.Context, activityID string) (*LiveActivityJob, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT `+laJobSelectColumns+`
		 FROM live_activity_jobs
		 WHERE activity_id = $1`,
		activityID,
	)

	var job LiveActivityJob
	if err := scanLAJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error getting LA job by activity id: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) FindLAJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT `+laJobSelectColumns+`
		 FROM live_activity_jobs
		 WHERE activity_type = $1
		   AND user_id = $2
		   AND topic_id IS NULL
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())
		 ORDER BY updated_at DESC
		 LIMIT 1`,
		activityType,
		userID,
	)

	var job LiveActivityJob
	if err := scanLAJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error finding LA job by user scope: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) FindLAJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT `+laJobSelectColumns+`
		 FROM live_activity_jobs
		 WHERE activity_type = $1
		   AND topic_id = $2
		   AND user_id IS NULL
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())
		 ORDER BY updated_at DESC
		 LIMIT 1`,
		activityType,
		topicID,
	)

	var job LiveActivityJob
	if err := scanLAJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error finding LA job by topic scope: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) UpdateLAJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt time.Time) error {
	updatedAt = requiredTime(updatedAt)

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET latest_payload = $1,
		     options = $2,
		     updated_at = $3
		 WHERE id = $4
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())`,
		nullableJSONRawMessage(payload),
		nullableJSONRawMessage(options),
		updatedAt,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("error updating LA job payload: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) RollbackLAStartJob(ctx context.Context, jobID string) error {
	if _, err := s.db.ExecContext(
		ctx,
		`DELETE FROM live_activity_jobs
		 WHERE id = $1
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL
		   AND NOT EXISTS (
			SELECT 1
			FROM live_activity_dispatches
			WHERE live_activity_job_id = $1
		   )`,
		jobID,
	); err != nil {
		return fmt.Errorf("error rolling back LA start job: %w", err)
	}
	return nil
}

func (s *PostgresStore) CloseLAJobIfActive(ctx context.Context, jobID string, updatedAt time.Time) error {
	updatedAt = requiredTime(updatedAt)

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'CLOSED',
		     closed_at = COALESCE(closed_at, NOW()),
		     updated_at = $2
		 WHERE id = $1
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL`,
		jobID,
		updatedAt,
	)
	if err != nil {
		return fmt.Errorf("error closing LA job: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) FailLAJobIfActive(ctx context.Context, jobID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'FAILED',
		     closed_at = COALESCE(closed_at, NOW()),
		     updated_at = NOW()
		 WHERE id = $1
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL`,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("error failing LA job: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) CreateLADispatch(ctx context.Context, dispatch *LiveActivityDispatch) (*LiveActivityDispatch, error) {
	dispatch.CreatedAt = requiredTime(dispatch.CreatedAt)

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO live_activity_dispatches(
			id, live_activity_job_id, action, payload, options, status, total_count, success_count, failure_count, created_at, completed_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		dispatch.ID,
		dispatch.LiveActivityJobID,
		dispatch.Action,
		nullableJSONRawMessage(dispatch.Payload),
		nullableJSONRawMessage(dispatch.Options),
		dispatch.Status,
		dispatch.TotalCount,
		dispatch.SuccessCount,
		dispatch.FailureCount,
		dispatch.CreatedAt,
		optionalTime(dispatch.CompletedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating LA dispatch: %w", err)
	}
	return dispatch, nil
}

func (s *PostgresStore) UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_dispatches
		 SET status = $1,
		     completed_at = CASE WHEN $1 IN ('COMPLETED', 'FAILED') THEN NOW() ELSE completed_at END
		 WHERE id = $2`,
		status,
		dispatchID,
	)
	if err != nil {
		return fmt.Errorf("error updating LA dispatch status: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) getLADispatchFanoutScope(ctx context.Context, dispatchID string) (*liveActivityDispatchFanoutScope, error) {
	var action string
	scope := &liveActivityDispatchFanoutScope{}
	err := s.db.QueryRowContext(
		ctx,
		`SELECT lad.action,
		        laj.activity_id,
		        COALESCE(laj.user_id, ''),
		        COALESCE(laj.topic_id, ''),
		        EXISTS (
			SELECT 1
			FROM live_activity_dispatches start_lad
			WHERE start_lad.live_activity_job_id = laj.id
			  AND start_lad.action = 'start'
			  AND start_lad.status IN ('QUEUED', 'IN_PROGRESS', 'DISPATCHED')
		        ) AS start_dispatch_pending
		 FROM live_activity_dispatches lad
		 JOIN live_activity_jobs laj ON laj.id = lad.live_activity_job_id
		 WHERE lad.id = $1`,
		dispatchID,
	).Scan(&action, &scope.activityID, &scope.userID, &scope.topicID, &scope.startDispatchPending)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error loading LA dispatch fanout scope: %w", err)
	}

	scope.action = model.LiveActivityAction(action)
	return scope, nil
}

func (s *PostgresStore) GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*LiveActivityTokenBatch, error) {
	scope, err := s.getLADispatchFanoutScope(ctx, dispatchID)
	if err != nil {
		return nil, err
	}

	apnsTokenType := model.LiveActivityTokenTypeUpdate
	if scope.action == model.LiveActivityActionStart {
		apnsTokenType = model.LiveActivityTokenTypeStart
	}

	var rows *sql.Rows

	switch {
	case scope.requiresActivityAssociation():
		rows, err = s.db.QueryContext(ctx, laTokenBatchByActivityQuery, scope.activityID, cursor, batchSize+1)
	case scope.userID != "":
		rows, err = s.db.QueryContext(ctx, laTokenBatchByUserQuery, scope.userID, cursor, apnsTokenType, batchSize+1)
	default:
		rows, err = s.db.QueryContext(ctx, laTokenBatchByTopicQuery, scope.topicID, cursor, apnsTokenType, batchSize+1)
	}

	if err != nil {
		return nil, fmt.Errorf("error getting LA token batch: %w", err)
	}
	defer rows.Close()

	var tokens []LiveActivityToken
	for rows.Next() {
		var token LiveActivityToken
		if err := scanLAToken(rows, &token); err != nil {
			return nil, fmt.Errorf("error scanning LA token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating LA token batch: %w", err)
	}

	batch := &LiveActivityTokenBatch{Tokens: tokens}
	if len(tokens) > batchSize {
		batch.HasMore = true
		batch.Tokens = tokens[:batchSize]
		batch.NextCursor = tokens[batchSize-1].ID
	}
	return batch, nil
}

type laOutcomeDelta struct {
	success int
	failure int
}

func (s *PostgresStore) CompleteLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	if totalCount == 0 {
		return s.completeEmptyLADispatch(ctx, dispatchID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("error starting LA dispatch enqueue completion transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE live_activity_dispatches
		 SET total_count = $1,
		     status = 'DISPATCHED'
		 WHERE id = $2`,
		totalCount,
		dispatchID,
	); err != nil {
		return fmt.Errorf("error completing LA dispatch enqueue: %w", err)
	}

	if err := completeLADispatchIfReadyTx(ctx, tx, dispatchID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing LA dispatch enqueue completion: %w", err)
	}
	return nil
}

func (s *PostgresStore) SupersedeLADispatchIfStale(ctx context.Context, dispatchID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE live_activity_dispatches d
		SET status = 'SUPERSEDED', completed_at = NOW()
		WHERE d.id = $1
			AND d.action = 'update'
			AND d.status IN ('QUEUED', 'IN_PROGRESS')
			AND EXISTS (
				SELECT 1 FROM live_activity_dispatches newer
				WHERE newer.live_activity_job_id = d.live_activity_job_id
					AND newer.action = 'update'
					AND newer.created_at > d.created_at
			)`, dispatchID)

	if err != nil {
		return false, fmt.Errorf("error superseding LA dispatch: %v", err)
	}
	rowsAffected, _ := result.RowsAffected()
	return rowsAffected > 0, nil

}

func (s *PostgresStore) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.LASendOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("error starting LA outcome transaction: %w", err)
	}
	defer tx.Rollback()

	deltas, invalidTokenIDs := summarizeLAOutcomes(outcomes)
	if err := invalidateLATokensTx(ctx, tx, invalidTokenIDs); err != nil {
		return err
	}

	lastSeenAt := time.Now().UTC()
	for _, outcome := range outcomes {
		if outcome.Receipt.Status != model.DeliveryStatusSuccess ||
			outcome.Task.LAJob.Action != model.LiveActivityActionStart ||
			outcome.Task.LAJob.ActivityID == "" ||
			outcome.Task.Target.Platform != model.FCM ||
			outcome.Receipt.TokenID == "" {
			continue
		}

		if err := associateLiveActivityTokenTx(ctx, tx, outcome.Task.LAJob.ActivityID, outcome.Receipt.TokenID, lastSeenAt); err != nil {
			return err
		}
	}

	if err := applyLAOutcomeDeltasTx(ctx, tx, deltas); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing LA outcome transaction: %w", err)
	}
	return nil
}

func (s *PostgresStore) completeEmptyLADispatch(ctx context.Context, dispatchID string) error {
	var action string
	var jobID string
	err := s.db.QueryRowContext(
		ctx,
		`UPDATE live_activity_dispatches
		 SET total_count = 0,
		     status = 'COMPLETED',
		     completed_at = NOW()
		 WHERE id = $1
		 RETURNING action, live_activity_job_id`,
		dispatchID,
	).Scan(&action, &jobID)
	if err != nil {
		return fmt.Errorf("error completing empty LA dispatch: %w", err)
	}

	switch action {
	case string(model.LiveActivityActionStart):
		if err := s.FailLAJobIfActive(ctx, jobID); err != nil && !errors.Is(err, Errors.NotFound) {
			return fmt.Errorf("error failing empty LA start job: %w", err)
		}
	case string(model.LiveActivityActionEnd):
		if err := s.CloseLAJobIfActive(ctx, jobID, time.Now().UTC()); err != nil && !errors.Is(err, Errors.NotFound) {
			return fmt.Errorf("error closing empty LA end job: %w", err)
		}
	}
	return nil
}

func summarizeLAOutcomes(outcomes []model.LASendOutcome) (map[string]laOutcomeDelta, map[string]struct{}) {
	deltas := make(map[string]laOutcomeDelta)
	invalidTokenIDs := make(map[string]struct{})

	for _, outcome := range outcomes {
		dispatchID := outcome.Receipt.JobID
		delta := deltas[dispatchID]
		switch outcome.Receipt.Status {
		case model.DeliveryStatusSuccess:
			delta.success++
		case model.DeliveryStatusFailed:
			delta.failure++
			if isLAInvalidToken(outcome.Receipt.StatusReason) {
				invalidTokenIDs[outcome.Receipt.TokenID] = struct{}{}
			}
		}
		deltas[dispatchID] = delta
	}

	return deltas, invalidTokenIDs
}

func invalidateLATokensTx(ctx context.Context, tx *sql.Tx, invalidTokenIDs map[string]struct{}) error {
	if len(invalidTokenIDs) == 0 {
		return nil
	}

	tokenIDs := make([]string, 0, len(invalidTokenIDs))
	for tokenID := range invalidTokenIDs {
		tokenIDs = append(tokenIDs, tokenID)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE live_activity_tokens
		 SET invalidated_at = NOW()
		 WHERE id = ANY($1)
		   AND invalidated_at IS NULL`,
		pq.Array(tokenIDs),
	); err != nil {
		return fmt.Errorf("error invalidating LA tokens: %w", err)
	}
	return nil
}

func applyLAOutcomeDeltasTx(ctx context.Context, tx *sql.Tx, deltas map[string]laOutcomeDelta) error {
	for dispatchID, delta := range deltas {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE live_activity_dispatches
			 SET success_count = success_count + $1,
			     failure_count = failure_count + $2
			 WHERE id = $3`,
			delta.success,
			delta.failure,
			dispatchID,
		); err != nil {
			return fmt.Errorf("error updating LA dispatch counters: %w", err)
		}

		if err := completeLADispatchIfReadyTx(ctx, tx, dispatchID); err != nil {
			return err
		}
	}
	return nil
}

func completeLADispatchIfReadyTx(ctx context.Context, tx *sql.Tx, dispatchID string) error {
	var action string
	var jobID string
	err := tx.QueryRowContext(
		ctx,
		`UPDATE live_activity_dispatches
		 SET status = 'COMPLETED',
		     completed_at = NOW()
		 WHERE id = $1
		   AND status = 'DISPATCHED'
		   AND success_count + failure_count >= total_count
		 RETURNING action, live_activity_job_id`,
		dispatchID,
	).Scan(&action, &jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("error completing LA dispatch: %w", err)
	}

	if action == string(model.LiveActivityActionEnd) {
		return closeLAJobAfterEndDispatchTx(ctx, tx, jobID)
	}
	return nil
}

func closeLAJobAfterEndDispatchTx(ctx context.Context, tx *sql.Tx, jobID string) error {
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'CLOSED',
		     closed_at = COALESCE(closed_at, NOW()),
		     updated_at = NOW()
		 WHERE id = $1
		   AND status = 'ACTIVE'
		   AND closed_at IS NULL`,
		jobID,
	); err != nil {
		return fmt.Errorf("error closing LA job after end dispatch: %w", err)
	}
	return nil
}

func (s *PostgresStore) InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error) {
	result, err := s.db.ExecContext(
		ctx,
		`WITH candidates AS (
			SELECT id
			FROM live_activity_tokens
			WHERE platform = 'apns'
			  AND token_type = 'update'
			  AND invalidated_at IS NULL
			  AND expires_at IS NOT NULL
			  AND expires_at <= NOW()
			ORDER BY expires_at
			LIMIT $1
		)
		UPDATE live_activity_tokens lat
		SET invalidated_at = NOW()
		FROM candidates
		WHERE lat.id = candidates.id`,
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("error invalidating expired LA update tokens: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	return int(rowsAffected), nil
}

const laTokenBatchByActivityQuery = `
       SELECT lat.id, lat.user_id, lat.platform, lat.token_type, lat.token,
              lat.created_at, lat.last_seen_at, lat.expires_at, lat.invalidated_at
       FROM live_activity_token_activities lata
       JOIN live_activity_tokens lat ON lat.id = lata.token_id
       WHERE lata.activity_id = $1
         AND lata.token_id > $2
         AND lat.invalidated_at IS NULL
         AND (lat.expires_at IS NULL OR lat.expires_at > NOW())
         AND lat.token_type = 'update'
       ORDER BY lata.token_id
       LIMIT $3`

const laTokenBatchByUserQuery = `
       SELECT lat.id, lat.user_id, lat.platform, lat.token_type, lat.token,
              lat.created_at, lat.last_seen_at, lat.expires_at, lat.invalidated_at
       FROM live_activity_tokens lat
       WHERE lat.user_id = $1
         AND lat.id > $2
         AND lat.invalidated_at IS NULL
         AND (lat.expires_at IS NULL OR lat.expires_at > NOW())
         AND (
               (lat.platform = 'apns' AND lat.token_type = $3)
               OR (lat.platform = 'fcm' AND lat.token_type = 'update')
         )
       ORDER BY lat.id
       LIMIT $4`

const laTokenBatchByTopicQuery = `
       SELECT lat.id, lat.user_id, lat.platform, lat.token_type, lat.token,
              lat.created_at, lat.last_seen_at, lat.expires_at, lat.invalidated_at
       FROM live_activity_tokens lat
       JOIN live_activity_user_topic_subscriptions sub
         ON sub.user_id = lat.user_id
         AND sub.topic_id = $1
       WHERE lat.id > $2
         AND lat.invalidated_at IS NULL
         AND (lat.expires_at IS NULL OR lat.expires_at > NOW())
         AND (
               (lat.platform = 'apns' AND lat.token_type = $3)
               OR (lat.platform = 'fcm' AND lat.token_type = 'update')
         )
       ORDER BY lat.id
       LIMIT $4`
