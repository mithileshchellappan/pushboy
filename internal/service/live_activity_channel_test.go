package service

import (
	"context"
	"errors"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestProvisionLAChannelPreservesOpaqueIDs(t *testing.T) {
	store := &channelStoreStub{}
	provider := &channelProviderStub{createID: "channel-1"}
	service := NewPushBoyService(store, "", provider)

	channel, created, err := service.ProvisionLAChannel(
		context.Background(),
		" race-1 ",
		" topic-1 ",
	)
	if err != nil {
		t.Fatalf("ProvisionLAChannel error = %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if channel.ActivityID != " race-1 " || channel.TopicID != " topic-1 " {
		t.Fatalf("channel IDs = (%q, %q), want opaque input", channel.ActivityID, channel.TopicID)
	}
	if store.topicID != " topic-1 " || store.lookupActivityID != " race-1 " {
		t.Fatalf("store IDs = (%q, %q), want opaque input", store.lookupActivityID, store.topicID)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("EnsureLAChannel calls = %d, want 1", store.ensureCalls)
	}
}

func TestProvisionLAChannelReturnsExistingMappingWithoutProvider(t *testing.T) {
	store := &channelStoreStub{channel: &storage.LiveActivityChannel{
		ActivityID: "race-1",
		TopicID:    "topic-1",
		ChannelID:  "channel-1",
	}}
	service := NewPushBoyService(store, "", nil)

	channel, created, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if err != nil {
		t.Fatalf("ProvisionLAChannel error = %v", err)
	}
	if created || channel.ChannelID != "channel-1" {
		t.Fatalf("result = (%+v, %v), want existing mapping", channel, created)
	}
}

func TestProvisionLAChannelRejectsDifferentTopic(t *testing.T) {
	store := &channelStoreStub{channel: &storage.LiveActivityChannel{
		ActivityID: "race-1",
		TopicID:    "topic-1",
		ChannelID:  "channel-1",
	}}
	service := NewPushBoyService(store, "", &channelProviderStub{})

	_, _, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-2")
	if !errors.Is(err, ErrLAChannelConflict) {
		t.Fatalf("ProvisionLAChannel error = %v, want conflict", err)
	}
}

func TestDeleteLAChannelDetachesOperationFromRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleanupCalled := false
	store := &channelStoreStub{
		channel: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-1",
			ChannelID:  "channel-1",
		},
		deleteFunc: func(
			ctx context.Context,
			activityID string,
			deleteRemote func(context.Context, string) error,
		) error {
			if ctx.Err() != nil {
				t.Fatalf("cleanup context error = %v, want detached context", ctx.Err())
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("cleanup context has no deadline")
			}
			if activityID != "race-1" {
				t.Fatalf("deleted activity = %q", activityID)
			}
			if err := deleteRemote(ctx, "channel-1"); err != nil {
				return err
			}
			cleanupCalled = true
			return nil
		},
	}
	service := NewPushBoyService(
		store,
		"",
		&channelProviderStub{onDelete: func(deleteCtx context.Context) {
			cancel()
			if deleteCtx.Err() != nil {
				t.Fatalf("provider context error = %v, want detached context", deleteCtx.Err())
			}
			if _, ok := deleteCtx.Deadline(); !ok {
				t.Fatal("provider context has no deadline")
			}
		}},
	)

	if err := service.DeleteLAChannel(ctx, "race-1"); err != nil {
		t.Fatalf("DeleteLAChannel error = %v", err)
	}
	if !cleanupCalled {
		t.Fatal("local mapping cleanup was not called")
	}
}

type channelStoreStub struct {
	storage.Store
	channel          *storage.LiveActivityChannel
	ensureCalls      int
	createError      error
	topicID          string
	lookupActivityID string
	deleteFunc       func(context.Context, string, func(context.Context, string) error) error
}

func (s *channelStoreStub) EnsureLAChannel(
	ctx context.Context,
	activityID string,
	topicID string,
	create func(context.Context) (string, error),
) (*storage.LiveActivityChannel, bool, error) {
	s.ensureCalls++
	s.lookupActivityID = activityID
	s.topicID = topicID
	if s.channel != nil {
		return s.channel, false, nil
	}
	if s.createError != nil {
		return nil, false, s.createError
	}
	channelID, err := create(ctx)
	if err != nil {
		return nil, false, err
	}
	s.channel = &storage.LiveActivityChannel{
		ActivityID: activityID,
		TopicID:    topicID,
		ChannelID:  channelID,
	}
	return s.channel, true, nil
}

func (s *channelStoreStub) GetTopicByID(_ context.Context, topicID string) (*storage.Topic, error) {
	s.topicID = topicID
	return &storage.Topic{ID: topicID}, nil
}

func (s *channelStoreStub) GetLAChannelByActivityID(
	_ context.Context,
	activityID string,
) (*storage.LiveActivityChannel, error) {
	s.lookupActivityID = activityID
	if s.channel == nil {
		return nil, storage.Errors.NotFound
	}
	return s.channel, nil
}

func (s *channelStoreStub) DeleteLAChannel(
	ctx context.Context,
	activityID string,
	deleteRemote func(context.Context, string) error,
) error {
	return s.deleteFunc(ctx, activityID, deleteRemote)
}

type channelProviderStub struct {
	createID    string
	createError error
	createCalls int
	onCreate    func()
	onDelete    func(context.Context)
}

func (p *channelProviderStub) CreateLiveActivityChannel(context.Context) (string, error) {
	p.createCalls++
	if p.onCreate != nil {
		p.onCreate()
	}
	return p.createID, p.createError
}

func (p *channelProviderStub) DeleteLiveActivityChannel(ctx context.Context, _ string) error {
	if p.onDelete != nil {
		p.onDelete(ctx)
	}
	return nil
}
