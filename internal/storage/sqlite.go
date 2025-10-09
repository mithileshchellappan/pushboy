package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

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
	store := &SQLStore{db: db}

	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("could not migrate db: %w", err)
	}

	log.Println("Database connection and migration successful")
	return store, nil
}

func (s *SQLStore) migrate() error {
	topicsQuery := `
	CREATE TABLE IF NOT EXISTS topics (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE
	);
	`
	if _, err := s.db.Exec(topicsQuery); err != nil {
		return err
	}

	subscriptionsQuery := `
		CREATE TABLE IF NOT EXISTS subscriptions (
		id TEXT PRIMARY KEY,
		topic_id TEXT NOT NULL,
		platform TEXT NOT NULL,
		token TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(topic_id) REFERENCES topics(id) ON DELETE CASCADE,
		UNIQUE(topic_id, endpoint)
	);`

	if _, err := s.db.Exec(subscriptionsQuery); err != nil {
		return err
	}

	return nil
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
