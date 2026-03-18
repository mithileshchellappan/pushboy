package worker

import "github.com/mithileshchellappan/pushboy/internal/storage"

type WorkKind string

const (
	WorkKindNotification WorkKind = "notification"
	WorkKindLA           WorkKind = "la"
)

type WorkItem struct {
	ID           string
	Kind         WorkKind
	Notification *NotificationWork
	LA           *LAWork
}

type LAWork struct {
	storage.LASnapshot
	ClaimedVersion int64
	ClaimToken     string
}

type NotificationWork struct {
	storage.NotificationSnapshot
}
