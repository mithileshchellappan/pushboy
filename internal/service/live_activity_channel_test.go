package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestProvisionLAChannelPreservesOpaqueIDs(t *testing.T) {
	store := &channelStoreStub{}
	provider := &channelProviderStub{createID: "channel-1"}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

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
}

func TestProvisionLAChannelReturnsExistingMappingWithoutCallingAPNS(t *testing.T) {
	store := &channelStoreStub{channel: &storage.LiveActivityChannel{
		ActivityID: "race-1",
		TopicID:    "topic-1",
		ChannelID:  "channel-1",
	}}
	provider := &channelProviderStub{createID: "should-not-be-used"}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	channel, created, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if err != nil {
		t.Fatalf("ProvisionLAChannel error = %v", err)
	}
	if created || channel.ChannelID != "channel-1" {
		t.Fatalf("result = (%+v, %v), want existing mapping", channel, created)
	}
	if provider.createCalls != 0 {
		t.Fatalf("APNs create calls = %d, want 0", provider.createCalls)
	}
}

func TestProvisionLAChannelDeletesConcurrentLosingChannel(t *testing.T) {
	store := &channelStoreStub{
		createResult: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-1",
			ChannelID:  "winner",
		},
		createResultCreated: false,
	}
	provider := &channelProviderStub{createID: "loser"}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	channel, created, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if err != nil {
		t.Fatalf("ProvisionLAChannel error = %v", err)
	}
	if created || channel.ChannelID != "winner" {
		t.Fatalf("result = (%+v, %v), want concurrent winner", channel, created)
	}
	if len(provider.deletedIDs) != 1 || provider.deletedIDs[0] != "loser" {
		t.Fatalf("deleted IDs = %v, want [loser]", provider.deletedIDs)
	}
}

func TestProvisionLAChannelRejectsDifferentTopic(t *testing.T) {
	store := &channelStoreStub{channel: &storage.LiveActivityChannel{
		ActivityID: "race-1",
		TopicID:    "topic-1",
		ChannelID:  "channel-1",
	}}
	service := NewPushBoyService(store, "", WithLAChannels(&channelProviderStub{}))

	_, _, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-2")
	if !errors.Is(err, ErrLAChannelConflict) {
		t.Fatalf("ProvisionLAChannel error = %v, want conflict", err)
	}
}

func TestProvisionLAChannelRecoversConfirmedChannelAfterAmbiguousPersistence(t *testing.T) {
	persistenceErr := errors.New("database connection lost")
	store := &channelStoreStub{
		createError: persistenceErr,
		reconcileResult: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-1",
			ChannelID:  "channel-1",
		},
	}
	provider := &channelProviderStub{createID: "channel-1"}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	channel, created, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if err != nil {
		t.Fatalf("ProvisionLAChannel error = %v", err)
	}
	if !created || channel.ChannelID != "channel-1" {
		t.Fatalf("result = (%+v, %v), want confirmed created mapping", channel, created)
	}
	if store.createCalls != 2 {
		t.Fatalf("CreateOrGetLAChannel calls = %d, want 2", store.createCalls)
	}
	if provider.createCalls != 1 {
		t.Fatalf("APNs create calls = %d, want 1", provider.createCalls)
	}
	if !store.reconcileHadDeadline {
		t.Fatal("persistence retry context has no deadline")
	}
	if len(provider.deletedIDs) != 0 {
		t.Fatalf("deleted IDs = %v, want confirmed channel retained", provider.deletedIDs)
	}
}

