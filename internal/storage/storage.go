package storage

import "context"

type Topic struct {
	ID   string
	Name string
}

type Store interface {
	CreateTopic(ctx context.Context, topic *Topic) error
	ListTopics(ctx context.Context) ([]Topic, error)
}
