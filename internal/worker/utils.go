package worker

import (
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func ConvertNotificationToWorkItem(job *storage.PublishJob) *WorkItem {
	return &WorkItem{
		ID:   uuid.New().String(),
		Kind: WorkKindNotification,
		Notification: &NotificationWork{
			NotificationSnapshot: job.NotificationSnapshot,
		},
		LA: nil,
	}
}

func ConvertLAToWorkItem(job *storage.LAActivity) *WorkItem {
	return &WorkItem{
		ID:           uuid.New().String(),
		Kind:         WorkKindLA,
		Notification: nil,
		LA: &LAWork{
			LASnapshot:     job.LASnapshot,
			ClaimToken:     job.ClaimToken,
			ClaimedVersion: job.ClaimedVersion,
		},
	}
}
