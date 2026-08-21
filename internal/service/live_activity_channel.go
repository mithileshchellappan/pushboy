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

type LAChannelClient interface {
	CreateLiveActivityChannel(ctx context.Context) (string, error)
	DeleteLiveActivityChannel(ctx context.Context, channelID string) error
}

// Channel deletion is one locked remote-and-local operation. A detached timeout
// lets it finish after the HTTP caller disconnects and bounds APNs semaphore
// waiting plus its 10-second HTTP timeout. Remote success immediately before
// this deadline still has the unavoidable risk of a failed database commit.
const laChannelDeleteTimeout = 30 * time.Second

func laChannelDeleteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), laChannelDeleteTimeout)
}

func (s *PushboyService) createLAChannel(ctx context.Context) (string, error) {
	if s.laChannelClient == nil {
		return "", ErrLAChannelUnavailable
	}
	channelID, err := s.laChannelClient.CreateLiveActivityChannel(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: create APNs live activity channel: %w", ErrLAChannelProviderFailed, err)
	}
	if channelID == "" {
		return "", fmt.Errorf("%w: APNs returned an empty channel ID", ErrLAChannelProviderFailed)
	}
	return channelID, nil
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
		s.createLAChannel,
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
	if s.laChannelClient == nil {
		return ErrLAChannelUnavailable
	}
	if activityID == "" {
		return errors.New("activityId is required")
	}

	deleteCtx, cancel := laChannelDeleteContext(ctx)
	defer cancel()
	return s.store.DeleteLAChannel(
		deleteCtx,
		activityID,
		func(ctx context.Context, channelID string) error {
			if err := s.laChannelClient.DeleteLiveActivityChannel(ctx, channelID); err != nil {
				return fmt.Errorf("%w: delete APNs live activity channel: %w", ErrLAChannelProviderFailed, err)
			}
			return nil
		},
	)
}
