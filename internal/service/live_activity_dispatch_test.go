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
	providerEntered := make(chan struct{})
	releaseProvider := make(chan struct{})
	defer func() {
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	}()

	store := &laDispatchStoreStub{}
	service := NewPushBoyService(
		store,
		"",
		&channelProviderStub{
			createID: "channel-1",
			onCreate: func() {
				close(providerEntered)
				<-releaseProvider
			},
		},
	)

	type startResult struct {
		result *LADispatchResult
		err    error
	}
	done := make(chan startResult, 1)
	go func() {
		result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
			Action:       model.LiveActivityActionStart,
			ActivityID:   "race-1",
			ActivityType: "race",
			TopicID:      "topic-1",
			Payload:      json.RawMessage(`{"lap":1}`),
		})
		done <- startResult{result: result, err: err}
	}()

	select {
	case <-providerEntered:
	case <-time.After(time.Second):
		t.Fatal("channel provider was not called")
	}
	if store.startJobCreated {
		t.Fatal("start job became visible while channel provisioning was blocked")
	}
	close(releaseProvider)

	var call startResult
	select {
	case call = <-done:
	case <-time.After(time.Second):
		t.Fatal("start did not finish after channel provisioning completed")
	}
	result, err := call.result, call.err
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

func TestCreateLAStartRetryDoesNotProvisionUnusedChannel(t *testing.T) {
	existing := &storage.LiveActivityJob{
		ID:           "job-1",
		ActivityID:   "race-1",
		ActivityType: "race",
		TopicID:      "topic-1",
		Status:       model.LiveActivityJobStatusActive,
	}
	store := &laDispatchStoreStub{job: existing}
	provider := &channelProviderStub{createID: "unused-channel"}
	service := NewPushBoyService(store, "", provider)

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
	if result.Status != "already_started" || result.Job != existing {
		t.Fatalf("result = %+v, want existing job", result)
	}
	if store.channelEnsured || provider.createCalls != 0 || store.dispatchCreated {
		t.Fatalf(
			"channel ensured = %v, provider calls = %d, dispatch created = %v; want false, 0, false",
			store.channelEnsured,
			provider.createCalls,
			store.dispatchCreated,
		)
	}
}

