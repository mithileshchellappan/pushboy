package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	pq "github.com/lib/pq"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}

	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(50)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not connect to database: %w", err)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("could not create migration driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://db/migrations/postgres",
		"postgres",
		driver,
	)

	if err != nil {
		db.Close()
		return nil, fmt.Errorf("could not create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		db.Close()
		return nil, fmt.Errorf("could not run database migrations: %w", err)
	}

	log.Println("Migration success!")

	store := &PostgresStore{db: db}

	log.Println("Database connection and migration successful")
	return store, nil
}

// User operations

func (s *PostgresStore) CreateUser(ctx context.Context, user *User) (*User, error) {
	user.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	query := `INSERT INTO users(id, created_at) VALUES($1, $2)`
	_, err := s.db.ExecContext(ctx, query, user.ID, user.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating user: %w", err)
	}

	return user, nil
}

func (s *PostgresStore) GetUser(ctx context.Context, userID string) (*User, error) {
	query := `SELECT id, created_at FROM users WHERE id = $1`
	row := s.db.QueryRowContext(ctx, query, userID)

	var user User
	err := row.Scan(&user.ID, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, Errors.NotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error getting user: %w", err)
	}

	return &user, nil
}

func (s *PostgresStore) DeleteUser(ctx context.Context, userID string) error {
	query := `DELETE FROM users WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("error deleting user: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

// Token operations

func (s *PostgresStore) CreateToken(ctx context.Context, token *Token) (*Token, error) {
	token.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	query := `INSERT INTO tokens(id, user_id, platform, token, created_at) VALUES($1, $2, $3, $4, $5)`
	_, err := s.db.ExecContext(ctx, query, token.ID, token.UserID, token.Platform, token.Token, token.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating token: %w", err)
	}

	return token, nil
}

func (s *PostgresStore) GetTokensByUserID(ctx context.Context, userID string) ([]Token, error) {
	query := `SELECT id, user_id, platform, token, created_at FROM tokens WHERE user_id = $1`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error getting tokens: %w", err)
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var token Token
		if err := rows.Scan(&token.ID, &token.UserID, &token.Platform, &token.Token, &token.CreatedAt); err != nil {
			return nil, fmt.Errorf("error scanning token: %w", err)
		}
		tokens = append(tokens, token)
	}

	return tokens, rows.Err()
}

func (s *PostgresStore) GetTokenBatchForTopic(ctx context.Context, topicID string, cursor string, batchSize int) (*TokenBatch, error) {
	query := `
		SELECT t.id, t.token, t.platform 
		FROM tokens t
		WHERE t.user_id IN (
			SELECT user_id FROM user_topic_subscriptions WHERE topic_id = $1
		) 
		AND t.id > $2 
		ORDER BY t.id 
		LIMIT $3`
	rows, err := s.db.QueryContext(ctx, query, topicID, cursor, batchSize+1)
	if err != nil {
		return nil, fmt.Errorf("error getting token batch for topic: %w", err)
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var token Token
		if err := rows.Scan(&token.ID, &token.Token, &token.Platform); err != nil {
			return nil, fmt.Errorf("error scanning token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error getting token batch for topic: %w", err)
	}

	batch := &TokenBatch{
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

func (s *PostgresStore) GetTokenBatchForUser(ctx context.Context, userID string, cursor string, batchSize int) (*TokenBatch, error) {
	query := `SELECT id, token, platform FROM tokens WHERE tokens.user_id = $1 AND tokens.id > $2 ORDER BY tokens.id LIMIT $3 `
	rows, err := s.db.QueryContext(ctx, query, userID, cursor, batchSize+1)
	if err != nil {
		return nil, fmt.Errorf("error getting token batch for topic: %w", err)
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var token Token
		if err := rows.Scan(&token.ID, &token.Token, &token.Platform); err != nil {
			return nil, fmt.Errorf("error scanning token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error getting token batch for topic: %w", err)
	}

	batch := &TokenBatch{
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

func (s *PostgresStore) DeleteToken(ctx context.Context, tokenID string) error {
	query := `DELETE FROM tokens WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query, tokenID)
	if err != nil {
		return fmt.Errorf("error deleting token: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

// Topic operations

func (s *PostgresStore) CreateTopic(ctx context.Context, topic *Topic) error {

	query := `INSERT INTO topics(id, name) VALUES($1, $2)`
	_, err := s.db.ExecContext(ctx, query, topic.ID, topic.Name)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return Errors.AlreadyExists
		}
		return fmt.Errorf("error creating topic: %w", err)
	}

	return nil
}

