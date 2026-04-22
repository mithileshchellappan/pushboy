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

func scanLiveActivityToken(scanner interface{ Scan(dest ...any) error }, token *LiveActivityToken) error {
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

const liveActivityJobSelectColumns = `
		id,
		activity_id,
		activity_type,
		COALESCE(user_id, ''),
		COALESCE(topic_id, ''),
		status,
		COALESCE(closed_reason, ''),
		latest_payload,
		COALESCE(options, '{}'::jsonb),
		created_at,
		updated_at,
		expires_at,
		closed_at`

type liveActivityQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func queryLiveActivityJobByScope(ctx context.Context, queryer liveActivityQueryer, activityType string, userID string, topicID string, includeExpired bool) (*LiveActivityJob, error) {
	var query string
	var row *sql.Row

	if userID != "" {
		query = `
			SELECT ` + liveActivityJobSelectColumns + `
			FROM live_activity_jobs
			WHERE activity_type = $1
			  AND user_id = $2
			  AND topic_id IS NULL
			  AND closed_at IS NULL
			  AND status IN ('active', 'closing')`
		if !includeExpired {
			query += `
			  AND (expires_at IS NULL OR expires_at > NOW())`
		}
		query += `
			ORDER BY updated_at DESC
			LIMIT 1`
		row = queryer.QueryRowContext(ctx, query, activityType, userID)
	} else {
		query = `
			SELECT ` + liveActivityJobSelectColumns + `
			FROM live_activity_jobs
			WHERE activity_type = $1
			  AND topic_id = $2
			  AND user_id IS NULL
			  AND closed_at IS NULL
			  AND status IN ('active', 'closing')`
		if !includeExpired {
			query += `
			  AND (expires_at IS NULL OR expires_at > NOW())`
		}
		query += `
			ORDER BY updated_at DESC
			LIMIT 1`
		row = queryer.QueryRowContext(ctx, query, activityType, topicID)
	}

	var job LiveActivityJob
	if err := scanLiveActivityJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error finding live activity job by scope: %w", err)
	}
	return &job, nil
}

func scanLiveActivityJob(scanner interface{ Scan(dest ...any) error }, job *LiveActivityJob) error {
	var status string
	var closedReason string
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
		&closedReason,
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
	job.ClosedReason = model.LiveActivityClosedReason(closedReason)
	job.LatestPayload = latestPayload
	job.Options = options
	job.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	job.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	job.ExpiresAt = formatTimePtr(expiresAt)
	job.ClosedAt = formatTimePtr(closedAt)
	return nil
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

	query := `
		INSERT INTO live_activity_tokens(
			id, user_id, platform, token_type, token, created_at, last_seen_at, expires_at, invalidated_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, NULL)
		ON CONFLICT (platform, token_type, token)
		DO UPDATE SET
			user_id = EXCLUDED.user_id,
			last_seen_at = EXCLUDED.last_seen_at,
			expires_at = EXCLUDED.expires_at,
			invalidated_at = NULL
		RETURNING id, user_id, platform, token_type, token, created_at, last_seen_at, expires_at, invalidated_at`

	row := s.db.QueryRowContext(
		ctx,
		query,
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
	if err := scanLiveActivityToken(row, &stored); err != nil {
		return nil, fmt.Errorf("error upserting live activity token: %w", err)
	}
	return &stored, nil
}

func (s *PostgresStore) InvalidateLiveActivityToken(ctx context.Context, tokenID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_tokens SET invalidated_at = NOW() WHERE id = $1 AND invalidated_at IS NULL`,
		tokenID,
	)
	if err != nil {
		return fmt.Errorf("error invalidating live activity token: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) SubscribeUserToLiveActivityTopic(ctx context.Context, sub *LiveActivityUserTopicSubscription) (*LiveActivityUserTopicSubscription, error) {
	query := `
		INSERT INTO live_activity_user_topic_subscriptions(id, user_id, topic_id, created_at)
		VALUES($1, $2, $3, $4)
		ON CONFLICT (user_id, topic_id)
		DO UPDATE SET created_at = EXCLUDED.created_at
		RETURNING id, user_id, topic_id, created_at`

	row := s.db.QueryRowContext(ctx, query, sub.ID, sub.UserID, sub.TopicID, sub.CreatedAt)
	var stored LiveActivityUserTopicSubscription
	var createdAt time.Time
	if err := row.Scan(&stored.ID, &stored.UserID, &stored.TopicID, &createdAt); err != nil {
		return nil, fmt.Errorf("error subscribing user to live activity topic: %w", err)
	}
	stored.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return &stored, nil
}

func getLiveActivityJobByActivityID(ctx context.Context, queryer liveActivityQueryer, activityID string) (*LiveActivityJob, error) {
	row := queryer.QueryRowContext(
		ctx,
		`SELECT `+liveActivityJobSelectColumns+`
		 FROM live_activity_jobs
		 WHERE activity_id = $1`,
		activityID,
	)

	var job LiveActivityJob
	if err := scanLiveActivityJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error getting live activity job by activity id: %w", err)
	}
	return &job, nil
}

func lockLiveActivityScope(ctx context.Context, tx *sql.Tx, userID string, topicID string) error {
	if userID != "" {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return fmt.Errorf("error locking live activity user scope: %w", err)
		}
		return nil
	}

	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM topics WHERE id = $1 FOR UPDATE`, topicID); err != nil {
		return fmt.Errorf("error locking live activity topic scope: %w", err)
	}
	return nil
}

