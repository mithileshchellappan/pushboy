package worker

type WorkKind string

const (
	WorkKindNotification WorkKind = "notification"
	WorkKindLA           WorkKind = "la"
)

type WorkItem struct {
	Kind           WorkKind
	JobID          string
	LAID           string
	ClaimedVersion int64
	ClaimToken     string
}
