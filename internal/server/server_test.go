package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type fakeStore struct {
	storage.Store

	getUser                func(context.Context, string) (*storage.User, error)
	getTopicByID           func(context.Context, string) (*storage.Topic, error)
	listUsers              func(context.Context, storage.PageQuery) ([]storage.User, error)
	listTopicSubscribers   func(context.Context, string, storage.PageQuery) ([]storage.TopicSubscriber, error)
	listTopicNotifications func(context.Context, string, storage.NotificationListQuery) ([]storage.PublishJob, error)
	listUserNotifications  func(context.Context, string, storage.NotificationListQuery) ([]storage.PublishJob, error)
	getJobStatus           func(context.Context, string) (*storage.PublishJob, error)
}

func (f *fakeStore) GetUser(ctx context.Context, userID string) (*storage.User, error) {
	if f.getUser != nil {
		return f.getUser(ctx, userID)
	}
	return &storage.User{ID: userID, CreatedAt: "2026-05-01T10:00:00Z"}, nil
}

func (f *fakeStore) GetTopicByID(ctx context.Context, topicID string) (*storage.Topic, error) {
	if f.getTopicByID != nil {
		return f.getTopicByID(ctx, topicID)
	}
	return &storage.Topic{ID: topicID, Name: topicID}, nil
}

func (f *fakeStore) ListUsers(ctx context.Context, query storage.PageQuery) ([]storage.User, error) {
	return f.listUsers(ctx, query)
}

func (f *fakeStore) ListTopicSubscribers(ctx context.Context, topicID string, query storage.PageQuery) ([]storage.TopicSubscriber, error) {
	return f.listTopicSubscribers(ctx, topicID, query)
}

func (f *fakeStore) ListTopicNotifications(ctx context.Context, topicID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
	return f.listTopicNotifications(ctx, topicID, query)
}

func (f *fakeStore) ListUserNotifications(ctx context.Context, userID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
	return f.listUserNotifications(ctx, userID, query)
}

func (f *fakeStore) GetJobStatus(ctx context.Context, jobID string) (*storage.PublishJob, error) {
	return f.getJobStatus(ctx, jobID)
}

func newTestHandler(store *fakeStore) http.Handler {
	svc := service.NewPushBoyService(store, "")
	return New(svc, nil).setupRouter()
}

func TestListUsersPagination(t *testing.T) {
	store := &fakeStore{
		listUsers: func(ctx context.Context, query storage.PageQuery) ([]storage.User, error) {
			if query.Limit != 2 {
				t.Fatalf("expected storage limit 2, got %d", query.Limit)
			}
			if query.Cursor.SortValue != "" || query.Cursor.ID != "" {
				t.Fatalf("expected empty cursor, got %+v", query.Cursor)
			}
			return []storage.User{
				{ID: "user-2", CreatedAt: "2026-05-01T11:00:00Z"},
				{ID: "user-1", CreatedAt: "2026-05-01T10:00:00Z"},
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/users?limit=1", nil)
	rec := httptest.NewRecorder()
	newTestHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response listResponse[storage.User]
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].ID != "user-2" {
		t.Fatalf("unexpected items: %+v", response.Items)
	}
	if !response.HasMore || response.NextCursor == "" {
		t.Fatalf("expected next cursor, got %+v", response)
	}

	cursor, err := parseCursor(response.NextCursor, "/v1/users", "")
	if err != nil {
		t.Fatalf("decode next cursor: %v", err)
	}
	if cursor.SortValue != "2026-05-01T11:00:00Z" || cursor.ID != "user-2" {
		t.Fatalf("unexpected cursor: %+v", cursor)
	}
}

func TestListRoutesRejectBadQueryParams(t *testing.T) {
	handler := newTestHandler(&fakeStore{})

	tests := []string{
		"/v1/users?limit=0",
		"/v1/users?limit=201",
		"/v1/users?cursor=not-a-cursor",
		"/v1/topics/topic-1/notifications?status=completed",
		"/v1/topics/topic-1/notifications?status=UNKNOWN",
	}

	for _, target := range tests {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", target, rec.Code)
		}
	}
}

func TestListScopedRoutesReturnNotFound(t *testing.T) {
	handler := newTestHandler(&fakeStore{
		getUser: func(ctx context.Context, userID string) (*storage.User, error) {
			return nil, storage.Errors.NotFound
		},
		getTopicByID: func(ctx context.Context, topicID string) (*storage.Topic, error) {
			return nil, storage.Errors.NotFound
		},
	})

	tests := []string{
		"/v1/topics/missing/subscribers",
		"/v1/topics/missing/notifications",
		"/v1/users/missing/notifications",
	}

	for _, target := range tests {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d", target, rec.Code)
		}
	}
}

func TestTopicNotificationsPassStatusFilter(t *testing.T) {
	store := &fakeStore{
		listTopicNotifications: func(ctx context.Context, topicID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
			if topicID != "topic-1" {
				t.Fatalf("unexpected topicID %q", topicID)
			}
			if query.Status != "COMPLETED" {
				t.Fatalf("expected status COMPLETED, got %q", query.Status)
			}
			if query.Limit != defaultPageLimit+1 {
				t.Fatalf("expected default storage limit %d, got %d", defaultPageLimit+1, query.Limit)
			}
			return []storage.PublishJob{
				{ID: "job-1", TopicID: "topic-1", Status: "COMPLETED", CreatedAt: "2026-05-01T10:00:00Z"},
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/topics/topic-1/notifications?status=COMPLETED", nil)
	rec := httptest.NewRecorder()
	newTestHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCanonicalNotificationLookupAndOldRouteRemoval(t *testing.T) {
	handler := newTestHandler(&fakeStore{
		getJobStatus: func(ctx context.Context, jobID string) (*storage.PublishJob, error) {
			if jobID != "job-1" {
				t.Fatalf("unexpected jobID %q", jobID)
			}
			return &storage.PublishJob{ID: "job-1", Status: "QUEUED", CreatedAt: "2026-05-01T10:00:00Z"}, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/notifications/job-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/topics/topic-1/publish/job-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected old route to be removed with 404, got %d", rec.Code)
	}
}
