package worker

import "github.com/mithileshchellappan/pushboy/internal/storage"

type WorkKind string

const (
	WorkKindNotification WorkKind = "notification"
	WorkKindLA           WorkKind = "la"
)

type WorkItem struct {
	Kind    WorkKind
	JobData *storage.PublishJob
	LAData  *storage.LAActivity
}
