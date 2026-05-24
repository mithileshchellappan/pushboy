package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestHandleRegisterTokenValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "missing token", body: `{"platform":"apns"}`},
		{name: "invalid platform", body: `{"platform":"web","token":"device-token"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := testRouter(t, &serverStoreStub{t: t}, &serverJobPipeline{t: t, failOnSubmit: true})
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/users/tokens", bytes.NewBufferString(tt.body))

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestHandleRegisterTokenSuccessAndConflict(t *testing.T) {
	now := time.Now().UTC()

	t.Run("success creates user and token", func(t *testing.T) {
		store := &serverStoreStub{t: t}
		store.getUserFunc = func(ctx context.Context, userID string) (*storage.User, error) {
			if userID != "user-1" {
				t.Fatalf("GetUser userID = %q, want user-1", userID)
			}
			return nil, storage.Errors.NotFound
		}
		store.createUserFunc = func(ctx context.Context, user *storage.User) (*storage.User, error) {
			if user.ID != "user-1" {
				t.Fatalf("CreateUser ID = %q, want user-1", user.ID)
			}
			return &storage.User{ID: user.ID, CreatedAt: now}, nil
		}
		store.createTokenFunc = func(ctx context.Context, token *storage.Token) (*storage.Token, error) {
			if token.UserID != "user-1" || token.Platform != model.APNS || token.Token != "device-token" {
				t.Fatalf("CreateToken token = %+v", token)
			}
			token.CreatedAt = now
			return token, nil
		}
		router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true})
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/users/tokens", bytes.NewBufferString(`{"id":"user-1","platform":"apns","token":"device-token"}`))

		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
		}
		var response struct {
			User  *userResponse
			Token *tokenResponse
		}
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.User == nil || response.User.ID != "user-1" {
			t.Fatalf("response.User = %+v, want user-1", response.User)
		}
		if response.Token == nil || response.Token.UserID != "user-1" || response.Token.Platform != model.APNS || response.Token.Token != "device-token" {
			t.Fatalf("response.Token = %+v", response.Token)
		}
	})

	t.Run("duplicate token maps to conflict", func(t *testing.T) {
		store := &serverStoreStub{t: t}
		store.getUserFunc = func(ctx context.Context, userID string) (*storage.User, error) {
			return &storage.User{ID: userID, CreatedAt: now}, nil
		}
		store.createTokenFunc = func(ctx context.Context, token *storage.Token) (*storage.Token, error) {
			return nil, storage.Errors.AlreadyExists
		}
		router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true})
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/users/tokens", bytes.NewBufferString(`{"id":"user-1","platform":"fcm","token":"device-token"}`))

		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusConflict, recorder.Body.String())
		}
	})
}

func TestHandleSendToUserNotificationValidation(t *testing.T) {
	t.Run("empty visible notification is rejected before service", func(t *testing.T) {
		router := testRouter(t, &serverStoreStub{t: t}, &serverJobPipeline{t: t, failOnSubmit: true})
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/users/user-1/send", bytes.NewBufferString(`{}`))

		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	})

	t.Run("silent notification with empty title and body is accepted", func(t *testing.T) {
		store := &serverStoreStub{t: t}
		store.createUserPublishJobFunc = func(ctx context.Context, job *storage.PublishJob) (*storage.PublishJob, error) {
			if job.UserID != "user-1" {
				t.Fatalf("CreateUserPublishJob UserID = %q, want user-1", job.UserID)
			}
			if job.Payload == nil || !job.Payload.Silent {
				t.Fatalf("CreateUserPublishJob payload = %+v, want silent payload", job.Payload)
			}
			return job, nil
		}
		jobPipeline := &serverJobPipeline{t: t}
		router := testRouter(t, store, jobPipeline)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/users/user-1/send", bytes.NewBufferString(`{"silent":true}`))

		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
		}
		if len(jobPipeline.submitted) != 1 {
			t.Fatalf("submitted jobs = %d, want 1", len(jobPipeline.submitted))
		}
		if got := jobPipeline.submitted[0]; got.UserID != "user-1" || got.JobType != model.JobTypePush || got.Payload == nil || !got.Payload.Silent {
			t.Fatalf("submitted job = %+v", got)
		}
	})
}

func TestHandleRegisterLATokenStoresActivityID(t *testing.T) {
	now := time.Now().UTC()
	store := &serverStoreStub{t: t}
	store.getUserFunc = func(ctx context.Context, userID string) (*storage.User, error) {
		if userID != "user-1" {
			t.Fatalf("GetUser userID = %q, want user-1", userID)
		}
		return &storage.User{ID: userID, CreatedAt: now}, nil
	}
	store.getTopicByIDFunc = func(ctx context.Context, topicID string) (*storage.Topic, error) {
		if topicID != "broadcast" {
			t.Fatalf("GetTopicByID topicID = %q, want broadcast", topicID)
		}
		return &storage.Topic{ID: topicID, Name: topicID}, nil
	}
	store.subscribeUserToLATopicFunc = func(ctx context.Context, sub *storage.LiveActivityUserTopicSubscription) (*storage.LiveActivityUserTopicSubscription, error) {
		if sub.UserID != "user-1" || sub.TopicID != "broadcast" {
			t.Fatalf("SubscribeUserToLATopic sub = %+v", sub)
		}
		return sub, nil
	}
	store.upsertLiveActivityTokenFunc = func(ctx context.Context, token *storage.LiveActivityToken) (*storage.LiveActivityToken, error) {
		if token.UserID != "user-1" || token.Platform != model.APNS || token.TokenType != model.LiveActivityTokenTypeUpdate || token.Token != "la-token" {
			t.Fatalf("UpsertLiveActivityToken token = %+v", token)
		}
		if token.ActivityID != "session-1_2026" {
			t.Fatalf("UpsertLiveActivityToken ActivityID = %q, want session-1_2026", token.ActivityID)
		}
		token.ID = "token-id"
		token.CreatedAt = now
		token.LastSeenAt = now
		return token, nil
	}

	router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/live-activity/tokens", bytes.NewBufferString(`{
		"userId":"user-1",
		"topicId":"broadcast",
		"activityId":"session-1_2026",
		"platform":"apns",
		"tokenType":"update",
		"token":"la-token"
	}`))

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	var response struct {
		Token *laTokenResponse
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Token == nil || response.Token.ActivityID != "session-1_2026" {
		t.Fatalf("response.Token = %+v", response.Token)
	}
}

func testRouter(t *testing.T, store storage.Store, jobPipeline pipeline.Pipeline[model.JobItem]) http.Handler {
	t.Helper()

	return New(service.NewPushBoyService(store, ""), jobPipeline).setupRouter()
}

type serverJobPipeline struct {
	t            *testing.T
	failOnSubmit bool
	submitErr    error
	submitted    []model.JobItem
}

func (p *serverJobPipeline) Submit(ctx context.Context, item model.JobItem) error {
	if p.failOnSubmit {
		p.t.Helper()
		p.t.Fatalf("job pipeline Submit should not be called")
	}
	if p.submitErr != nil {
		return p.submitErr
	}
	p.submitted = append(p.submitted, item)
	return nil
}

func (p *serverJobPipeline) Receive(ctx context.Context) (pipeline.Delivery[model.JobItem], error) {
	return nil, pipeline.ErrClosed
}

func (p *serverJobPipeline) Close(ctx context.Context) error {
	return nil
}

type serverStoreStub struct {
	t *testing.T

	getUserFunc                 func(context.Context, string) (*storage.User, error)
	getTopicByIDFunc            func(context.Context, string) (*storage.Topic, error)
	createUserFunc              func(context.Context, *storage.User) (*storage.User, error)
	createTokenFunc             func(context.Context, *storage.Token) (*storage.Token, error)
	upsertLiveActivityTokenFunc func(context.Context, *storage.LiveActivityToken) (*storage.LiveActivityToken, error)
	subscribeUserToLATopicFunc  func(context.Context, *storage.LiveActivityUserTopicSubscription) (*storage.LiveActivityUserTopicSubscription, error)
	createUserPublishJobFunc    func(context.Context, *storage.PublishJob) (*storage.PublishJob, error)
	updateJobStatusFunc         func(context.Context, string, model.NotificationJobStatus) error
}

func (s *serverStoreStub) unused(method string) {
	if s.t != nil {
		s.t.Helper()
		s.t.Fatalf("%s should not be called", method)
	}
}

func (s *serverStoreStub) CreateUser(ctx context.Context, user *storage.User) (*storage.User, error) {
	if s.createUserFunc != nil {
		return s.createUserFunc(ctx, user)
	}
	s.unused("CreateUser")
	return nil, errors.New("unexpected CreateUser")
}

func (s *serverStoreStub) GetUser(ctx context.Context, userID string) (*storage.User, error) {
	if s.getUserFunc != nil {
		return s.getUserFunc(ctx, userID)
	}
	s.unused("GetUser")
	return nil, errors.New("unexpected GetUser")
}

func (s *serverStoreStub) ListUsers(ctx context.Context, query storage.PageQuery) ([]storage.User, error) {
	s.unused("ListUsers")
	return nil, errors.New("unexpected ListUsers")
}

func (s *serverStoreStub) DeleteUser(ctx context.Context, userID string) error {
	s.unused("DeleteUser")
	return errors.New("unexpected DeleteUser")
}

func (s *serverStoreStub) CreateToken(ctx context.Context, token *storage.Token) (*storage.Token, error) {
	if s.createTokenFunc != nil {
		return s.createTokenFunc(ctx, token)
	}
	s.unused("CreateToken")
	return nil, errors.New("unexpected CreateToken")
}

func (s *serverStoreStub) GetTokensByUserID(ctx context.Context, userID string) ([]storage.Token, error) {
	s.unused("GetTokensByUserID")
	return nil, errors.New("unexpected GetTokensByUserID")
}

func (s *serverStoreStub) DeleteToken(ctx context.Context, tokenID string) error {
	s.unused("DeleteToken")
	return errors.New("unexpected DeleteToken")
}

func (s *serverStoreStub) SoftDeleteToken(ctx context.Context, tokenID string) error {
	s.unused("SoftDeleteToken")
	return errors.New("unexpected SoftDeleteToken")
}

func (s *serverStoreStub) BulkSoftDeleteToken(ctx context.Context, tokenIDs []string) error {
	s.unused("BulkSoftDeleteToken")
	return errors.New("unexpected BulkSoftDeleteToken")
}

func (s *serverStoreStub) CreateTopic(ctx context.Context, topic *storage.Topic) error {
	s.unused("CreateTopic")
	return errors.New("unexpected CreateTopic")
}

func (s *serverStoreStub) ListTopics(ctx context.Context) ([]storage.Topic, error) {
	s.unused("ListTopics")
	return nil, errors.New("unexpected ListTopics")
}

func (s *serverStoreStub) GetTopicByID(ctx context.Context, topicID string) (*storage.Topic, error) {
	if s.getTopicByIDFunc != nil {
		return s.getTopicByIDFunc(ctx, topicID)
	}
	s.unused("GetTopicByID")
	return nil, errors.New("unexpected GetTopicByID")
}

func (s *serverStoreStub) DeleteTopic(ctx context.Context, topicID string) error {
	s.unused("DeleteTopic")
	return errors.New("unexpected DeleteTopic")
}

func (s *serverStoreStub) SubscribeUserToTopic(ctx context.Context, sub *storage.UserTopicSubscription) (*storage.UserTopicSubscription, error) {
	s.unused("SubscribeUserToTopic")
	return nil, errors.New("unexpected SubscribeUserToTopic")
}

func (s *serverStoreStub) UnsubscribeUserFromTopic(ctx context.Context, userID string, topicID string) error {
	s.unused("UnsubscribeUserFromTopic")
	return errors.New("unexpected UnsubscribeUserFromTopic")
}

func (s *serverStoreStub) GetUserSubscriptions(ctx context.Context, userID string) ([]storage.UserTopicSubscription, error) {
	s.unused("GetUserSubscriptions")
	return nil, errors.New("unexpected GetUserSubscriptions")
}

func (s *serverStoreStub) GetTopicSubscribers(ctx context.Context, topicID string) ([]storage.User, error) {
	s.unused("GetTopicSubscribers")
	return nil, errors.New("unexpected GetTopicSubscribers")
}

func (s *serverStoreStub) ListTopicSubscribers(ctx context.Context, topicID string, query storage.PageQuery) ([]storage.TopicSubscriber, error) {
	s.unused("ListTopicSubscribers")
	return nil, errors.New("unexpected ListTopicSubscribers")
}

func (s *serverStoreStub) GetTopicSubscriberCount(ctx context.Context, topicID string) (int, error) {
	s.unused("GetTopicSubscriberCount")
	return 0, errors.New("unexpected GetTopicSubscriberCount")
}

func (s *serverStoreStub) CreatePublishJob(ctx context.Context, job *storage.PublishJob) (*storage.PublishJob, error) {
	s.unused("CreatePublishJob")
	return nil, errors.New("unexpected CreatePublishJob")
}

func (s *serverStoreStub) CreateUserPublishJob(ctx context.Context, job *storage.PublishJob) (*storage.PublishJob, error) {
	if s.createUserPublishJobFunc != nil {
		return s.createUserPublishJobFunc(ctx, job)
	}
	s.unused("CreateUserPublishJob")
	return nil, errors.New("unexpected CreateUserPublishJob")
}

func (s *serverStoreStub) FetchPendingJobs(ctx context.Context, limit int) ([]storage.PublishJob, error) {
	s.unused("FetchPendingJobs")
	return nil, errors.New("unexpected FetchPendingJobs")
}

func (s *serverStoreStub) UpdateJobStatus(ctx context.Context, jobID string, status model.NotificationJobStatus) error {
	if s.updateJobStatusFunc != nil {
		return s.updateJobStatusFunc(ctx, jobID, status)
	}
	s.unused("UpdateJobStatus")
	return errors.New("unexpected UpdateJobStatus")
}

func (s *serverStoreStub) FinalizeJobDispatch(ctx context.Context, jobID string, totalCount int) error {
	s.unused("FinalizeJobDispatch")
	return errors.New("unexpected FinalizeJobDispatch")
}

func (s *serverStoreStub) GetJobStatus(ctx context.Context, jobID string) (*storage.PublishJob, error) {
	s.unused("GetJobStatus")
	return nil, errors.New("unexpected GetJobStatus")
}

func (s *serverStoreStub) ListTopicNotifications(ctx context.Context, topicID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
	s.unused("ListTopicNotifications")
	return nil, errors.New("unexpected ListTopicNotifications")
}

func (s *serverStoreStub) ListUserNotifications(ctx context.Context, userID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
	s.unused("ListUserNotifications")
	return nil, errors.New("unexpected ListUserNotifications")
}

func (s *serverStoreStub) ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error {
	s.unused("ApplyPushOutcomeBatch")
	return errors.New("unexpected ApplyPushOutcomeBatch")
}

func (s *serverStoreStub) IncrementJobCounters(ctx context.Context, jobID string, success int, failure int) error {
	s.unused("IncrementJobCounters")
	return errors.New("unexpected IncrementJobCounters")
}

func (s *serverStoreStub) CompleteJobIfDone(ctx context.Context, jobID string) error {
	s.unused("CompleteJobIfDone")
	return errors.New("unexpected CompleteJobIfDone")
}

func (s *serverStoreStub) GetTokenBatchForTopic(ctx context.Context, topicID string, cursor string, batchSize int) (*storage.TokenBatch, error) {
	s.unused("GetTokenBatchForTopic")
	return nil, errors.New("unexpected GetTokenBatchForTopic")
}

func (s *serverStoreStub) GetTokenBatchForUser(ctx context.Context, userID string, cursor string, batchSize int) (*storage.TokenBatch, error) {
	s.unused("GetTokenBatchForUser")
	return nil, errors.New("unexpected GetTokenBatchForUser")
}

func (s *serverStoreStub) GetTokenCountForTopic(ctx context.Context, topicID string) (int, error) {
	s.unused("GetTokenCountForTopic")
	return 0, errors.New("unexpected GetTokenCountForTopic")
}

func (s *serverStoreStub) GetTokenCountForUser(ctx context.Context, userID string) (int, error) {
	s.unused("GetTokenCountForUser")
	return 0, errors.New("unexpected GetTokenCountForUser")
}

func (s *serverStoreStub) RecordDeliveryReceipt(ctx context.Context, receipt *model.DeliveryReceipt) error {
	s.unused("RecordDeliveryReceipt")
	return errors.New("unexpected RecordDeliveryReceipt")
}

func (s *serverStoreStub) GetScheduledJobs(ctx context.Context) ([]storage.PublishJob, error) {
	s.unused("GetScheduledJobs")
	return nil, errors.New("unexpected GetScheduledJobs")
}

func (s *serverStoreStub) UpsertLiveActivityToken(ctx context.Context, token *storage.LiveActivityToken) (*storage.LiveActivityToken, error) {
	if s.upsertLiveActivityTokenFunc != nil {
		return s.upsertLiveActivityTokenFunc(ctx, token)
	}
	s.unused("UpsertLiveActivityToken")
	return nil, errors.New("unexpected UpsertLiveActivityToken")
}

func (s *serverStoreStub) InvalidateLiveActivityToken(ctx context.Context, userID string, tokenValue string) error {
	s.unused("InvalidateLiveActivityToken")
	return errors.New("unexpected InvalidateLiveActivityToken")
}

func (s *serverStoreStub) SubscribeUserToLATopic(ctx context.Context, sub *storage.LiveActivityUserTopicSubscription) (*storage.LiveActivityUserTopicSubscription, error) {
	if s.subscribeUserToLATopicFunc != nil {
		return s.subscribeUserToLATopicFunc(ctx, sub)
	}
	s.unused("SubscribeUserToLATopic")
	return nil, errors.New("unexpected SubscribeUserToLATopic")
}

func (s *serverStoreStub) CreateOrGetLAStartJob(ctx context.Context, job *storage.LiveActivityJob) (*storage.LiveActivityJob, bool, error) {
	s.unused("CreateOrGetLAStartJob")
	return nil, false, errors.New("unexpected CreateOrGetLAStartJob")
}

func (s *serverStoreStub) GetLAJob(ctx context.Context, jobID string) (*storage.LiveActivityJob, error) {
	s.unused("GetLAJob")
	return nil, errors.New("unexpected GetLAJob")
}

func (s *serverStoreStub) GetLAJobByActivityID(ctx context.Context, activityID string) (*storage.LiveActivityJob, error) {
	s.unused("GetLAJobByActivityID")
	return nil, errors.New("unexpected GetLAJobByActivityID")
}

func (s *serverStoreStub) FindLAJobByUserScope(ctx context.Context, activityType string, userID string) (*storage.LiveActivityJob, error) {
	s.unused("FindLAJobByUserScope")
	return nil, errors.New("unexpected FindLAJobByUserScope")
}

func (s *serverStoreStub) FindLAJobByTopicScope(ctx context.Context, activityType string, topicID string) (*storage.LiveActivityJob, error) {
	s.unused("FindLAJobByTopicScope")
	return nil, errors.New("unexpected FindLAJobByTopicScope")
}

func (s *serverStoreStub) UpdateLAJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt time.Time) error {
	s.unused("UpdateLAJobPayloadIfActive")
	return errors.New("unexpected UpdateLAJobPayloadIfActive")
}

func (s *serverStoreStub) RollbackLAStartJob(ctx context.Context, jobID string) error {
	s.unused("RollbackLAStartJob")
	return errors.New("unexpected RollbackLAStartJob")
}

func (s *serverStoreStub) CloseLAJobIfActive(ctx context.Context, jobID string, updatedAt time.Time) error {
	s.unused("CloseLAJobIfActive")
	return errors.New("unexpected CloseLAJobIfActive")
}

func (s *serverStoreStub) FailLAJobIfActive(ctx context.Context, jobID string) error {
	s.unused("FailLAJobIfActive")
	return errors.New("unexpected FailLAJobIfActive")
}

func (s *serverStoreStub) CreateLADispatch(ctx context.Context, dispatch *storage.LiveActivityDispatch) (*storage.LiveActivityDispatch, error) {
	s.unused("CreateLADispatch")
	return nil, errors.New("unexpected CreateLADispatch")
}

func (s *serverStoreStub) UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error {
	s.unused("UpdateLADispatchStatus")
	return errors.New("unexpected UpdateLADispatchStatus")
}

func (s *serverStoreStub) GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*storage.LiveActivityTokenBatch, error) {
	s.unused("GetLATokenBatchForDispatch")
	return nil, errors.New("unexpected GetLATokenBatchForDispatch")
}

func (s *serverStoreStub) CompleteLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	s.unused("CompleteLADispatchEnqueue")
	return errors.New("unexpected CompleteLADispatchEnqueue")
}

func (s *serverStoreStub) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error {
	s.unused("ApplyLAOutcomeBatch")
	return errors.New("unexpected ApplyLAOutcomeBatch")
}

func (s *serverStoreStub) InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error) {
	s.unused("InvalidateExpiredLAUpdateTokens")
	return 0, errors.New("unexpected InvalidateExpiredLAUpdateTokens")
}

func (s *serverStoreStub) Close() error {
	s.unused("Close")
	return errors.New("unexpected Close")
}
