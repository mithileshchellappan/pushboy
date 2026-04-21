package storage

import (
	"context"
	"database/sql"
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

func scanLiveActivityJob(scanner interface{ Scan(dest ...any) error }, job *LiveActivityJob) error {
	var status string
	var latestPayload []byte
	var options []byte
	var createdAt time.Time
	var updatedAt time.Time
	var expiresAt sql.NullTime
	var closedAt sql.NullTime

	if err := scanner.Scan(
		&job.ID,
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

func (s *PostgresStore) CreateLiveActivityJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, error) {
	query := `
		INSERT INTO live_activity_jobs(
			id, activity_type, user_id, topic_id, status, latest_payload, options, created_at, updated_at, expires_at, closed_at
		)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err := s.db.ExecContext(
		ctx,
		query,
		job.ID,
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
	)
	if err != nil {
		if strings.Contains(err.Error(), "idx_live_activity_jobs_active_") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating live activity job: %w", err)
	}
	return job, nil
}

func (s *PostgresStore) GetLiveActivityJob(ctx context.Context, jobID string) (*LiveActivityJob, error) {
	query := `
		SELECT id, activity_type, COALESCE(user_id, ''), COALESCE(topic_id, ''), status, latest_payload, COALESCE(options, '{}'::jsonb), created_at, updated_at, expires_at, closed_at
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

func (s *PostgresStore) FindLiveActivityJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error) {
	query := `
		SELECT id, activity_type, COALESCE(user_id, ''), COALESCE(topic_id, ''), status, latest_payload, COALESCE(options, '{}'::jsonb), created_at, updated_at, expires_at, closed_at
		FROM live_activity_jobs
		WHERE activity_type = $1
		  AND user_id = $2
		  AND topic_id IS NULL
		  AND status IN ('active', 'closing')
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY updated_at DESC
		LIMIT 1`

	row := s.db.QueryRowContext(ctx, query, activityType, userID)
	var job LiveActivityJob
	if err := scanLiveActivityJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error finding live activity job by user scope: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) FindLiveActivityJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error) {
	query := `
		SELECT id, activity_type, COALESCE(user_id, ''), COALESCE(topic_id, ''), status, latest_payload, COALESCE(options, '{}'::jsonb), created_at, updated_at, expires_at, closed_at
		FROM live_activity_jobs
		WHERE activity_type = $1
		  AND topic_id = $2
		  AND user_id IS NULL
		  AND status IN ('active', 'closing')
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY updated_at DESC
		LIMIT 1`

	row := s.db.QueryRowContext(ctx, query, activityType, topicID)
	var job LiveActivityJob
	if err := scanLiveActivityJob(row, &job); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error finding live activity job by topic scope: %w", err)
	}
	return &job, nil
}

func (s *PostgresStore) UpdateLiveActivityJob(ctx context.Context, job *LiveActivityJob) error {
	query := `
		UPDATE live_activity_jobs
		SET status = $1,
			latest_payload = $2,
			options = $3,
			updated_at = $4,
			expires_at = $5,
			closed_at = $6
		WHERE id = $7`

	result, err := s.db.ExecContext(
		ctx,
		query,
		job.Status,
		job.LatestPayload,
		job.Options,
		job.UpdatedAt,
		nullableString(job.ExpiresAt),
		nullableString(job.ClosedAt),
		job.ID,
	)
	if err != nil {
		return fmt.Errorf("error updating live activity job: %w", err)
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
				 SET status = 'closed', closed_at = NOW(), updated_at = NOW()
				 WHERE id = $1`,
				jobID,
			); err != nil {
				return fmt.Errorf("error closing live activity job for empty end dispatch: %w", err)
			}
		}

		if action == string(model.LiveActivityActionStart) {
			if _, err := s.db.ExecContext(
				ctx,
				`UPDATE live_activity_jobs
				 SET status = 'failed', closed_at = NOW(), updated_at = NOW()
				 WHERE id = $1`,
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
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("live_activity_dispatch_attempts", "id", "dispatch_id", "live_activity_token_id", "platform", "status", "reason", "sent_at"))
	if err != nil {
		return fmt.Errorf("error preparing live activity dispatch attempts bulk insert: %w", err)
	}

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

		if _, err := stmt.ExecContext(
			ctx,
			outcome.Receipt.ID,
			dispatchID,
			outcome.Receipt.TokenID,
			outcome.Task.Target.Platform,
			outcome.Receipt.Status,
			nullableString(outcome.Receipt.StatusReason),
			outcome.Receipt.DispatchedAt,
		); err != nil {
			stmt.Close()
			return fmt.Errorf("error inserting live activity dispatch attempt: %w", err)
		}

		if outcome.Receipt.Status == string(model.Failed) && isLiveActivityTokenInvalid(outcome.Receipt.StatusReason) {
			invalidTokenIDs[outcome.Receipt.TokenID] = struct{}{}
		}
	}

	if _, err := stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return fmt.Errorf("error finalizing live activity dispatch attempts bulk insert: %w", err)
	}
	if err := stmt.Close(); err != nil {
		return fmt.Errorf("error closing live activity dispatch attempts bulk insert: %w", err)
	}

	for tokenID := range invalidTokenIDs {
		result, err := tx.ExecContext(
			ctx,
			`DELETE FROM live_activity_tokens WHERE id = $1 AND token_type = 'update'`,
			tokenID,
		)
		if err != nil {
			return fmt.Errorf("error deleting live activity update token: %w", err)
		}

		rowsAffected, _ := result.RowsAffected()
		if rowsAffected > 0 {
			continue
		}

		if _, err := tx.ExecContext(
			ctx,
			`UPDATE live_activity_tokens SET invalidated_at = NOW() WHERE id = $1 AND invalidated_at IS NULL`,
			tokenID,
		); err != nil {
			return fmt.Errorf("error invalidating live activity start token: %w", err)
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
				 SET status = 'closed', closed_at = NOW(), updated_at = NOW()
				 WHERE id = $1`,
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
