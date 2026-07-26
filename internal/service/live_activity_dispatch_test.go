package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestCreateLAStartLazilyEnsuresTopicChannelBeforeCreatingJob(t *testing.T) {
	store := &laDispatchStoreStub{}
	service := NewPushBoyService(
		store,
		"",
		WithLAChannels(&channelProviderStub{createID: "channel-1"}),
	)

	result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:       model.LiveActivityActionStart,
		ActivityID:   "race-1",
		ActivityType: "race",
		TopicID:      "topic-1",
		Payload:      json.RawMessage(`{"lap":1}`),
	})
	if err != nil {
		t.Fatalf("CreateLADispatch error = %v", err)
	}
	if !store.channelEnsured {
		t.Fatal("channel was not ensured")
	}
	if !store.channelEnsuredBeforeStartJob {
		t.Fatal("start job was created before channel ensure completed")
	}
	if result.ChannelID != "channel-1" {
		t.Fatalf("ChannelID = %q, want channel-1", result.ChannelID)
	}
}

func TestCreateLAStartFallsBackToTokenOnlyWhenChannelEnsureFails(t *testing.T) {
	providerErr := errors.New("APNs unavailable")
	tests := []struct {
		name     string
		service  func(*laDispatchStoreStub) *PushboyService
		storeErr error
	}{
		{
			name: "provider not configured",
			service: func(store *laDispatchStoreStub) *PushboyService {
				return NewPushBoyService(store, "")
			},
		},
		{
			name: "provider failure",
			service: func(store *laDispatchStoreStub) *PushboyService {
				return NewPushBoyService(
					store,
					"",
					WithLAChannels(&channelProviderStub{createError: providerErr}),
				)
			},
		},
		{
			name:     "storage failure",
			storeErr: errors.New("database unavailable"),
			service: func(store *laDispatchStoreStub) *PushboyService {
				return NewPushBoyService(
					store,
					"",
					WithLAChannels(&channelProviderStub{createID: "channel-1"}),
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &laDispatchStoreStub{channelEnsureError: test.storeErr}
			service := test.service(store)

			result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
				Action:       model.LiveActivityActionStart,
				ActivityID:   "race-1",
				ActivityType: "race",
				TopicID:      "topic-1",
				Payload:      json.RawMessage(`{"lap":1}`),
			})
			if err != nil {
				t.Fatalf("CreateLADispatch error = %v", err)
			}
			if !store.startJobCreated {
				t.Fatal("token-only start did not create the job")
			}
			if !store.dispatchCreated {
				t.Fatal("token-only start did not create a dispatch")
			}
			if result.ChannelID != "" {
				t.Fatalf("ChannelID = %q, want token-only fallback", result.ChannelID)
			}
		})
	}
}

func TestCreateLAStartRejectsExistingChannelForAnotherTopic(t *testing.T) {
	store := &laDispatchStoreStub{
		channel: &storage.LiveActivityChannel{
			ActivityID: "race-1",
			TopicID:    "topic-2",
			ChannelID:  "channel-1",
		},
	}
	service := NewPushBoyService(store, "")

	_, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:       model.LiveActivityActionStart,
		ActivityID:   "race-1",
		ActivityType: "race",
		TopicID:      "topic-1",
		Payload:      json.RawMessage(`{"lap":1}`),
	})
	if !errors.Is(err, ErrLAChannelConflict) {
		t.Fatalf("CreateLADispatch error = %v, want channel conflict", err)
	}
	if store.startJobCreated {
		t.Fatal("start job was created despite channel topic conflict")
	}
}

func TestCreateLAStartClassifiesAtomicChannelJobConflict(t *testing.T) {
	store := &laDispatchStoreStub{
		startJobError: storage.Errors.Conflict,
	}
	service := NewPushBoyService(
		store,
		"",
		WithLAChannels(&channelProviderStub{createID: "channel-1"}),
	)

	_, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:       model.LiveActivityActionStart,
		ActivityID:   "race-1",
		ActivityType: "race",
		TopicID:      "topic-1",
		Payload:      json.RawMessage(`{"lap":1}`),
	})
	if !errors.Is(err, ErrLAChannelConflict) {
		t.Fatalf("CreateLADispatch error = %v, want channel conflict", err)
	}
}