func lockLiveActivityActivityID(ctx context.Context, tx *sql.Tx, activityID string) error {
	if activityID == "" {
		return nil
	}

	if _, err := tx.ExecContext(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		activityID,
	); err != nil {
		return fmt.Errorf("error locking live activity activity id: %w", err)
	}
	return nil
}

func closeCurrentLiveActivityJobsByScope(ctx context.Context, queryer liveActivityQueryer, activityType string, userID string, topicID string, requireExpired bool, reason model.LiveActivityClosedReason) error {
	query := `
		UPDATE live_activity_jobs
		SET status = 'closed',
		    closed_reason = $1,
		    closed_at = NOW(),
		    updated_at = NOW()
		WHERE activity_type = $2
		  AND status IN ('active', 'closing')
		  AND closed_at IS NULL`

	args := []any{reason, activityType}
	if userID != "" {
		query += `
		  AND user_id = $3
		  AND topic_id IS NULL`
		args = append(args, userID)
	} else {
		query += `
		  AND topic_id = $3
		  AND user_id IS NULL`
		args = append(args, topicID)
	}
	if requireExpired {
		query += `
		  AND expires_at IS NOT NULL
		  AND expires_at <= NOW()`
	}

	if _, err := queryer.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("error closing live activity jobs by scope: %w", err)
	}
	return nil
}

func (s *PostgresStore) CreateOrGetLiveActivityStartJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("error starting live activity start transaction: %w", err)
	}
	defer tx.Rollback()

	if err := lockLiveActivityActivityID(ctx, tx, job.ActivityID); err != nil {
		return nil, false, err
	}
	if err := lockLiveActivityScope(ctx, tx, job.UserID, job.TopicID); err != nil {
		return nil, false, err
	}

	existing, err := getLiveActivityJobByActivityID(ctx, tx, job.ActivityID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("error committing live activity start transaction: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, Errors.NotFound) {
		return nil, false, err
	}

	if err := closeCurrentLiveActivityJobsByScope(ctx, tx, job.ActivityType, job.UserID, job.TopicID, true, model.LiveActivityClosedReasonExpired); err != nil {
		return nil, false, err
	}

	if err := closeCurrentLiveActivityJobsByScope(ctx, tx, job.ActivityType, job.UserID, job.TopicID, false, model.LiveActivityClosedReasonSuperseded); err != nil {
		return nil, false, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO live_activity_jobs(
			id, activity_id, activity_type, user_id, topic_id, status, closed_reason, latest_payload, options, created_at, updated_at, expires_at, closed_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		job.ID,
		job.ActivityID,
		job.ActivityType,
		nullableString(job.UserID),
		nullableString(job.TopicID),
		job.Status,
		nullableString(string(job.ClosedReason)),
		job.LatestPayload,
		job.Options,
		job.CreatedAt,
		job.UpdatedAt,
		nullableString(job.ExpiresAt),
		nullableString(job.ClosedAt),
	); err != nil {
		if strings.Contains(err.Error(), "live_activity_jobs_activity_id") || strings.Contains(err.Error(), "idx_live_activity_jobs_activity_id") {
			existingByActivityID, findErr := getLiveActivityJobByActivityID(ctx, s.db, job.ActivityID)
			if findErr == nil {
				return existingByActivityID, false, nil
			}
		}
		if strings.Contains(err.Error(), "idx_live_activity_jobs_active_") {
			currentJob, findErr := queryLiveActivityJobByScope(ctx, s.db, job.ActivityType, job.UserID, job.TopicID, true)
			if findErr == nil {
				return currentJob, false, nil
			}
		}
		return nil, false, fmt.Errorf("error creating live activity start job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("error committing live activity start transaction: %w", err)
	}
	return job, true, nil
}

func (s *PostgresStore) GetLiveActivityJob(ctx context.Context, jobID string) (*LiveActivityJob, error) {
	query := `
		SELECT ` + liveActivityJobSelectColumns + `
		FROM live_activity_jobs
		WHERE id = $1`

	row := s.db.QueryRowContext(ctx, query, jobID)
	var job LiveActivityJob
	if err := scanLiveActivityJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error getting live activity job: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) GetLiveActivityJobByActivityID(ctx context.Context, activityID string) (*LiveActivityJob, error) {
	return getLiveActivityJobByActivityID(ctx, s.db, activityID)
}

func (s *PostgresStore) FindLiveActivityJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error) {
	return queryLiveActivityJobByScope(ctx, s.db, activityType, userID, "", false)
}

func (s *PostgresStore) FindLiveActivityJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error) {
	return queryLiveActivityJobByScope(ctx, s.db, activityType, "", topicID, false)
}

