package storage

import "context"

type Topic struct {
	ID   string
	Name string
}

type Store interface {
	CreateTopic(ctx context.Context, topic *Topic) error
	ListTopics(ctx context.Context) ([]Topic, error)
	GetTopicByID(ctx context.Context, topicID string) (*Topic, error)
	DeleteTopic(ctx context.Context, topicID string) error
}

type Subscription struct {
	ID        string
	TopicID   string
	Platform  string
	Token     string
	CreatedAt string
}
