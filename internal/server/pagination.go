package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
	cursorVersion    = 1
)

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

type pageCursor struct {
	Version   int    `json:"v"`
	Route     string `json:"route"`
	Status    string `json:"status,omitempty"`
	SortValue string `json:"sort_value"`
	ID        string `json:"id"`
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultPageLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if limit <= 0 || limit > maxPageLimit {
		return 0, errors.New("limit out of range")
	}

	return limit, nil
}

func parseNotificationStatus(raw string) (model.NotificationJobStatus, error) {
	if raw == "" {
		return "", nil
	}
	return model.ParseNotificationJobStatus(raw)
}

func parseCursor(raw string, route string, status model.NotificationJobStatus) (storage.PageCursor, error) {
	if raw == "" {
		return storage.PageCursor{}, nil
	}

	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return storage.PageCursor{}, err
	}

	var cursor pageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return storage.PageCursor{}, err
	}

	if cursor.Version != cursorVersion || cursor.Route != route || cursor.Status != string(status) || cursor.SortValue == "" || cursor.ID == "" {
		return storage.PageCursor{}, errors.New("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.SortValue); err != nil {
		return storage.PageCursor{}, err
	}

	return storage.PageCursor{
		SortValue: cursor.SortValue,
		ID:        cursor.ID,
	}, nil
}

func encodeCursor(route string, status model.NotificationJobStatus, sortValue string, id string) string {
	payload, err := json.Marshal(pageCursor{
		Version:   cursorVersion,
		Route:     route,
		Status:    string(status),
		SortValue: sortValue,
		ID:        id,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func pageQueryFromRequest(r *http.Request, route string) (storage.PageQuery, int, error) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return storage.PageQuery{}, 0, err
	}

	cursor, err := parseCursor(r.URL.Query().Get("cursor"), route, "")
	if err != nil {
		return storage.PageQuery{}, 0, err
	}

	return storage.PageQuery{
		Limit:  limit + 1,
		Cursor: cursor,
	}, limit, nil
}

func notificationListQueryFromRequest(r *http.Request, route string) (storage.NotificationListQuery, int, error) {
	status, err := parseNotificationStatus(r.URL.Query().Get("status"))
	if err != nil {
		return storage.NotificationListQuery{}, 0, err
	}

	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return storage.NotificationListQuery{}, 0, err
	}

	cursor, err := parseCursor(r.URL.Query().Get("cursor"), route, status)
	if err != nil {
		return storage.NotificationListQuery{}, 0, err
	}

	return storage.NotificationListQuery{
		Limit:  limit + 1,
		Cursor: cursor,
		Status: status,
	}, limit, nil
}

func notificationJobCursorTime(job storage.PublishJob) string {
	if job.ScheduledAt != "" {
		return job.ScheduledAt
	}
	return job.CreatedAt
}

func makeListResponse[T any](items []T, limit int, route string, status model.NotificationJobStatus, keyFunc func(T) (string, string)) listResponse[T] {
	if items == nil {
		items = make([]T, 0)
	}

	response := listResponse[T]{
		Items: items,
	}

	if len(items) <= limit {
		return response
	}

	response.HasMore = true
	response.Items = items[:limit]
	sortValue, id := keyFunc(response.Items[len(response.Items)-1])
	response.NextCursor = encodeCursor(route, status, sortValue, id)
	return response
}