func (s *PostgresStore) ListTopics(ctx context.Context) ([]Topic, error) {
	query := `SELECT id, name FROM topics`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error listing topics: %w", err)
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var topic Topic
		if err := rows.Scan(&topic.ID, &topic.Name); err != nil {
			return nil, fmt.Errorf("error scanning topic: %w", err)
		}
		topics = append(topics, topic)
	}

	return topics, rows.Err()
}

func (s *PostgresStore) GetTopicByID(ctx context.Context, topicID string) (*Topic, error) {
	query := `SELECT id, name FROM topics WHERE id = $1`
	row := s.db.QueryRowContext(ctx, query, topicID)

	var topic Topic
	err := row.Scan(&topic.ID, &topic.Name)
	if err == sql.ErrNoRows {
		return nil, Errors.NotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error getting topic: %w", err)
	}

	return &topic, nil
}

func (s *PostgresStore) DeleteTopic(ctx context.Context, topicID string) error {
	query := `DELETE FROM topics WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query, topicID)
	if err != nil {
		return fmt.Errorf("error deleting topic: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

// User-Topic subscription operations

func (s *PostgresStore) SubscribeUserToTopic(ctx context.Context, sub *UserTopicSubscription) (*UserTopicSubscription, error) {
	sub.ID = fmt.Sprintf("sub:%s:%s", sub.UserID, sub.TopicID)
	sub.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	query := `INSERT INTO user_topic_subscriptions(id, user_id, topic_id, created_at) VALUES($1, $2, $3, $4)`
	_, err := s.db.ExecContext(ctx, query, sub.ID, sub.UserID, sub.TopicID, sub.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error subscribing user to topic: %w", err)
	}

	return sub, nil
}

func (s *PostgresStore) UnsubscribeUserFromTopic(ctx context.Context, userID, topicID string) error {
	query := `DELETE FROM user_topic_subscriptions WHERE user_id = $1 AND topic_id = $2`
	result, err := s.db.ExecContext(ctx, query, userID, topicID)
	if err != nil {
		return fmt.Errorf("error unsubscribing user from topic: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

func (s *PostgresStore) GetUserSubscriptions(ctx context.Context, userID string) ([]UserTopicSubscription, error) {
	query := `SELECT id, user_id, topic_id, created_at FROM user_topic_subscriptions WHERE user_id = $1`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error getting user subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []UserTopicSubscription
	for rows.Next() {
		var sub UserTopicSubscription
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.TopicID, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("error scanning subscription: %w", err)
		}
		subs = append(subs, sub)
	}

	return subs, rows.Err()
}

func (s *PostgresStore) GetTopicSubscribers(ctx context.Context, topicID string) ([]User, error) {
	query := `SELECT u.id, u.created_at FROM users u
		INNER JOIN user_topic_subscriptions s ON u.id = s.user_id
		WHERE s.topic_id = $1`
	rows, err := s.db.QueryContext(ctx, query, topicID)
	if err != nil {
		return nil, fmt.Errorf("error getting topic subscribers: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.CreatedAt); err != nil {
			return nil, fmt.Errorf("error scanning user: %w", err)
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

func (s *PostgresStore) GetTopicSubscriberCount(ctx context.Context, topicID string) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM user_topic_subscriptions WHERE topic_id = $1`
	err := s.db.QueryRowContext(ctx, query, topicID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("error counting topic subscribers: %w", err)
	}
	return count, nil
}

// Publish job operations

