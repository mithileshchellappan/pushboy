package storage

type LAPlatform string

const (
	LAPlatformIOS     LAPlatform = "ios"
	LAPlatformAndroid LAPlatform = "android"
)

type LAAudienceKind string

const (
	LAAudienceKindUser  LAAudienceKind = "user"
	LAAudienceKindTopic LAAudienceKind = "topic"
)

type LAEvent string

const (
	LAEventStart  LAEvent = "start"
	LAEventUpdate LAEvent = "update"
	LAEventEnd    LAEvent = "end"
)

type LAActivityStatus string

const (
	LAActivityStatusStarting LAActivityStatus = "starting"
	LAActivityStatusActive   LAActivityStatus = "active"
	LAActivityStatusEnding   LAActivityStatus = "ending"
	LAActivityStatusEnded    LAActivityStatus = "ended"
	LAActivityStatusFailed   LAActivityStatus = "failed"
)

type LAInstanceStatus string

const (
	LAInstanceStatusPendingStart LAInstanceStatus = "pending_start"
	LAInstanceStatusActive       LAInstanceStatus = "active"
	LAInstanceStatusEnded        LAInstanceStatus = "ended"
	LAInstanceStatusFailed       LAInstanceStatus = "failed"
)

type LARegistration struct {
	ID               string
	UserID           string
	Platform         LAPlatform
	StartToken       string
	AutoStartEnabled bool
	Enabled          bool
	CreatedAt        string
	UpdatedAt        string
}

type LAUserTopicPreference struct {
	UserID    string
	TopicID   string
	CreatedAt string
}

type LATopicSubscription struct {
	LARegistrationID string
	TopicID          string
	CreatedAt        string
}

type LAAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Sound string `json:"sound,omitempty"`
}

type LAPayload struct {
	Kind            string         `json:"kind"`
	ExternalRef     string         `json:"external_ref,omitempty"`
	Event           LAEvent        `json:"event"`
	StartAttributes map[string]any `json:"start_attributes,omitempty"`
	State           map[string]any `json:"state,omitempty"`
	Alert           *LAAlert       `json:"alert,omitempty"`
	StaleAt         string         `json:"stale_at,omitempty"`
	DismissAt       string         `json:"dismiss_at,omitempty"`
}

type LAActivity struct {
	ID              string
	Kind            string
	AudienceKind    LAAudienceKind
	UserID          string
	TopicID         string
	ExternalRef     string
	Status          LAActivityStatus
	PendingEvent    LAEvent
	StartAttributes map[string]any
	CurrentState    map[string]any
	Alert           *LAAlert
	StateVersion    int64
	ClaimedVersion  int64
	ClaimToken      string
	ClaimUntil      string
	DispatchNeeded  bool
	DispatchAfter   string
	LastDispatchAt  string
	LastError       string
	CreatedAt       string
	UpdatedAt       string
}

type LAInstance struct {
	ID                 string
	LAID               string
	LARegistrationID   string
	Platform           LAPlatform
	Status             LAInstanceStatus
	DeliveryToken      string
	LastSuccessVersion int64
	LastAttemptVersion int64
	LastAttemptAt      string
	NextRetryAt        string
	FailureCount       int
	LastError          string
	CreatedAt          string
	UpdatedAt          string
}