func TestCreateLAStartForUserDoesNotEnsureChannel(t *testing.T) {
	store := &laDispatchStoreStub{}
	service := NewPushBoyService(
		store,
		"",
		WithLAChannels(&channelProviderStub{createID: "channel-1"}),
	)

	result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:       model.LiveActivityActionStart,
		ActivityID:   "race-1",
		ActivityType: "race",
		UserID:       "user-1",
		Payload:      json.RawMessage(`{"lap":1}`),
	})
	if err != nil {
		t.Fatalf("CreateLADispatch error = %v", err)
	}
	if store.channelEnsured {
		t.Fatal("user-scoped start ensured a channel")
	}
	if result.ChannelID != "" {
		t.Fatalf("ChannelID = %q, want token-only user start", result.ChannelID)
	}
}

func TestCreateLAUpdateContinuesTokenOnlyWhenChannelLookupFails(t *testing.T) {
	lookupErr := errors.New("channel lookup failed")
	store := &laDispatchStoreStub{
		channelLookupError: lookupErr,
		job: &storage.LiveActivityJob{
			ID:            "job-1",
			ActivityID:    "race-1",
			ActivityType:  "race",
			TopicID:       "topic-1",
			Status:        model.LiveActivityJobStatusActive,
			LatestPayload: json.RawMessage(`{"lap":1}`),
		},
	}
	service := NewPushBoyService(store, "")

	result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:     model.LiveActivityActionUpdate,
		ActivityID: "race-1",
		Payload:    json.RawMessage(`{"lap":2}`),
	})
	if err != nil {
		t.Fatalf("CreateLADispatch error = %v, want token-only fallback", err)
	}
	if !store.payloadUpdated {
		t.Fatal("job payload was not updated after channel lookup failure")
	}
	if !store.dispatchCreated {
		t.Fatal("dispatch was not created after channel lookup failure")
	}
	if result.ChannelID != "" {
		t.Fatalf("ChannelID = %q, want token-only fallback", result.ChannelID)
	}
	if got := string(store.job.LatestPayload); got != `{"lap":2}` {
		t.Fatalf("job payload = %s, want updated payload", got)
	}
}

func TestCreateLAEndFailsBeforeDispatchWhenChannelLookupFails(t *testing.T) {
	lookupErr := errors.New("channel lookup failed")
	store := &laDispatchStoreStub{
		channelLookupError: lookupErr,
		job: &storage.LiveActivityJob{
			ID:            "job-1",
			ActivityID:    "race-1",
			ActivityType:  "race",
			TopicID:       "topic-1",
			Status:        model.LiveActivityJobStatusActive,
			LatestPayload: json.RawMessage(`{"lap":1}`),
		},
	}
	service := NewPushBoyService(store, "")

	_, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:     model.LiveActivityActionEnd,
		ActivityID: "race-1",
	})
	if !errors.Is(err, lookupErr) || !errors.Is(err, ErrLAChannelLookupFailed) {
		t.Fatalf("CreateLADispatch error = %v, want classified channel lookup error", err)
	}
	if store.dispatchCreated {
		t.Fatal("end dispatch was created despite channel lookup failure")
	}
}