func TestCreateLAStartRetryRejectsDifferentScopeWithoutProvisioning(t *testing.T) {
	tests := []struct {
		name          string
		existingUser  string
		existingTopic string
		requestUser   string
		requestTopic  string
	}{
		{name: "different topic", existingTopic: "topic-2", requestTopic: "topic-1"},
		{name: "topic to user", existingTopic: "topic-1", requestUser: "user-1"},
		{name: "user to topic", existingUser: "user-1", requestTopic: "topic-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &laDispatchStoreStub{job: &storage.LiveActivityJob{
				ID:           "job-1",
				ActivityID:   "race-1",
				ActivityType: "race",
				UserID:       test.existingUser,
				TopicID:      test.existingTopic,
				Status:       model.LiveActivityJobStatusActive,
			}}
			provider := &channelProviderStub{createID: "unused-channel"}
			service := NewPushBoyService(store, "", provider)

			_, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
				Action:       model.LiveActivityActionStart,
				ActivityID:   "race-1",
				ActivityType: "race",
				UserID:       test.requestUser,
				TopicID:      test.requestTopic,
				Payload:      json.RawMessage(`{"lap":1}`),
			})
			if !errors.Is(err, ErrLAChannelConflict) {
				t.Fatalf("CreateLADispatch error = %v, want channel conflict", err)
			}
			if store.channelEnsured || provider.createCalls != 0 || store.dispatchCreated {
				t.Fatalf(
					"channel ensured = %v, provider calls = %d, dispatch created = %v; want false, 0, false",
					store.channelEnsured,
					provider.createCalls,
					store.dispatchCreated,
				)
			}
		})
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
				return NewPushBoyService(store, "", nil)
			},
		},
		{
			name: "provider failure",
			service: func(store *laDispatchStoreStub) *PushboyService {
				return NewPushBoyService(
					store,
					"",
					&channelProviderStub{createError: providerErr},
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
					&channelProviderStub{createID: "channel-1"},
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
	service := NewPushBoyService(store, "", nil)

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

func TestCreateLAStartClassifiesAtomicJobConflict(t *testing.T) {
	store := &laDispatchStoreStub{startJobError: storage.Errors.Conflict}
	service := NewPushBoyService(
		store,
		"",
		&channelProviderStub{createID: "channel-1"},
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
		&channelProviderStub{createID: "channel-1"},
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

func TestCreateLADispatchChannelLookupPolicy(t *testing.T) {
	lookupErr := errors.New("channel lookup failed")
	for _, action := range []model.LiveActivityAction{
		model.LiveActivityActionUpdate,
		model.LiveActivityActionEnd,
	} {
		t.Run(string(action), func(t *testing.T) {
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
			service := NewPushBoyService(store, "", nil)

			result, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
				Action:     action,
				ActivityID: "race-1",
				Payload:    json.RawMessage(`{"lap":2}`),
			})

			if action == model.LiveActivityActionUpdate {
				if err != nil {
					t.Fatalf("CreateLADispatch error = %v, want token-only fallback", err)
				}
				if result.ChannelID != "" || !store.payloadUpdated || !store.dispatchCreated {
					t.Fatalf("update fallback = (%q, %v, %v), want token-only dispatch", result.ChannelID, store.payloadUpdated, store.dispatchCreated)
				}
				return
			}
			if !errors.Is(err, lookupErr) || !errors.Is(err, ErrLAChannelLookupFailed) {
				t.Fatalf("CreateLADispatch error = %v, want classified lookup error", err)
			}
			if store.dispatchCreated {
				t.Fatal("end dispatch was created despite channel lookup failure")
			}
		})
	}

	for _, test := range []struct {
		name    string
		channel *storage.LiveActivityChannel
	}{
		{name: "missing mapping"},
		{
			name: "mismatched topic",
			channel: &storage.LiveActivityChannel{
				ActivityID: "race-1",
				TopicID:    "topic-2",
				ChannelID:  "channel-1",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &laDispatchStoreStub{
				channel: test.channel,
				job: &storage.LiveActivityJob{
					ID:         "job-1",
					ActivityID: "race-1",
					TopicID:    "topic-1",
					Status:     model.LiveActivityJobStatusActive,
				},
			}
			result, err := NewPushBoyService(store, "", nil).CreateLADispatch(
				context.Background(),
				LADispatchRequest{
					Action:     model.LiveActivityActionUpdate,
					ActivityID: "race-1",
					Payload:    json.RawMessage(`{"lap":2}`),
				},
			)
			if err != nil {
				t.Fatalf("CreateLADispatch error = %v", err)
			}
			if result.ChannelID != "" || !store.dispatchCreated {
				t.Fatalf("result = (%q, %v), want token-only dispatch", result.ChannelID, store.dispatchCreated)
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
	ctx context.Context,
	job *storage.LiveActivityJob,
	createChannel func(context.Context) (string, error),
) (*storage.LAStartJobResult, error) {
	if s.startJobError != nil {
		return nil, s.startJobError
	}
	if s.job != nil {
		if s.job.TopicID != job.TopicID {
			return nil, storage.Errors.Conflict
		}
		return &storage.LAStartJobResult{Job: s.job}, nil
	}
	if s.channel != nil && (job.TopicID == "" || s.channel.TopicID != job.TopicID) {
		return nil, storage.Errors.Conflict
	}

	s.channelEnsured = createChannel != nil
	channelErr := s.channelEnsureError
	if s.channel == nil && channelErr == nil && createChannel != nil {
		channelID, err := createChannel(ctx)
		if err != nil {
			channelErr = err
		} else {
			s.channel = &storage.LiveActivityChannel{
				ActivityID: job.ActivityID,
				TopicID:    job.TopicID,
				ChannelID:  channelID,
			}
		}
	}
	s.startJobCreated = true
	s.channelEnsuredBeforeStartJob = s.channelEnsured
	s.job = job
	return &storage.LAStartJobResult{
		Job:          job,
		Channel:      s.channel,
		Created:      true,
		ChannelError: channelErr,
	}, nil
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
