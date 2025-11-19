package dispatch

import (
	"context"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type NotificationPayload struct {
	Title string
	Body  string
}

type Dispatcher interface {
	Send(ctx context.Context, sub *storage.Subscription, payload *NotificationPayload) error
}
