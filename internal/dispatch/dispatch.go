package dispatch

import (
	"context"
	"encoding/json"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

type Dispatcher interface {
	Send(ctx context.Context, token string, payload *model.NotificationPayload) error
}

type LiveActivityRequest struct {
	Action       model.LiveActivityAction
	ActivityType string
	Payload      json.RawMessage
	Options      json.RawMessage
}

type LiveActivityDispatcher interface {
	SendLiveActivity(ctx context.Context, token string, request *LiveActivityRequest) error
}
