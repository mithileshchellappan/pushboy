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
	if activityID == "" {
		return nil, false, errors.New("activityId is required")
	}
	if topicID == "" {
		return nil, false, errors.New("topicId is required")
	}

	channel, created, err := s.store.EnsureLAChannel(
		ctx,
		activityID,
		topicID,
		func(ctx context.Context) (string, error) {
			if s.laChannelProvider == nil {
				return "", ErrLAChannelUnavailable
			}
			channelID, err := s.laChannelProvider.CreateLiveActivityChannel(ctx)
			if err != nil {
				return "", fmt.Errorf("%w: create APNs live activity channel: %w", ErrLAChannelProviderFailed, err)
			}
			if channelID == "" {
				return "", fmt.Errorf("%w: APNs returned an empty channel ID", ErrLAChannelProviderFailed)
			}
			return channelID, nil
		},
	)
	if err != nil {
		if errors.Is(err, storage.Errors.Conflict) {
			return nil, false, ErrLAChannelConflict
		}
		return nil, false, err
	}
	if channel.TopicID != topicID {
		return nil, false, ErrLAChannelConflict
	}
	return channel, created, nil
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