func (s *PostgresStore) CreatePublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error) {
	// Count tokens for this topic (not subscribers, since one user can have multiple tokens)
	var totalCount int
	countQuery := `
		SELECT COUNT(*) FROM tokens t
		WHERE t.user_id IN (
			SELECT user_id FROM user_topic_subscriptions WHERE topic_id = $1
		)`
	row := s.db.QueryRowContext(ctx, countQuery, job.TopicID)
	if err := row.Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("error counting tokens: %w", err)
	}
	job.TotalCount = totalCount

	// Serialize payload to JSON
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return nil, fmt.Errorf("error serializing payload: %w", err)
	}

	var scheduledAt any
	if job.ScheduledAt == "" {
		scheduledAt = nil
	} else {
		scheduledAt = job.ScheduledAt
	}

	query := `INSERT INTO publish_jobs(id, topic_id, payload, status, total_count, success_count, failure_count, created_at, scheduled_at) 
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	_, err = s.db.ExecContext(ctx, query, job.ID, job.TopicID, payloadJSON, job.Status, job.TotalCount, job.SuccessCount, job.FailureCount, job.CreatedAt, scheduledAt)
	if err != nil {
		return nil, fmt.Errorf("error creating publish job: %w", err)
	}

	return job, nil
}

func (s *PostgresStore) CreateUserPublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error) {
	// Verify user exists
	var userExists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, job.UserID).Scan(&userExists)
	if err != nil {
		return nil, fmt.Errorf("error checking user existence: %w", err)
	}
	if !userExists {
		return nil, Errors.NotFound
	}

	// Count user's tokens for total_count
	var totalCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE user_id = $1`, job.UserID).Scan(&totalCount)
	if err != nil {
		return nil, fmt.Errorf("error counting user tokens: %w", err)
	}
	job.TotalCount = totalCount

	// Serialize payload to JSON
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return nil, fmt.Errorf("error serializing payload: %w", err)
	}

	var scheduledAt any
	if job.ScheduledAt == "" {
		scheduledAt = nil
	} else {
		scheduledAt = job.ScheduledAt
	}

	query := `INSERT INTO publish_jobs(id, user_id, payload, status, total_count, success_count, failure_count, created_at, scheduled_at) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	_, err = s.db.ExecContext(ctx, query, job.ID, job.UserID, payloadJSON, job.Status, job.TotalCount, job.SuccessCount, job.FailureCount, job.CreatedAt, scheduledAt)
	if err != nil {
		return nil, fmt.Errorf("error creating user publish job: %w", err)
	}
	return job, nil
}

func (s *PostgresStore) FetchPendingJobs(ctx context.Context, limit int) ([]PublishJob, error) {
	query := `SELECT id, COALESCE(topic_id, ''), COALESCE(user_id, ''), payload, status, total_count, success_count, failure_count, created_at, completed_at 
		FROM publish_jobs WHERE status = 'PENDING' LIMIT $1`
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("error fetching pending jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PublishJob
	for rows.Next() {
		var job PublishJob
		var payloadJSON []byte
		var completedAt sql.NullTime
		if err := rows.Scan(&job.ID, &job.TopicID, &job.UserID, &payloadJSON, &job.Status, &job.TotalCount, &job.SuccessCount, &job.FailureCount, &job.CreatedAt, &completedAt); err != nil {
			return nil, fmt.Errorf("error scanning job: %w", err)
		}
		if completedAt.Valid {
			job.CompletedAt = completedAt.Time.UTC().Format(time.RFC3339)
		}
		// Deserialize payload from JSON
		if len(payloadJSON) > 0 {
			var payload NotificationPayload
			if err := json.Unmarshal(payloadJSON, &payload); err != nil {
				return nil, fmt.Errorf("error deserializing payload: %w", err)
			}
			job.Payload = &payload
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

func (s *PostgresStore) UpdateJobStatus(ctx context.Context, jobID string, status string) error {
	var query string
	if status == "COMPLETED" {
		query = `UPDATE publish_jobs SET status = $1, completed_at = NOW() WHERE id = $2`
	} else {
		query = `UPDATE publish_jobs SET status = $1 WHERE id = $2`
	}
	_, err := s.db.ExecContext(ctx, query, status, jobID)
	return err
}

func (s *PostgresStore) GetJobStatus(ctx context.Context, jobID string) (*PublishJob, error) {
	query := `
		SELECT 
			pj.id, 
			COALESCE(pj.topic_id, ''), 
			COALESCE(pj.user_id, ''), 
			pj.payload, 
			pj.status, 
			pj.total_count, 
			COALESCE(SUM(CASE WHEN dr.status = 'SUCCESS' THEN 1 ELSE 0 END), 0) as success_count,
			COALESCE(SUM(CASE WHEN dr.status = 'FAILED' THEN 1 ELSE 0 END), 0) as failure_count,
			pj.created_at,
			pj.completed_at
		FROM publish_jobs pj
		LEFT JOIN delivery_receipts dr ON pj.id = dr.job_id
		WHERE pj.id = $1
		GROUP BY pj.id, pj.topic_id, pj.user_id, pj.payload, pj.status, pj.total_count, pj.created_at, pj.completed_at`

	row := s.db.QueryRowContext(ctx, query, jobID)

	var job PublishJob
	var payloadJSON []byte
	var completedAt sql.NullTime
	err := row.Scan(&job.ID, &job.TopicID, &job.UserID, &payloadJSON, &job.Status, &job.TotalCount, &job.SuccessCount, &job.FailureCount, &job.CreatedAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, Errors.NotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error getting job status: %w", err)
	}

	if completedAt.Valid {
		job.CompletedAt = completedAt.Time.Format(time.RFC3339)
	}

	// Deserialize payload from JSON
	if len(payloadJSON) > 0 {
		var payload NotificationPayload
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, fmt.Errorf("error deserializing payload: %w", err)
		}
		job.Payload = &payload
	}

	return &job, nil
}

func (s *PostgresStore) IncrementJobCounters(ctx context.Context, jobID string, success int, failure int) error {
	query := `UPDATE publish_jobs SET success_count = success_count + $1, failure_count = failure_count + $2 WHERE id = $3`
	_, err := s.db.ExecContext(ctx, query, success, failure, jobID)
	return err
}

// Delivery receipt operations

func (s *PostgresStore) RecordDeliveryReceipt(ctx context.Context, receipt *DeliveryReceipt) error {
	query := `INSERT INTO delivery_receipts(id, job_id, token_id, status, status_reason, dispatched_at) VALUES($1, $2, $3, $4, $5, $6)`
	_, err := s.db.ExecContext(ctx, query, receipt.ID, receipt.JobID, receipt.TokenID, receipt.Status, receipt.StatusReason, receipt.DispatchedAt)
	return err
}

func (s *PostgresStore) BulkInsertReceipts(ctx context.Context, receipts []DeliveryReceipt) error {
	if len(receipts) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("delivery_receipts", "id", "job_id", "token_id", "status", "status_reason", "dispatched_at"))
	if err != nil {
		return err
	}

	for _, r := range receipts {
		_, err := stmt.ExecContext(ctx, r.ID, r.JobID, r.TokenID, r.Status, r.StatusReason, r.DispatchedAt)
		if err != nil {
			stmt.Close()
			return err
		}
	}

	if err := stmt.Close(); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *PostgresStore) GetScheduledJobs(ctx context.Context) ([]PublishJob, error) {
	query := `UPDATE publish_jobs SET status = 'QUEUED' WHERE id IN (SELECT id FROM publish_jobs WHERE status = 'SCHEDULED' AND scheduled_at IS NOT NULL AND scheduled_at <= NOW() FOR UPDATE SKIP LOCKED) RETURNING id, COALESCE(topic_id, ''), COALESCE(user_id, ''), payload, status, total_count, success_count, failure_count, created_at, COALESCE(completed_at::text, ''), COALESCE(scheduled_at::text, '')`

	rows, err := s.db.QueryContext(ctx, query)

	if err != nil {
		return nil, fmt.Errorf("error fetching pending jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PublishJob
	for rows.Next() {
		var job PublishJob
		var payloadJSON []byte
		var completedAt sql.NullString
		if err := rows.Scan(&job.ID, &job.TopicID, &job.UserID, &payloadJSON, &job.Status, &job.TotalCount, &job.SuccessCount, &job.FailureCount, &job.CreatedAt, &completedAt, &job.ScheduledAt); err != nil {
			return nil, fmt.Errorf("error scanning job: %w", err)
		}
		// Deserialize payload from JSON
		if len(payloadJSON) > 0 {
			var payload NotificationPayload
			if err := json.Unmarshal(payloadJSON, &payload); err != nil {
				return nil, fmt.Errorf("error deserializing payload: %w", err)
			}
			job.Payload = &payload
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()

}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}
