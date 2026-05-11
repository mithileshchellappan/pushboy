package server

import (
	"net/http/httptest"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestNotificationListQueryFromRequestStatusValidation(t *testing.T) {
	t.Run("invalid status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/users/user-1/notifications?status=queued", nil)

		if _, _, err := notificationListQueryFromRequest(req, "/v1/users/user-1/notifications"); err == nil {
			t.Fatalf("notificationListQueryFromRequest error = nil, want invalid status error")
		}
	})

	t.Run("valid status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/users/user-1/notifications?status=QUEUED&limit=10", nil)

		got, visibleLimit, err := notificationListQueryFromRequest(req, "/v1/users/user-1/notifications")
		if err != nil {
			t.Fatalf("notificationListQueryFromRequest error = %v", err)
		}
		if got.Status != model.NotificationJobStatusQueued {
			t.Fatalf("status = %q, want QUEUED", got.Status)
		}
		if visibleLimit != 10 || got.Limit != 11 {
			t.Fatalf("limit = %d/%d, want visible 10 and storage 11", visibleLimit, got.Limit)
		}
	})
}