func TestProvisionLAChannelConfirmedLoserCleanupSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &channelStoreStub{
		createError: errors.New("database connection lost"),
		reconcileResult: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-1",
			ChannelID:  "winner",
		},
	}
	provider := &channelProviderStub{
		createID: "loser",
		onCreate: cancel,
	}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	channel, created, err := service.ProvisionLAChannel(ctx, "race-1", "topic-1")
	if err != nil {
		t.Fatalf("ProvisionLAChannel error = %v", err)
	}
	if created || channel.ChannelID != "winner" {
		t.Fatalf("result = (%+v, %v), want confirmed concurrent winner", channel, created)
	}
	if len(provider.deletedIDs) != 1 || provider.deletedIDs[0] != "loser" {
		t.Fatalf("deleted IDs = %v, want [loser]", provider.deletedIDs)
	}
	if provider.createCalls != 1 {
		t.Fatalf("APNs create calls = %d, want 1", provider.createCalls)
	}
	if store.reconcileContextErr != nil {
		t.Fatalf("persistence retry context error = %v, want detached context", store.reconcileContextErr)
	}
	if !store.reconcileHadDeadline {
		t.Fatal("persistence retry context has no deadline")
	}
	if provider.deleteContextErr != nil {
		t.Fatalf("cleanup context error = %v, want cleanup detached from request cancellation", provider.deleteContextErr)
	}
	if !provider.deleteHadDeadline {
		t.Fatal("cleanup context has no deadline")
	}
}

func TestProvisionLAChannelRetainsChannelWhenPersistenceRemainsAmbiguous(t *testing.T) {
	persistenceErr := errors.New("database connection lost")
	reconcileErr := errors.New("database retry unavailable")
	store := &channelStoreStub{
		createError:    persistenceErr,
		reconcileError: reconcileErr,
	}
	provider := &channelProviderStub{createID: "possibly-persisted"}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	_, _, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if !errors.Is(err, persistenceErr) {
		t.Fatalf("ProvisionLAChannel error = %v, want initial persistence error", err)
	}
	if !errors.Is(err, reconcileErr) {
		t.Fatalf("ProvisionLAChannel error = %v, want reconciliation error", err)
	}
	if !strings.Contains(err.Error(), "possibly-persisted") {
		t.Fatalf("ProvisionLAChannel error = %v, want retained channel ID", err)
	}
	if len(provider.deletedIDs) != 0 {
		t.Fatalf("deleted IDs = %v, want no deletion while persistence is ambiguous", provider.deletedIDs)
	}
	if provider.createCalls != 1 {
		t.Fatalf("APNs create calls = %d, want 1", provider.createCalls)
	}
	if !store.reconcileHadDeadline {
		t.Fatal("persistence retry context has no deadline")
	}
}

func TestProvisionLAChannelSurfacesPersistenceAndCleanupFailures(t *testing.T) {
	persistenceErr := errors.New("database unavailable")
	cleanupErr := errors.New("APNs unavailable")
	store := &channelStoreStub{
		createError: persistenceErr,
		reconcileResult: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-1",
			ChannelID:  "winner",
		},
	}
	provider := &channelProviderStub{
		createID:    "loser-channel",
		deleteError: cleanupErr,
	}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	_, _, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if !errors.Is(err, persistenceErr) {
		t.Fatalf("ProvisionLAChannel error = %v, want persistence error", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("ProvisionLAChannel error = %v, want cleanup error", err)
	}
	if !errors.Is(err, ErrLAChannelProviderFailed) {
		t.Fatalf("ProvisionLAChannel error = %v, want provider classification", err)
	}
	if !strings.Contains(err.Error(), "loser-channel") {
		t.Fatalf("ProvisionLAChannel error = %v, want loser channel ID", err)
	}
	if provider.createCalls != 1 {
		t.Fatalf("APNs create calls = %d, want 1", provider.createCalls)
	}
	if !provider.deleteHadDeadline {
		t.Fatal("cleanup context has no deadline")
	}
}

