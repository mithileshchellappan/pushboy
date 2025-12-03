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
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLStore(dataSourceName string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}

	// Enable foreign keys
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		return nil, fmt.Errorf("could not enable foreign keys: %w", err)
	}

	driver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return nil, fmt.Errorf("could not create migration driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://db/migrations/sqlite",
		"sqlite",
		driver,
	)

	if err != nil {
		return nil, fmt.Errorf("could not create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return nil, fmt.Errorf("could not run database migrations: %w", err)
	}

	log.Println("Migration success!")

	store := &SQLiteStore{db: db}

	log.Println("Database connection and migration successful")
	return store, nil
}

// User operations

func (s *SQLiteStore) CreateUser(ctx context.Context, user *User) (*User, error) {
	user.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	query := `INSERT INTO users(id, created_at) VALUES(?, ?)`
	_, err := s.db.ExecContext(ctx, query, user.ID, user.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating user: %w", err)
	}

	return user, nil
}

func (s *SQLiteStore) GetUser(ctx context.Context, userID string) (*User, error) {
	query := `SELECT id, created_at FROM users WHERE id = ?`
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

func (s *SQLiteStore) DeleteUser(ctx context.Context, userID string) error {
	query := `DELETE FROM users WHERE id = ?`
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

func (s *SQLiteStore) CreateToken(ctx context.Context, token *Token) (*Token, error) {
	token.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	query := `INSERT INTO tokens(id, user_id, platform, token, created_at) VALUES(?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, token.ID, token.UserID, token.Platform, token.Token, token.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating token: %w", err)
	}

	return token, nil
}

func (s *SQLiteStore) GetTokensByUserID(ctx context.Context, userID string) ([]Token, error) {
	query := `SELECT id, user_id, platform, token, created_at FROM tokens WHERE user_id = ?`
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

func (s *SQLiteStore) DeleteToken(ctx context.Context, tokenID string) error {
	query := `DELETE FROM tokens WHERE id = ?`
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

func (s *SQLiteStore) CreateTopic(ctx context.Context, topic *Topic) error {
	topic.ID = fmt.Sprintf("topic:%s", strings.ToLower(topic.Name))

	query := `INSERT INTO topics(id, name) VALUES(?, ?)`
	_, err := s.db.ExecContext(ctx, query, topic.ID, topic.Name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Errors.AlreadyExists
		}
		return fmt.Errorf("error creating topic: %w", err)
	}

	return nil
}

func (s *SQLiteStore) ListTopics(ctx context.Context) ([]Topic, error) {
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

func (s *SQLiteStore) GetTopicByID(ctx context.Context, topicID string) (*Topic, error) {
	query := `SELECT id, name FROM topics WHERE id = ?`
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

func (s *SQLiteStore) DeleteTopic(ctx context.Context, topicID string) error {
	query := `DELETE FROM topics WHERE id = ?`
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

func (s *SQLiteStore) SubscribeUserToTopic(ctx context.Context, sub *UserTopicSubscription) (*UserTopicSubscription, error) {
	sub.ID = fmt.Sprintf("sub:%s:%s", sub.UserID, sub.TopicID)
	sub.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	query := `INSERT INTO user_topic_subscriptions(id, user_id, topic_id, created_at) VALUES(?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, sub.ID, sub.UserID, sub.TopicID, sub.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error subscribing user to topic: %w", err)
	}

	return sub, nil
}

func (s *SQLiteStore) UnsubscribeUserFromTopic(ctx context.Context, userID, topicID string) error {
	query := `DELETE FROM user_topic_subscriptions WHERE user_id = ? AND topic_id = ?`
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

func (s *SQLiteStore) GetTokenBatchForTopic(ctx context.Context, topicID string, cursor string, batchSize int) (*TokenBatch, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *SQLiteStore) GetUserSubscriptions(ctx context.Context, userID string) ([]UserTopicSubscription, error) {
	query := `SELECT id, user_id, topic_id, created_at FROM user_topic_subscriptions WHERE user_id = ?`
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

func (s *SQLiteStore) GetTopicSubscribers(ctx context.Context, topicID string) ([]User, error) {
	query := `SELECT u.id, u.created_at FROM users u 
		INNER JOIN user_topic_subscriptions s ON u.id = s.user_id 
		WHERE s.topic_id = ?`
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

// Publish job operations

func (s *SQLiteStore) CreatePublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error) {
	// Count subscribers for this topic
	var totalCount int
	countQuery := `SELECT COUNT(*) FROM user_topic_subscriptions WHERE topic_id = ?`
	row := s.db.QueryRowContext(ctx, countQuery, job.TopicID)
	if err := row.Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("error counting subscribers: %w", err)
	}
	job.TotalCount = totalCount

	// Serialize payload to JSON
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return nil, fmt.Errorf("error serializing payload: %w", err)
	}

	query := `INSERT INTO publish_jobs(id, topic_id, payload, status, total_count, success_count, failure_count, created_at) 
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, query, job.ID, job.TopicID, payloadJSON, job.Status, job.TotalCount, job.SuccessCount, job.FailureCount, job.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("error creating publish job: %w", err)
	}

	return job, nil
}

func (s *SQLiteStore) CreateUserPublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error) {
	// Verify user exists
	var userExists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = ?)`, job.UserID).Scan(&userExists)
	if err != nil {
		return nil, fmt.Errorf("error checking user existence: %w", err)
	}
	if !userExists {
		return nil, Errors.NotFound
	}

	// Count user's tokens for total_count
	var totalCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE user_id = ?`, job.UserID).Scan(&totalCount)
	if err != nil {
		return nil, fmt.Errorf("error counting user tokens: %w", err)
	}
	job.TotalCount = totalCount

	// Serialize payload to JSON
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return nil, fmt.Errorf("error serializing payload: %w", err)
	}

	query := `INSERT INTO publish_jobs(id, user_id, payload, status, total_count, success_count, failure_count, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, query, job.ID, job.UserID, payloadJSON, job.Status, job.TotalCount, job.SuccessCount, job.FailureCount, job.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("error creating user publish job: %w", err)
	}
	return job, nil
}

func (s *SQLiteStore) FetchPendingJobs(ctx context.Context, limit int) ([]PublishJob, error) {
	query := `SELECT id, COALESCE(topic_id, ''), COALESCE(user_id, ''), payload, status, total_count, success_count, failure_count, created_at 
		FROM publish_jobs WHERE status = 'PENDING' LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("error fetching pending jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PublishJob
	for rows.Next() {
		var job PublishJob
		var payloadJSON []byte
		if err := rows.Scan(&job.ID, &job.TopicID, &job.UserID, &payloadJSON, &job.Status, &job.TotalCount, &job.SuccessCount, &job.FailureCount, &job.CreatedAt); err != nil {
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

func (s *SQLiteStore) UpdateJobStatus(ctx context.Context, jobID string, status string) error {
	query := `UPDATE publish_jobs SET status = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, status, jobID)
	return err
}