func TestCreateLAUpdateTreatsMissingOrMismatchedChannelAsTokenOnly(t *testing.T) {
	tests := []struct {
		name    string
		channel *storage.LiveActivityChannel
	}{
		{name: "missing"},
		{
			name: "topic mismatch",
			channel: &storage.LiveActivityChannel{
				ActivityID: "race-1",
				TopicID:    "topic-2",
				ChannelID:  "channel-1",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &laDispatchStoreStub{
				job: &storage.LiveActivityJob{
					ID:           "job-1",
					ActivityID:   "race-1",
					ActivityType: "race",
					TopicID:      "topic-1",
					Status:       model.LiveActivityJobStatusActive,
				},
				channel: test.channel,
			}
			service := NewPushBoyService(store, "")

			result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
				Action:     model.LiveActivityActionUpdate,
				ActivityID: "race-1",
				Payload:    json.RawMessage(`{"lap":2}`),
			})
			if err != nil {
				t.Fatalf("CreateLADispatch error = %v", err)
			}
			if !store.payloadUpdated {
				t.Fatal("token-only update did not update the job payload")
			}
			if !store.dispatchCreated {
				t.Fatal("token-only update did not create a dispatch")
			}
			if result.ChannelID != "" {
				t.Fatalf("ChannelID = %q, want token-only fallback", result.ChannelID)
			}
		})
	}
}

type laDispatchStoreStub struct {
	storage.Store
	job                          *storage.LiveActivityJob
	channel                      *storage.LiveActivityChannel
	channelLookupError           error
	channelEnsureError           error
	channelEnsured               bool
	channelEnsuredBeforeStartJob bool
	startJobCreated              bool
	startJobError                error
	payloadUpdated               bool
	dispatchCreated              bool
}

func (s *laDispatchStoreStub) GetTopicByID(
	_ context.Context,
	topicID string,
) (*storage.Topic, error) {
	return &storage.Topic{ID: topicID}, nil
}

func (s *laDispatchStoreStub) GetUser(
	_ context.Context,
	userID string,
) (*storage.User, error) {
	return &storage.User{ID: userID}, nil
}

func (s *laDispatchStoreStub) EnsureLAChannel(
	ctx context.Context,
	activityID string,
	topicID string,
	create func(context.Context) (string, error),
) (*storage.LiveActivityChannel, bool, error) {
	s.channelEnsured = true
	if s.channelEnsureError != nil {
		return nil, false, s.channelEnsureError
	}
	if s.channel != nil {
		return s.channel, false, nil
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

func (s *laDispatchStoreStub) GetLAChannelByActivityID(
	_ context.Context,
	_ string,
) (*storage.LiveActivityChannel, error) {
	if s.channelLookupError != nil {
		return nil, s.channelLookupError
	}
	if s.channel != nil {
		return s.channel, nil
	}
	return nil, storage.Errors.NotFound
}

func (s *laDispatchStoreStub) CreateOrGetLAStartJob(
	_ context.Context,
	job *storage.LiveActivityJob,
) (*storage.LiveActivityJob, bool, error) {
	s.startJobCreated = true
	s.channelEnsuredBeforeStartJob = s.channelEnsured
	if s.startJobError != nil {
		return nil, false, s.startJobError
	}
	s.job = job
	return job, true, nil
}

func (s *laDispatchStoreStub) GetLAJobByActivityID(
	_ context.Context,
	_ string,
) (*storage.LiveActivityJob, error) {
	if s.job == nil {
		return nil, storage.Errors.NotFound
	}
	return s.job, nil
}

func (s *laDispatchStoreStub) RollbackLAStartJob(
	_ context.Context,
	_ string,
) error {
	return nil
}

func (s *laDispatchStoreStub) UpdateLAJobPayloadIfActive(
	_ context.Context,
	_ string,
	payload json.RawMessage,
	options json.RawMessage,
	updatedAt time.Time,
) error {
	s.payloadUpdated = true
	s.job.LatestPayload = payload
	s.job.Options = options
	s.job.UpdatedAt = updatedAt
	return nil
}

func (s *laDispatchStoreStub) CreateLADispatch(
	_ context.Context,
	dispatch *storage.LiveActivityDispatch,
) (*storage.LiveActivityDispatch, error) {
	s.dispatchCreated = true
	return dispatch, nil
}
