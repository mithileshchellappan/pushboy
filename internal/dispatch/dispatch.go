package dispatch

import (
	"context"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

// NotificationPayload is the unified notification format that works across all platforms.
// Pushboy translates this to platform-specific formats (APNs/FCM) internally.
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

// LAPayload is the unified live activity / live update payload that platform
// dispatchers translate into APNs ActivityKit or Android live-update messages.
// It carries only sendable state, not DB coordination fields.
type LAPayload struct {
	LAID            string            `json:"la_id"`
	Kind            string            `json:"kind"`
	ExternalRef     string            `json:"external_ref,omitempty"`
	Event           storage.LAEvent   `json:"event"`
	StartAttributes map[string]any    `json:"start_attributes,omitempty"`
	CurrentState    map[string]any    `json:"current_state,omitempty"`
	Alert           *storage.LAAlert  `json:"alert,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type Dispatcher interface {
	Send(ctx context.Context, token *storage.Token, payload *NotificationPayload) error
}
