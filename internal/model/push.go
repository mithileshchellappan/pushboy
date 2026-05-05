package model

import "fmt"

type Platform string

const (
	APNS Platform = "apns"
	FCM  Platform = "fcm"
)

type NotificationJobStatus string

const (
	NotificationJobStatusQueued     NotificationJobStatus = "QUEUED"
	NotificationJobStatusScheduled  NotificationJobStatus = "SCHEDULED"
	NotificationJobStatusInProgress NotificationJobStatus = "IN_PROGRESS"
	NotificationJobStatusDispatched NotificationJobStatus = "DISPATCHED"
	NotificationJobStatusCompleted  NotificationJobStatus = "COMPLETED"
	NotificationJobStatusFailed     NotificationJobStatus = "FAILED"
)

type NotificationPayload struct {
	// Required fields
	Title string `json:"title"`
	Body  string `json:"body"`

	// Rich content
	ImageURL string `json:"image_url,omitempty"` // URL to image (FCM: native, APNs: via mutable-content)
	Sound    string `json:"sound,omitempty"`     // "default" or custom sound filename
	Badge    *int   `json:"badge,omitempty"`     // App icon badge (nil = don't change, 0 = clear)

	// Custom app data - passed through to the app
	Data map[string]string `json:"data,omitempty"`

	// Behavior options
	Silent     bool   `json:"silent,omitempty"`      // Background/silent notification (no visible alert)
	CollapseID string `json:"collapse_id,omitempty"` // Replace previous notification with same ID
	Priority   string `json:"priority,omitempty"`    // "high" (default) or "normal"
	TTL        int    `json:"ttl,omitempty"`         // Seconds until notification expires (0 = no expiry)

	// Grouping and actions
	ThreadID string `json:"thread_id,omitempty"` // Group related notifications together
	Category string `json:"category,omitempty"`  // Actionable notification category ID
}

//Helper methods

func ParsePlatform(s string) (Platform, error) {
	switch Platform(s) {
	case APNS, FCM:
		return Platform(s), nil
	default:
		return "", fmt.Errorf("invalid platform: %q", s)
	}
}

func ParseNotificationJobStatus(s string) (NotificationJobStatus, error) {
	switch NotificationJobStatus(s) {
	case NotificationJobStatusQueued,
		NotificationJobStatusScheduled,
		NotificationJobStatusInProgress,
		NotificationJobStatusDispatched,
		NotificationJobStatusCompleted,
		NotificationJobStatusFailed:
		return NotificationJobStatus(s), nil
	default:
		return "", fmt.Errorf("invalid notification job status: %q", s)
	}
}
