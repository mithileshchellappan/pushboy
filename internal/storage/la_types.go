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

type LAAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Sound string `json:"sound,omitempty"`
}

type LASnapshot struct {
	ID              string
	Kind            string
	AudienceKind    LAAudienceKind
	UserID          string
	TopicID         string
	ExternalRef     string
	PendingEvent    LAEvent
	StartAttributes map[string]any
	CurrentState    map[string]any
	Alert           *LAAlert
}

type LAActivity struct {
	LASnapshot
	Status         LAActivityStatus
	StateVersion   int64
	ClaimedVersion int64
	ClaimToken     string
	ClaimUntil     string
	DispatchNeeded bool
	DispatchAfter  string
	LastDispatchAt string
	LastError      string
	CreatedAt      string
	UpdatedAt      string
}

type LAUpdateToken struct {
	ID        string
	UserID    string
	TopicID   string
	Platform  LAPlatform
	Token     string
	ExpiresAt string
	CreatedAt string
	UpdatedAt string
}