func TestProvisionLAChannelSurfacesConcurrentLoserCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("APNs unavailable")
	store := &channelStoreStub{
		createResult: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-1",
			ChannelID:  "winner",
		},
		createResultCreated: false,
	}
	provider := &channelProviderStub{
		createID:    "loser",
		deleteError: cleanupErr,
	}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	_, _, err := service.ProvisionLAChannel(context.Background(), "race-1", "topic-1")
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("ProvisionLAChannel error = %v, want cleanup error", err)
	}
	if !errors.Is(err, ErrLAChannelProviderFailed) {
		t.Fatalf("ProvisionLAChannel error = %v, want provider classification", err)
	}
	if !strings.Contains(err.Error(), "loser") {
		t.Fatalf("ProvisionLAChannel error = %v, want loser channel ID", err)
	}
	if !provider.deleteHadDeadline {
		t.Fatal("cleanup context has no deadline")
	}
}

func TestDeleteLAChannelLocalCleanupSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &channelStoreStub{channel: &storage.LiveActivityChannel{
		ActivityID: "race-1",
		TopicID:    "topic-1",
		ChannelID:  "channel-1",
	}}
	provider := &channelProviderStub{onDelete: cancel}
	service := NewPushBoyService(store, "", WithLAChannels(provider))

	if err := service.DeleteLAChannel(ctx, "race-1"); err != nil {
		t.Fatalf("DeleteLAChannel error = %v", err)
	}
	if store.deletedActivityID != "race-1" || store.deletedChannelID != "channel-1" {
		t.Fatalf(
			"deleted mapping = (%q, %q), want (race-1, channel-1)",
			store.deletedActivityID,
			store.deletedChannelID,
		)
	}
	if store.deleteContextErr != nil {
		t.Fatalf("local cleanup context error = %v, want detached context", store.deleteContextErr)
	}
	if !store.deleteHadDeadline {
		t.Fatal("local cleanup context has no deadline")
	}
}

type channelStoreStub struct {
	storage.Store
	channel              *storage.LiveActivityChannel
	createResult         *storage.LiveActivityChannel
	createResultCreated  bool
	createError          error
	reconcileResult      *storage.LiveActivityChannel
	reconcileError       error
	createCalls          int
	reconcileContextErr  error
	reconcileHadDeadline bool
	topicID              string
	lookupActivityID     string
	deletedActivityID    string
	deletedChannelID     string
	deleteContextErr     error
	deleteHadDeadline    bool
	deleteError          error
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

func (s *channelStoreStub) CreateOrGetLAChannel(
	ctx context.Context,
	channel *storage.LiveActivityChannel,
) (*storage.LiveActivityChannel, bool, error) {
	s.createCalls++
	if s.createCalls > 1 {
		s.reconcileContextErr = ctx.Err()
		_, s.reconcileHadDeadline = ctx.Deadline()
		if s.reconcileError != nil {
			return nil, false, s.reconcileError
		}
		if s.reconcileResult != nil {
			return s.reconcileResult, false, nil
		}
	}
	if s.createError != nil {
		return nil, false, s.createError
	}
	if s.createResult != nil {
		return s.createResult, s.createResultCreated, nil
	}
	s.channel = channel
	return channel, true, nil
}

func (s *channelStoreStub) DeleteLAChannel(
	ctx context.Context,
	activityID string,
	channelID string,
) error {
	s.deletedActivityID = activityID
	s.deletedChannelID = channelID
	s.deleteContextErr = ctx.Err()
	_, s.deleteHadDeadline = ctx.Deadline()
	return s.deleteError
}

type channelProviderStub struct {
	createID          string
	createCalls       int
	deletedIDs        []string
	onCreate          func()
	onDelete          func()
	deleteContextErr  error
	deleteHadDeadline bool
	deleteError       error
}

func (p *channelProviderStub) CreateLiveActivityChannel(context.Context) (string, error) {
	p.createCalls++
	if p.onCreate != nil {
		p.onCreate()
	}
	return p.createID, nil
}

func (p *channelProviderStub) DeleteLiveActivityChannel(ctx context.Context, channelID string) error {
	p.deleteContextErr = ctx.Err()
	_, p.deleteHadDeadline = ctx.Deadline()
	p.deletedIDs = append(p.deletedIDs, channelID)
	if p.onDelete != nil {
		p.onDelete()
	}
	return p.deleteError
}