func (s *PostgresStore) UpdateLiveActivityJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET latest_payload = $1,
		     options = $2,
		     updated_at = $3
		 WHERE id = $4
		   AND status = 'active'
		   AND closed_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())`,
		payload,
		options,
		updatedAt,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("error updating live activity job payload: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) MarkLiveActivityJobClosingIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'closing',
		     closed_reason = NULL,
		     latest_payload = CASE WHEN $1::jsonb IS NULL THEN latest_payload ELSE $1::jsonb END,
		     options = $2,
		     updated_at = $3
		 WHERE id = $4
		   AND status = 'active'
		   AND closed_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())`,
		payload,
		options,
		updatedAt,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("error marking live activity job closing: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) MarkLiveActivityJobClosedIfClosing(ctx context.Context, jobID string, reason model.LiveActivityClosedReason) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'closed',
		     closed_reason = $2,
		     closed_at = COALESCE(closed_at, NOW()),
		     updated_at = NOW()
		 WHERE id = $1
		   AND status = 'closing'
		   AND closed_at IS NULL`,
		jobID,
		reason,
	)
	if err != nil {
		return fmt.Errorf("error marking live activity job closed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) MarkLiveActivityJobFailedIfActive(ctx context.Context, jobID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'failed',
		     closed_reason = 'failed',
		     closed_at = COALESCE(closed_at, NOW()),
		     updated_at = NOW()
		 WHERE id = $1
		   AND status = 'active'
		   AND closed_at IS NULL`,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("error marking live activity job failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) ReopenLiveActivityJobIfClosing(ctx context.Context, jobID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_jobs
		 SET status = 'active',
		     closed_reason = NULL,
		     closed_at = NULL,
		     updated_at = NOW()
		 WHERE id = $1
		   AND status = 'closing'
		   AND closed_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())`,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("error reopening live activity job: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) CreateLiveActivityDispatch(ctx context.Context, dispatch *LiveActivityDispatch) (*LiveActivityDispatch, error) {
	query := `
		INSERT INTO live_activity_dispatches(
			id, live_activity_job_id, action, payload, options, status, total_count, success_count, failure_count, created_at, completed_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err := s.db.ExecContext(
		ctx,
		query,
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
		return nil, fmt.Errorf("error creating live activity dispatch: %w", err)
	}
	return dispatch, nil
}

func (s *PostgresStore) UpdateLiveActivityDispatchStatus(ctx context.Context, dispatchID string, status string) error {
	query := `UPDATE live_activity_dispatches SET status = $1, completed_at = CASE WHEN $1 IN ('COMPLETED', 'FAILED') THEN NOW() ELSE completed_at END WHERE id = $2`
	result, err := s.db.ExecContext(ctx, query, status, dispatchID)
	if err != nil {
		return fmt.Errorf("error updating live activity dispatch status: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}
	return nil
}

func (s *PostgresStore) GetLiveActivityTokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*LiveActivityTokenBatch, error) {
	query := `
		SELECT lat.id, lat.user_id, lat.platform, lat.token_type, lat.token, lat.created_at, lat.last_seen_at, lat.expires_at, lat.invalidated_at
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
		LIMIT $3`

	rows, err := s.db.QueryContext(ctx, query, dispatchID, cursor, batchSize+1)
	if err != nil {
		return nil, fmt.Errorf("error getting live activity token batch: %w", err)
	}
	defer rows.Close()

	var tokens []LiveActivityToken
	for rows.Next() {
		var token LiveActivityToken
		if err := scanLiveActivityToken(rows, &token); err != nil {
			return nil, fmt.Errorf("error scanning live activity token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating live activity token batch: %w", err)
	}

	batch := &LiveActivityTokenBatch{
		Tokens:  tokens,
		HasMore: false,
	}
	if len(tokens) > batchSize {
		batch.HasMore = true
		batch.Tokens = tokens[:batchSize]
		batch.NextCursor = tokens[batchSize-1].ID
	}
	return batch, nil
}

func (s *PostgresStore) FinalizeLiveActivityDispatch(ctx context.Context, dispatchID string, totalCount int) error {
	if totalCount == 0 {
		var action string
		var jobID string
		err := s.db.QueryRowContext(
			ctx,
			`UPDATE live_activity_dispatches
			 SET total_count = $1, status = 'COMPLETED', completed_at = NOW()
			 WHERE id = $2
			 RETURNING action, live_activity_job_id`,
			totalCount,
			dispatchID,
		).Scan(&action, &jobID)
		if err != nil {
			return fmt.Errorf("error finalizing empty live activity dispatch: %w", err)
		}

		if action == string(model.LiveActivityActionEnd) {
			if _, err := s.db.ExecContext(
				ctx,
				`UPDATE live_activity_jobs
				 SET status = 'closed', closed_reason = 'ended', closed_at = NOW(), updated_at = NOW()
				 WHERE id = $1
				   AND status = 'closing'`,
				jobID,
			); err != nil {
				return fmt.Errorf("error closing live activity job for empty end dispatch: %w", err)
			}
		}

		if action == string(model.LiveActivityActionStart) {
			if _, err := s.db.ExecContext(
				ctx,
				`UPDATE live_activity_jobs
				 SET status = 'failed', closed_reason = 'failed', closed_at = NOW(), updated_at = NOW()
				 WHERE id = $1
				   AND status = 'active'
				   AND closed_at IS NULL`,
				jobID,
			); err != nil {
				return fmt.Errorf("error failing empty live activity start job: %w", err)
			}
		}
		return nil
	}

	_, err := s.db.ExecContext(
		ctx,
		`UPDATE live_activity_dispatches SET total_count = $1, status = 'DISPATCHED' WHERE id = $2`,
		totalCount,
		dispatchID,
	)
	if err != nil {
		return fmt.Errorf("error finalizing live activity dispatch: %w", err)
	}
	return nil
}

func isLiveActivityTokenInvalid(reason string) bool {
	return strings.Contains(reason, "BadDeviceToken") ||
		strings.Contains(reason, "Unregistered") ||
		strings.Contains(reason, "registration-token-not-registered")
}

func (s *PostgresStore) ApplyLiveActivityOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}

	type counterDelta struct {
		success int
		failure int
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("error starting live activity outcome transaction: %w", err)
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

		if outcome.Receipt.Status == string(model.Failed) && isLiveActivityTokenInvalid(outcome.Receipt.StatusReason) {
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
			return fmt.Errorf("error invalidating live activity tokens: %w", err)
		}
	}

	for dispatchID, delta := range deltas {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE live_activity_dispatches
			 SET success_count = success_count + $1, failure_count = failure_count + $2
			 WHERE id = $3`,
			delta.success,
			delta.failure,
			dispatchID,
		); err != nil {
			return fmt.Errorf("error updating live activity dispatch counters: %w", err)
		}

		var action string
		var jobID string
		err := tx.QueryRowContext(
			ctx,
			`UPDATE live_activity_dispatches
			 SET status = 'COMPLETED', completed_at = NOW()
			 WHERE id = $1
			   AND status = 'DISPATCHED'
			   AND success_count + failure_count >= total_count
			 RETURNING action, live_activity_job_id`,
			dispatchID,
		).Scan(&action, &jobID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("error completing live activity dispatch: %w", err)
		}

		if err == nil && action == string(model.LiveActivityActionEnd) {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE live_activity_jobs
					 SET status = 'closed', closed_reason = 'ended', closed_at = NOW(), updated_at = NOW()
					 WHERE id = $1
					   AND status = 'closing'`,
				jobID,
			); err != nil {
				return fmt.Errorf("error closing live activity job after end dispatch: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing live activity outcome transaction: %w", err)
	}
	return nil
}

func (s *PostgresStore) InvalidateExpiredLiveActivityUpdateTokens(ctx context.Context, limit int) (int, error) {
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
		return 0, fmt.Errorf("error invalidating expired live activity update tokens: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	return int(rowsAffected), nil
}
