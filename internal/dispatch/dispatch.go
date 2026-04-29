package dispatch

import (
	"context"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

type Dispatcher interface {
	Send(ctx context.Context, token string, payload *model.NotificationPayload) error
}

type LiveActivityDispatcher interface {
	SendLiveActivity(ctx context.Context, token string, request *model.LiveActivityRequest) error
}
