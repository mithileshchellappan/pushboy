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

func formatTimePtr(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func scanLAToken(scanner interface{ Scan(dest ...any) error }, token *LiveActivityToken) error {
	var platform string
	var tokenType string
	var createdAt time.Time
	var lastSeenAt time.Time
	var expiresAt sql.NullTime
	var invalidatedAt sql.NullTime

	if err := scanner.Scan(
		&token.ID,
		&token.UserID,
		&platform,
		&tokenType,
		&token.Token,
		&createdAt,
		&lastSeenAt,
		&expiresAt,
		&invalidatedAt,
	); err != nil {
		return err
	}

	token.Platform = model.Platform(platform)
	token.TokenType = model.LiveActivityTokenType(tokenType)
	token.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	token.LastSeenAt = lastSeenAt.UTC().Format(time.RFC3339)
	token.ExpiresAt = formatTimePtr(expiresAt)
	token.InvalidatedAt = formatTimePtr(invalidatedAt)
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
	var createdAt time.Time
	var updatedAt time.Time
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
		&createdAt,
		&updatedAt,
		&expiresAt,
		&closedAt,
	); err != nil {
		return err
	}

	job.Status = model.LiveActivityJobStatus(status)
	job.LatestPayload = latestPayload
	job.Options = options
	job.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	job.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	job.ExpiresAt = formatTimePtr(expiresAt)
	job.ClosedAt = formatTimePtr(closedAt)
	return nil
}

func isLAInvalidToken(reason string) bool {
	return strings.Contains(reason, "BadDeviceToken") ||
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

func (s *PostgresStore) UpsertLiveActivityToken(ctx context.Context, token *LiveActivityToken) (*LiveActivityToken, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if token.ID == "" {
		token.ID = token.Token
	}
	if token.CreatedAt == "" {
		token.CreatedAt = now
	}
	token.LastSeenAt = now

	row := s.db.QueryRowContext(
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
		nullableString(token.ExpiresAt),
	)

	var stored LiveActivityToken
	if err := scanLAToken(row, &stored); err != nil {
		return nil, fmt.Errorf("error upserting LA token: %w", err)
	}
	return &stored, nil
}

func (s *PostgresStore) InvalidateLiveActivityToken(ctx context.Context, tokenID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_tokens
		 SET invalidated_at = NOW()
		 WHERE id = $1
		   AND invalidated_at IS NULL`,
		tokenID,
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
	var createdAt time.Time
	if err := row.Scan(&stored.ID, &stored.UserID, &stored.TopicID, &createdAt); err != nil {
		return nil, fmt.Errorf("error subscribing user to LA topic: %w", err)
	}
	stored.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return &stored, nil
}

func (s *PostgresStore) CreateOrGetLAStartJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, bool, error) {
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
		job.LatestPayload,
		job.Options,
		job.CreatedAt,
		job.UpdatedAt,
		nullableString(job.ExpiresAt),
		nullableString(job.ClosedAt),
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

func (s *PostgresStore) UpdateLAJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error {
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
		payload,
		options,
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

func (s *PostgresStore) CloseLAJobIfActive(ctx context.Context, jobID string, updatedAt string) error {
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
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO live_activity_dispatches(
			id, live_activity_job_id, action, payload, options, status, total_count, success_count, failure_count, created_at, completed_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		dispatch.ID,
		dispatch.LiveActivityJobID,
		dispatch.Action,
		dispatch.Payload,
		dispatch.Options,
		dispatch.Status,
		dispatch.TotalCount,
		dispatch.SuccessCount,
		dispatch.FailureCount,
		dispatch.CreatedAt,
		nullableString(dispatch.CompletedAt),
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

func (s *PostgresStore) GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*LiveActivityTokenBatch, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT lat.id, lat.user_id, lat.platform, lat.token_type, lat.token, lat.created_at, lat.last_seen_at, lat.expires_at, lat.invalidated_at
		 FROM live_activity_dispatches lad
		 JOIN live_activity_jobs laj ON laj.id = lad.live_activity_job_id
		 JOIN live_activity_tokens lat
		   ON lat.token_type = CASE WHEN lad.action = 'start' THEN 'start' ELSE 'update' END
		  AND lat.invalidated_at IS NULL
		  AND (lat.expires_at IS NULL OR lat.expires_at > NOW())
		 WHERE lad.id = $1
		   AND lat.id > $2
		   AND (
			(laj.user_id IS NOT NULL AND lat.user_id = laj.user_id)
			OR
			(
				laj.topic_id IS NOT NULL AND lat.user_id IN (
					SELECT user_id
					FROM live_activity_user_topic_subscriptions
					WHERE topic_id = laj.topic_id
				)
			)
		   )
		 ORDER BY lat.id
		 LIMIT $3`,
		dispatchID,
		cursor,
		batchSize+1,
	)
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

func (s *PostgresStore) FinalizeLADispatch(ctx context.Context, dispatchID string, totalCount int) error {
	if totalCount == 0 {
		var action string
		var jobID string
		err := s.db.QueryRowContext(
			ctx,
			`UPDATE live_activity_dispatches
			 SET total_count = $1,
			     status = 'COMPLETED',
			     completed_at = NOW()
			 WHERE id = $2
			 RETURNING action, live_activity_job_id`,
			totalCount,
			dispatchID,
		).Scan(&action, &jobID)
		if err != nil {
			return fmt.Errorf("error finalizing empty LA dispatch: %w", err)
		}

		switch action {
		case string(model.LiveActivityActionStart):
			if err := s.FailLAJobIfActive(ctx, jobID); err != nil && !errors.Is(err, Errors.NotFound) {
				return fmt.Errorf("error failing empty LA start job: %w", err)
			}
		case string(model.LiveActivityActionEnd):
			if err := s.CloseLAJobIfActive(ctx, jobID, time.Now().UTC().Format(time.RFC3339)); err != nil && !errors.Is(err, Errors.NotFound) {
				return fmt.Errorf("error closing empty LA end job: %w", err)
			}
		}
		return nil
	}

	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_dispatches
		 SET total_count = $1,
		     status = 'DISPATCHED'
		 WHERE id = $2`,
		totalCount,
		dispatchID,
	); err != nil {
		return fmt.Errorf("error finalizing LA dispatch: %w", err)
	}
	return nil
}

func (s *PostgresStore) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}

	type counterDelta struct {
		success int
		failure int
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("error starting LA outcome transaction: %w", err)
	}
	defer tx.Rollback()

	deltas := make(map[string]*counterDelta)
	invalidTokenIDs := make(map[string]struct{})

	for _, outcome := range outcomes {
		dispatchID := outcome.Receipt.JobID
		if _, ok := deltas[dispatchID]; !ok {
			deltas[dispatchID] = &counterDelta{}
		}

		switch outcome.Receipt.Status {
		case string(model.Success):
			deltas[dispatchID].success++
		case string(model.Failed):
			deltas[dispatchID].failure++
		}

		if outcome.Receipt.Status == string(model.Failed) && isLAInvalidToken(outcome.Receipt.StatusReason) {
			invalidTokenIDs[outcome.Receipt.TokenID] = struct{}{}
		}
	}

	if len(invalidTokenIDs) > 0 {
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
	}

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
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("error completing LA dispatch: %w", err)
		}

		if err == nil && action == string(model.LiveActivityActionEnd) {
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
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing LA outcome transaction: %w", err)
	}
	return nil
}

func (s *PostgresStore) InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error) {
	result, err := s.db.ExecContext(
		ctx,
		`WITH candidates AS (
			SELECT id
			FROM live_activity_tokens
			WHERE token_type = 'update'
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
