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
			router := testRouter(t, &serverStoreStub{t: t}, &serverJobPipeline{t: t, failOnSubmit: true}, &serverLAJobPipeline{t: t, failOnSubmit: true})
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
		router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true}, &serverLAJobPipeline{t: t, failOnSubmit: true})
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
		router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true}, &serverLAJobPipeline{t: t, failOnSubmit: true})
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
		router := testRouter(t, &serverStoreStub{t: t}, &serverJobPipeline{t: t, failOnSubmit: true}, &serverLAJobPipeline{t: t, failOnSubmit: true})
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
		router := testRouter(t, store, jobPipeline, &serverLAJobPipeline{t: t, failOnSubmit: true})
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
		if token.ActivityID != " session-1_2026 " {
			t.Fatalf("UpsertLiveActivityToken ActivityID = %q, want exact opaque value", token.ActivityID)
		}
		token.ID = "token-id"
		token.CreatedAt = now
		token.LastSeenAt = now
		return token, nil
	}

	router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true}, &serverLAJobPipeline{t: t, failOnSubmit: true})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/live-activity/tokens", bytes.NewBufferString(`{
		"userId":"user-1",
		"topicId":"broadcast",
		"activityId":" session-1_2026 ",
		"platform":"apns",
		"tokenType":"update",
		"token":"la-token"
	}`))

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
}

func TestHandleRegisterLAStartTokenStoresBroadcastChannelCapability(t *testing.T) {
	now := time.Now().UTC()
	store := &serverStoreStub{t: t}
	store.getUserFunc = func(context.Context, string) (*storage.User, error) {
		return &storage.User{ID: "user-1", CreatedAt: now}, nil
	}
	store.upsertLiveActivityTokenFunc = func(_ context.Context, token *storage.LiveActivityToken) (*storage.LiveActivityToken, error) {
		if !token.SupportsBroadcastChannels {
			t.Fatal("SupportsBroadcastChannels = false, want true")
		}
		token.ID = "token-1"
		token.CreatedAt = now
		token.LastSeenAt = now
		return token, nil
	}

	router := testRouter(t, store, &serverJobPipeline{t: t, failOnSubmit: true}, &serverLAJobPipeline{t: t, failOnSubmit: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/live-activity/tokens", bytes.NewBufferString(`{
		"userId":"user-1",
		"platform":"apns",
		"tokenType":"start",
		"token":"push-to-start-token",
		"supportsBroadcastChannels":true
	}`))

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
}

func TestHandleProvisionLAChannel(t *testing.T) {
	tests := []struct {
		name          string
		existing      bool
		storeErr      error
		providerError error
		wantCode      int
	}{
		{
			name:     "creates mapping",
			wantCode: http.StatusCreated,
		},
		{
			name:     "returns existing mapping",
			existing: true,
			wantCode: http.StatusOK,
		},
		{
			name:     "rejects topic conflict",
			storeErr: storage.Errors.Conflict,
			wantCode: http.StatusConflict,
		},
		{
			name:          "maps provider failure",
			providerError: errors.New("APNs unavailable"),
			wantCode:      http.StatusBadGateway,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &serverStoreStub{t: t}
			store.ensureLAChannelFunc = func(
				ctx context.Context,
				activityID string,
				topicID string,
				create func(context.Context) (string, error),
			) (*storage.LiveActivityChannel, bool, error) {
				if test.storeErr != nil {
					return nil, false, test.storeErr
				}
				channelID := "existing-channel"
				if !test.existing {
					var err error
					channelID, err = create(ctx)
					if err != nil {
						return nil, false, err
					}
				}
				return &storage.LiveActivityChannel{
					ActivityID: activityID,
					TopicID:    topicID,
					ChannelID:  channelID,
				}, !test.existing, nil
			}
			provider := &serverLAChannelProvider{
				channelID:   "created-channel",
				createError: test.providerError,
			}
			router := New(
				service.NewPushBoyService(store, "", service.WithLAChannels(provider)),
				&serverJobPipeline{t: t, failOnSubmit: true},
				&serverLAJobPipeline{t: t, failOnSubmit: true},
			).setupRouter()

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPut,
				"/v1/live-activity/channels/activity-1",
				bytes.NewBufferString(`{"topicId":"topic-1"}`),
			)
			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantCode {
				t.Fatalf(
					"status = %d, want %d, body: %s",
					recorder.Code,
					test.wantCode,
					recorder.Body.String(),
				)
			}

			if test.wantCode == http.StatusCreated || test.wantCode == http.StatusOK {
				var response map[string]any
				if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if response["activityId"] != "activity-1" ||
					response["topicId"] != "topic-1" ||
					response["channelId"] == "" {
					t.Fatalf("response = %#v", response)
				}
			}
		})
	}
}

func TestLiveActivityChannelLifecycle(t *testing.T) {
	const (
		activityID   = "race/2026"
		activityPath = "/v1/live-activity/channels/race%2F2026"
	)
	var mapping *storage.LiveActivityChannel
	store := &serverStoreStub{t: t}
	store.ensureLAChannelFunc = func(
		ctx context.Context,
		gotActivityID string,
		topicID string,
		create func(context.Context) (string, error),
	) (*storage.LiveActivityChannel, bool, error) {
		if gotActivityID != activityID {
			t.Fatalf("EnsureLAChannel activity ID = %q, want %q", gotActivityID, activityID)
		}
		channelID, err := create(ctx)
		if err != nil {
			return nil, false, err
		}
		mapping = &storage.LiveActivityChannel{
			ActivityID: gotActivityID,
			TopicID:    topicID,
			ChannelID:  channelID,
			CreatedAt:  time.Now().UTC(),
		}
		return mapping, true, nil
	}
	store.getLAChannelFunc = func(_ context.Context, gotActivityID string) (*storage.LiveActivityChannel, error) {
		if gotActivityID != activityID {
			t.Fatalf("GetLAChannelByActivityID activity ID = %q, want %q", gotActivityID, activityID)
		}
		if mapping == nil || mapping.ActivityID != gotActivityID {
			return nil, storage.Errors.NotFound
		}
		return mapping, nil
	}
	store.deleteLAChannelFunc = func(_ context.Context, gotActivityID, channelID string) error {
		if gotActivityID != activityID {
			t.Fatalf("DeleteLAChannel activity ID = %q, want %q", gotActivityID, activityID)
		}
		if mapping == nil || mapping.ActivityID != gotActivityID || mapping.ChannelID != channelID {
			return storage.Errors.NotFound
		}
		mapping = nil
		return nil
	}
	provider := &serverLAChannelProvider{channelID: "apple-channel-id"}
	router := New(
		service.NewPushBoyService(store, "", service.WithLAChannels(provider)),
		&serverJobPipeline{t: t, failOnSubmit: true},
		&serverLAJobPipeline{t: t, failOnSubmit: true},
	).setupRouter()

	requests := []struct {
		method string
		body   string
		status int
	}{
		{http.MethodPut, `{"topicId":"topic-1"}`, http.StatusCreated},
		{http.MethodGet, "", http.StatusOK},
		{http.MethodDelete, "", http.StatusNoContent},
	}
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(
			recorder,
			httptest.NewRequest(
				request.method,
				activityPath,
				bytes.NewBufferString(request.body),
			),
		)
		if recorder.Code != request.status {
			t.Fatalf("%s status = %d, want %d, body: %s",
				request.method, recorder.Code, request.status, recorder.Body.String())
		}
	}
	if provider.deletedChannelID != "apple-channel-id" {
		t.Fatalf("deleted channel = %q, want apple-channel-id", provider.deletedChannelID)
	}
	if mapping != nil {
		t.Fatalf("mapping = %+v, want deleted", mapping)
	}
}

func TestHandleCreateLAJobUpdateRoutesToLALane(t *testing.T) {
	now := time.Now().UTC()
	store := &serverStoreStub{t: t}
	// The push lane must never see a Live Activity job.
	pushPipeline := &serverJobPipeline{t: t, failOnSubmit: true}
	laPipeline := &serverLAJobPipeline{t: t}
	var enqueuedDispatchID string
	store.getLAJobByActivityIDFunc = func(ctx context.Context, activityID string) (*storage.LiveActivityJob, error) {
		if activityID != "race-42" {
			t.Fatalf("GetLAJobByActivityID activityID = %q, want race-42", activityID)
		}
		return &storage.LiveActivityJob{
			ID:           "la-job-1",
			ActivityID:   "race-42",
			ActivityType: "RaceAttributes",
			TopicID:      "broadcast",
			Status:       model.LiveActivityJobStatusActive,
			CreatedAt:    now,
			UpdatedAt:    now,
		}, nil
	}
	store.updateLAJobPayloadIfActiveFunc = func(ctx context.Context, jobID string, payload, options json.RawMessage, updatedAt time.Time) error {
		if jobID != "la-job-1" {
			t.Fatalf("UpdateLAJobPayloadIfActive jobID = %q, want la-job-1", jobID)
		}
		return nil
	}
	store.getLAChannelFunc = func(_ context.Context, activityID string) (*storage.LiveActivityChannel, error) {
		return &storage.LiveActivityChannel{
			ActivityID: activityID,
			TopicID:    "broadcast",
			ChannelID:  "apple-channel-id",
		}, nil
	}
	store.createLADispatchFunc = func(ctx context.Context, dispatch *storage.LiveActivityDispatch) (*storage.LiveActivityDispatch, error) {
		if dispatch.Status != model.LiveActivityDispatchStatusEnqueuePending {
			t.Fatalf("CreateLADispatch status = %q, want %q", dispatch.Status, model.LiveActivityDispatchStatusEnqueuePending)
		}
		return dispatch, nil
	}
	store.markLADispatchEnqueuedFunc = func(ctx context.Context, dispatchID string) error {
		if len(laPipeline.submitted) != 1 {
			t.Fatalf("MarkLADispatchEnqueued called before pipeline accepted the dispatch")
		}
		enqueuedDispatchID = dispatchID
		return nil
	}

	router := New(
		service.NewPushBoyService(store, "", service.WithLAChannels(&serverLAChannelProvider{})),
		pushPipeline,
		laPipeline,
	).setupRouter()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/live-activity/jobs", bytes.NewBufferString(`{
		"action":"update",
		"activityId":"race-42",
		"payload":{"lap":12}
	}`))

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if len(laPipeline.submitted) != 1 {
		t.Fatalf("LA pipeline submissions = %d, want 1", len(laPipeline.submitted))
	}
	got := laPipeline.submitted[0]
	if got.Action != model.LiveActivityActionUpdate ||
		got.ActivityID != "race-42" ||
		got.JobID != "la-job-1" ||
		got.DispatchID == "" ||
		got.TopicID != "broadcast" ||
		got.ChannelID != "apple-channel-id" ||
		string(got.Payload) != `{"lap":12}` {
		t.Fatalf("submitted job = %+v, want LA fields populated", got)
	}
	if enqueuedDispatchID != got.DispatchID {
		t.Fatalf("enqueued dispatch ID = %q, want %q", enqueuedDispatchID, got.DispatchID)
	}
}

func TestEnqueueImmediateLADispatchFailsPendingRowWhenPipelineRejects(t *testing.T) {
	queueErr := errors.New("queue unavailable")
	store := &serverStoreStub{t: t}
	var failedDispatchID string
	store.updateLADispatchStatusFunc = func(ctx context.Context, dispatchID string, status string) error {
		if status != "FAILED" {
			t.Fatalf("UpdateLADispatchStatus status = %q, want FAILED", status)
		}
		failedDispatchID = dispatchID
		return nil
	}
	store.failLAJobIfActiveFunc = func(ctx context.Context, jobID string) error {
		t.Fatalf("FailLAJobIfActive should not be called for an update dispatch")
		return nil
	}
	store.markLADispatchEnqueuedFunc = func(ctx context.Context, dispatchID string) error {
		t.Fatalf("MarkLADispatchEnqueued should not be called after pipeline rejection")
		return nil
	}

	server := New(
		service.NewPushBoyService(store, ""),
		&serverJobPipeline{t: t, failOnSubmit: true},
		&serverLAJobPipeline{t: t, submitErr: queueErr},
	)
	dispatch := &storage.LiveActivityDispatch{ID: "dispatch-1", Action: model.LiveActivityActionUpdate}
	job := &storage.LiveActivityJob{ID: "job-1"}
	err := server.enqueueImmediateLADispatch(job, dispatch, model.LAJobItem{DispatchID: dispatch.ID})

	if !errors.Is(err, queueErr) {
		t.Fatalf("enqueueImmediateLADispatch error = %v, want %v", err, queueErr)
	}
	if failedDispatchID != dispatch.ID {
		t.Fatalf("failed dispatch ID = %q, want %q", failedDispatchID, dispatch.ID)
	}
}

func testRouter(t *testing.T, store storage.Store, jobPipeline pipeline.Pipeline[model.JobItem], laJobPipeline pipeline.Pipeline[model.LAJobItem]) http.Handler {
	t.Helper()

	return New(service.NewPushBoyService(store, ""), jobPipeline, laJobPipeline).setupRouter()
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

type serverLAJobPipeline struct {
	t            *testing.T
	failOnSubmit bool
	submitErr    error
	submitted    []model.LAJobItem
}

func (p *serverLAJobPipeline) Submit(ctx context.Context, item model.LAJobItem) error {
	if p.failOnSubmit {
		p.t.Helper()
		p.t.Fatalf("LA job pipeline Submit should not be called")
	}
	if p.submitErr != nil {
		return p.submitErr
	}
	p.submitted = append(p.submitted, item)
	return nil
}

func (p *serverLAJobPipeline) Receive(ctx context.Context) (pipeline.Delivery[model.LAJobItem], error) {
	return nil, pipeline.ErrClosed
}

func (p *serverLAJobPipeline) Close(ctx context.Context) error {
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
	ensureLAChannelFunc         func(context.Context, string, string, func(context.Context) (string, error)) (*storage.LiveActivityChannel, bool, error)
	getLAChannelFunc            func(context.Context, string) (*storage.LiveActivityChannel, error)
	deleteLAChannelFunc         func(context.Context, string, string) error
	createUserPublishJobFunc    func(context.Context, *storage.PublishJob) (*storage.PublishJob, error)
	updateJobStatusFunc         func(context.Context, string, model.NotificationJobStatus) error

	getLAJobByActivityIDFunc       func(context.Context, string) (*storage.LiveActivityJob, error)
	updateLAJobPayloadIfActiveFunc func(context.Context, string, json.RawMessage, json.RawMessage, time.Time) error
	createLADispatchFunc           func(context.Context, *storage.LiveActivityDispatch) (*storage.LiveActivityDispatch, error)
	updateLADispatchStatusFunc     func(context.Context, string, string) error
	markLADispatchEnqueuedFunc     func(context.Context, string) error
	failLAJobIfActiveFunc          func(context.Context, string) error
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

func (s *serverStoreStub) EnsureLAChannel(
	ctx context.Context,
	activityID string,
	topicID string,
	create func(context.Context) (string, error),
) (*storage.LiveActivityChannel, bool, error) {
	if s.ensureLAChannelFunc != nil {
		return s.ensureLAChannelFunc(ctx, activityID, topicID, create)
	}
	s.unused("EnsureLAChannel")
	return nil, false, errors.New("unexpected EnsureLAChannel")
}

func (s *serverStoreStub) GetLAChannelByActivityID(ctx context.Context, activityID string) (*storage.LiveActivityChannel, error) {
	if s.getLAChannelFunc != nil {
		return s.getLAChannelFunc(ctx, activityID)
	}
	s.unused("GetLAChannelByActivityID")
	return nil, errors.New("unexpected GetLAChannelByActivityID")
}

func (s *serverStoreStub) DeleteLAChannel(ctx context.Context, activityID, channelID string) error {
	if s.deleteLAChannelFunc != nil {
		return s.deleteLAChannelFunc(ctx, activityID, channelID)
	}
	s.unused("DeleteLAChannel")
	return errors.New("unexpected DeleteLAChannel")
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
	if s.getLAJobByActivityIDFunc != nil {
		return s.getLAJobByActivityIDFunc(ctx, activityID)
	}
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
	if s.updateLAJobPayloadIfActiveFunc != nil {
		return s.updateLAJobPayloadIfActiveFunc(ctx, jobID, payload, options, updatedAt)
	}
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
	if s.failLAJobIfActiveFunc != nil {
		return s.failLAJobIfActiveFunc(ctx, jobID)
	}
	s.unused("FailLAJobIfActive")
	return errors.New("unexpected FailLAJobIfActive")
}

func (s *serverStoreStub) CreateLADispatch(ctx context.Context, dispatch *storage.LiveActivityDispatch) (*storage.LiveActivityDispatch, error) {
	if s.createLADispatchFunc != nil {
		return s.createLADispatchFunc(ctx, dispatch)
	}
	s.unused("CreateLADispatch")
	return nil, errors.New("unexpected CreateLADispatch")
}

func (s *serverStoreStub) UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error {
	if s.updateLADispatchStatusFunc != nil {
		return s.updateLADispatchStatusFunc(ctx, dispatchID, status)
	}
	s.unused("UpdateLADispatchStatus")
	return errors.New("unexpected UpdateLADispatchStatus")
}

func (s *serverStoreStub) MarkLADispatchEnqueued(ctx context.Context, dispatchID string) error {
	if s.markLADispatchEnqueuedFunc != nil {
		return s.markLADispatchEnqueuedFunc(ctx, dispatchID)
	}
	s.unused("MarkLADispatchEnqueued")
	return errors.New("unexpected MarkLADispatchEnqueued")
}

func (s *serverStoreStub) GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*storage.LiveActivityTokenBatch, error) {
	s.unused("GetLATokenBatchForDispatch")
	return nil, errors.New("unexpected GetLATokenBatchForDispatch")
}

func (s *serverStoreStub) CompleteLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	s.unused("CompleteLADispatchEnqueue")
	return errors.New("unexpected CompleteLADispatchEnqueue")
}

func (s *serverStoreStub) FailLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	s.unused("FailLADispatchEnqueue")
	return errors.New("unexpected FailLADispatchEnqueue")
}

func (s *serverStoreStub) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.LASendOutcome) error {
	s.unused("ApplyLAOutcomeBatch")
	return errors.New("unexpected ApplyLAOutcomeBatch")
}

func (s *serverStoreStub) InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error) {
	s.unused("InvalidateExpiredLAUpdateTokens")
	return 0, errors.New("unexpected InvalidateExpiredLAUpdateTokens")
}

func (s *serverStoreStub) SupersedeLADispatchIfStale(ctx context.Context, dispatchID string, emittedCount int) (bool, error) {
	s.unused("SupersedeLADispatchIfStale")
	return false, errors.New("unexpected SupersedeLADispatchIfStale")
}

func (s *serverStoreStub) Close() error {
	s.unused("Close")
	return errors.New("unexpected Close")
}

type serverLAChannelProvider struct {
	channelID        string
	deletedChannelID string
	createError      error
}

func (p *serverLAChannelProvider) CreateLiveActivityChannel(context.Context) (string, error) {
	return p.channelID, p.createError
}

func (p *serverLAChannelProvider) DeleteLiveActivityChannel(_ context.Context, channelID string) error {
	p.deletedChannelID = channelID
	return nil
}
