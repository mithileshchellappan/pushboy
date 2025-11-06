package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

type SQLStore struct {
	db *sql.DB
}

func NewSQLStore(dataSourceName string) (*SQLStore, error) {
	db, err := sql.Open("sqlite3", "./db.sqlite")
	if err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}

	driver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return nil, fmt.Errorf("could not create migration driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://db/migrations",
		"sqlite",
		driver,
	)

	if err != nil {
		return nil, fmt.Errorf("Could not create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return nil, fmt.Errorf("could not run database migrations: %w", err)
	}

	log.Println("Migration success!")

	store := &SQLStore{db: db}

	log.Println("Database connection and migration successful")
	return store, nil
}

func (s *SQLStore) CreateTopic(ctx context.Context, topic *Topic) error {
	topic.ID = fmt.Sprintf("psby:%s", strings.ToLower(topic.Name))

	query := "INSERT INTO topics(id, name) VALUES(?, ?)"

	_, err := s.db.ExecContext(ctx, query, topic.ID, topic.Name)

	if err != nil {
		return err
	}

	return nil

}

func (s *SQLStore) ListTopics(ctx context.Context) ([]Topic, error) {
	var topics []Topic
	query := "SELECT id, name FROM topics"

	rows, err := s.db.Query(query)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ID string
		var Name string

		err = rows.Scan(&ID, &Name)

		if err != nil {
			return nil, err
		}
		topics = append(topics, Topic{ID, Name})
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return topics, nil

}

func (s *SQLStore) GetTopicByID(ctx context.Context, topicID string) (*Topic, error) {
	var topic Topic
	query := "SELECT id, name FROM topics WHERE id = ? "

	row := s.db.QueryRowContext(ctx, query, topicID)

	err := row.Scan(&topic.ID, &topic.Name)

	if err != nil {
		return nil, err
	}

	if topic.ID == "" {
		return nil, fmt.Errorf("Topic not found")
	}

	return &topic, nil
}

func (s *SQLStore) DeleteTopic(ctx context.Context, topicID string) error {
	query := "DELETE FROM topics WHERE id = ?"

	result, err := s.db.ExecContext(ctx, query, topicID)

	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()

	if rowsAffected == 0 {
		return fmt.Errorf("Topic not found")
	}

	return nil
}

func (s *SQLStore) SubscribeToTopic(ctx context.Context, sub *Subscription) (*Subscription, error) {
	sub.ID = fmt.Sprintf("sub:%s:%s", sub.TopicID, sub.Token)
	sub.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	subSql := `INSERT INTO subscriptions(id, topic_id, platform, token, created_at) VALUES(?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, subSql, sub.ID, sub.TopicID, sub.Platform, sub.Token, sub.CreatedAt)

	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error Subscribing to topic creation: %w", err)
	}

	return sub, nil
}

func (s *SQLStore) CreatePublishJob(ctx context.Context, topicID string) (*PublishJob, error) {

	var totalCount int
	countQuery := "SELECT COUNT(*) FROM subscriptions WHERE topic_id = ?"
	row := s.db.QueryRowContext(ctx, countQuery, topicID)
	if err := row.Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("Subscription unavailable: %w", err)
	}

	job := &PublishJob{
		ID:           uuid.New().String(),
		TopicID:      topicID,
		Status:       "PENDING",
		TotalCount:   totalCount,
		SuccessCount: 0,
		FailureCount: 0,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	query := `INSERT INTO publish_jobs(id, topic_id, status, total_count, success_count, failure_count, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, job.ID, job.TopicID, job.Status, job.TotalCount, job.SuccessCount, job.FailureCount, job.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("error creating publish job: %w", err)
	}
	return job, nil
}
