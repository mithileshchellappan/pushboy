package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

var (
	ErrLAChannelUnavailable    = errors.New("live activity channel provider is unavailable")
	ErrLAChannelConflict       = errors.New("live activity channel topic conflicts with the existing mapping")
	ErrLAChannelLookupFailed   = errors.New("live activity channel lookup failed")
	ErrLAChannelProviderFailed = errors.New("live activity channel provider failed")
)

type LAChannelProvider interface {
	CreateLiveActivityChannel(ctx context.Context) (string, error)
	DeleteLiveActivityChannel(ctx context.Context, channelID string) error
}

const laChannelCleanupTimeout = 5 * time.Second

func laChannelCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), laChannelCleanupTimeout)
}

func WithLAChannels(provider LAChannelProvider) Option {
	return func(service *PushboyService) {
		service.laChannelProvider = provider
	}
}

func (s *PushboyService) ProvisionLAChannel(
	ctx context.Context,
	activityID string,
	topicID string,
) (*storage.LiveActivityChannel, bool, error) {
	if s.laChannelProvider == nil {
		return nil, false, ErrLAChannelUnavailable
	}
	if activityID == "" {
		return nil, false, errors.New("activityId is required")
	}
	if topicID == "" {
		return nil, false, errors.New("topicId is required")
	}

	existing, err := s.store.GetLAChannelByActivityID(ctx, activityID)
	if err == nil {
		if existing.TopicID != topicID {
			return nil, false, ErrLAChannelConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, storage.Errors.NotFound) {
		return nil, false, err
	}
	if _, err := s.requireTopic(ctx, topicID); err != nil {
		return nil, false, err
	}

	channelID, err := s.laChannelProvider.CreateLiveActivityChannel(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("%w: create APNs live activity channel: %w", ErrLAChannelProviderFailed, err)
	}
	if channelID == "" {
		return nil, false, fmt.Errorf("%w: APNs returned an empty channel ID", ErrLAChannelProviderFailed)
	}

	channel := &storage.LiveActivityChannel{
		ActivityID: activityID,
		TopicID:    topicID,
		ChannelID:  channelID,
		CreatedAt:  time.Now().UTC(),
	}
	stored, created, err := s.store.CreateOrGetLAChannel(ctx, channel)
	if err != nil {
		reconcileCtx, cancel := laChannelCleanupContext(ctx)
		reconciled, _, reconcileErr := s.store.CreateOrGetLAChannel(reconcileCtx, channel)
		cancel()
		if reconcileErr != nil {
			return nil, false, errors.Join(
				fmt.Errorf("live activity channel %q persistence outcome is unknown: %w", channelID, err),
				fmt.Errorf("retry live activity channel persistence: %w", reconcileErr),
			)
		}
		stored = reconciled
		created = stored.ChannelID == channelID
	}
	if stored.ChannelID != channelID {
		cleanupCtx, cancel := laChannelCleanupContext(ctx)
		cleanupErr := s.laChannelProvider.DeleteLiveActivityChannel(cleanupCtx, channelID)
		cancel()
		if cleanupErr != nil {
			return nil, false, errors.Join(
				err,
				fmt.Errorf(
					"%w: delete unused APNs live activity channel %q: %w",
					ErrLAChannelProviderFailed,
					channelID,
					cleanupErr,
				),
			)
		}
	}
	if stored.TopicID != topicID {
		return nil, false, ErrLAChannelConflict
	}
	return stored, created, nil
}

func (s *PushboyService) GetLAChannel(
	ctx context.Context,
	activityID string,
) (*storage.LiveActivityChannel, error) {
	if activityID == "" {
		return nil, errors.New("activityId is required")
	}
	return s.store.GetLAChannelByActivityID(ctx, activityID)
}

func (s *PushboyService) DeleteLAChannel(ctx context.Context, activityID string) error {
	if s.laChannelProvider == nil {
		return ErrLAChannelUnavailable
	}
	channel, err := s.GetLAChannel(ctx, activityID)
	if err != nil {
		return err
	}
	if err := s.laChannelProvider.DeleteLiveActivityChannel(ctx, channel.ChannelID); err != nil {
		return fmt.Errorf("%w: delete APNs live activity channel: %w", ErrLAChannelProviderFailed, err)
	}
	cleanupCtx, cancel := laChannelCleanupContext(ctx)
	defer cancel()
	return s.store.DeleteLAChannel(cleanupCtx, channel.ActivityID, channel.ChannelID)
}
