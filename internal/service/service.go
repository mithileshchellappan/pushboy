package service

import (
	"context"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type PushboyService struct {
	store storage.Store
}

func NewPushBoyService(s storage.Store) *PushboyService {
	return &PushboyService{store: s}
}

func (s *PushboyService) CreateTopic(ctx context.Context, name string) (*storage.Topic, error) {
	topic := &storage.Topic{Name: name}

	err := s.store.CreateTopic(ctx, topic)
	if err != nil {
		return nil, err
	}

	return topic, nil
}

func (s *PushboyService) ListTopics(ctx context.Context) ([]storage.Topic, error) {
	topics, err := s.store.ListTopics(ctx)

	if err != nil {
		return nil, err
	}

	return topics, nil
}

func (s *PushboyService) GetTopicByID(ctx context.Context, topicID string) (*storage.Topic, error) {
	topic, err := s.store.GetTopicByID(ctx, topicID)

	if err != nil {
		return nil, err
	}

	return topic, nil
}

func (s *PushboyService) DeleteTopic(ctx context.Context, topicID string) error {
	err := s.store.DeleteTopic(ctx, topicID)
	if err != nil {
		return err
	}

	return nil
}
