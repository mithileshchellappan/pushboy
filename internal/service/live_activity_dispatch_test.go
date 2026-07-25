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

func TestCreateLAStartLooksUpChannelBeforeReplacingJob(t *testing.T) {
	lookupErr := errors.New("channel lookup failed")
	store := &laDispatchStoreStub{channelLookupError: lookupErr}
	service := NewPushBoyService(store, "")

	_, err := service.CreateLADispatch(context.Background(), LADispatchRequest{
		Action:       model.LiveActivityActionStart,
		ActivityID:   "race-1",
		ActivityType: "race",
		TopicID:      "topic-1",
		Payload:      json.RawMessage(`{"lap":1}`),
	})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("CreateLADispatch error = %v, want channel lookup error", err)
	}
	if !errors.Is(err, ErrLAChannelLookupFailed) {
		t.Fatalf("CreateLADispatch error = %v, want channel lookup classification", err)
	}
	if store.startJobCreated {
		t.Fatal("start job was replaced before channel lookup succeeded")
	}
	if store.dispatchCreated {
		t.Fatal("dispatch was created before channel lookup succeeded")
	}
}

func TestCreateLAStartTreatsMissingOrMismatchedChannelAsTokenOnly(t *testing.T) {
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
			store := &laDispatchStoreStub{channel: test.channel}
			service := NewPushBoyService(store, "")

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

func TestCreateLAUpdateLooksUpChannelBeforeUpdatingJob(t *testing.T) {
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
		Action:     model.LiveActivityActionUpdate,
		ActivityID: "race-1",
		Payload:    json.RawMessage(`{"lap":2}`),
	})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("CreateLADispatch error = %v, want channel lookup error", err)
	}
	if !errors.Is(err, ErrLAChannelLookupFailed) {
		t.Fatalf("CreateLADispatch error = %v, want channel lookup classification", err)
	}
	if store.payloadUpdated {
		t.Fatal("job payload was updated before channel lookup succeeded")
	}
	if store.dispatchCreated {
		t.Fatal("dispatch was created before channel lookup succeeded")
	}
	if got := string(store.job.LatestPayload); got != `{"lap":1}` {
		t.Fatalf("job payload = %s, want original payload", got)
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
	job                *storage.LiveActivityJob
	channel            *storage.LiveActivityChannel
	channelLookupError error
	startJobCreated    bool
	payloadUpdated     bool
	dispatchCreated    bool
}

func (s *laDispatchStoreStub) GetTopicByID(
	_ context.Context,
	topicID string,
) (*storage.Topic, error) {
	return &storage.Topic{ID: topicID}, nil
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