func (s *SQLiteStore) GetJobStatus(ctx context.Context, jobID string) (*PublishJob, error) {
	query := `SELECT id, COALESCE(topic_id, ''), COALESCE(user_id, ''), payload, status, total_count, success_count, failure_count, created_at 
		FROM publish_jobs WHERE id = ?`
	row := s.db.QueryRowContext(ctx, query, jobID)

	var job PublishJob
	var payloadJSON []byte
	err := row.Scan(&job.ID, &job.TopicID, &job.UserID, &payloadJSON, &job.Status, &job.TotalCount, &job.SuccessCount, &job.FailureCount, &job.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, Errors.NotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error getting job status: %w", err)
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

func (s *SQLiteStore) IncrementJobCounters(ctx context.Context, jobID string, success int, failure int) error {
	query := `UPDATE publish_jobs SET success_count = success_count + ?, failure_count = failure_count + ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, success, failure, jobID)
	return err
}

// Delivery receipt operations

func (s *SQLiteStore) RecordDeliveryReceipt(ctx context.Context, receipt *DeliveryReceipt) error {
	query := `INSERT INTO delivery_receipts(id, job_id, token_id, status, status_reason, dispatched_at) VALUES(?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, receipt.ID, receipt.JobID, receipt.TokenID, receipt.Status, receipt.StatusReason, receipt.DispatchedAt)
	return err
}

func (s *SQLiteStore) BulkInsertReceipts(ctx context.Context, receipts []DeliveryReceipt) error {
	if len(receipts) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `INSERT INTO delivery_receipts(id, job_id, token_id, status, status_reason, dispatched_at) VALUES(?, ?, ?, ?, ?, ?)`
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range receipts {
		_, err := stmt.ExecContext(ctx, r.ID, r.JobID, r.TokenID, r.Status, r.StatusReason, r.DispatchedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
